package handler

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/calnode/calnode/internal/secret"
	"github.com/calnode/calnode/internal/telegram"
)

// The personal booking assistant's settings, and the Telegram bot its owner reaches it
// through. This file is the connection only: the switch, the bot's key, tying a Telegram
// chat to an account, and receiving messages. What the assistant does with a message or a
// button tap is in assistant_owner.go.
//
// Two rules hold everything here together:
//
//   - The bot answers only chats that are linked to an account. A chat is linked by spending
//     a one-time code made in Settings, so being able to message the bot proves nothing.
//   - A delivery is trusted only if it carries the secret Bookmeet gave Telegram when it
//     registered the webhook. The endpoint is public; the secret is what makes it Telegram's.

const (
	telegramWebhookPath   = "/v1/telegram/webhook"
	telegramSecretHeader  = "X-Telegram-Bot-Api-Secret-Token" // #nosec G101 -- a header name, not a credential
	telegramLinkCodeTTL   = 15 * time.Minute
	telegramReplyDeadline = 8 * time.Second
)

// TelegramConfig is the stored bot connection, decrypted.
type TelegramConfig struct {
	Token         string
	Username      string
	WebhookSecret string
}

// LoadTelegramFromDB reads the bot connection. Nil (not an error) when no bot is set.
func LoadTelegramFromDB(db *sql.DB, encKey [32]byte) (*TelegramConfig, error) {
	var tokenEnc, username, secretEnc string
	err := db.QueryRow(`
		SELECT telegram_bot_token_enc, telegram_bot_username, telegram_webhook_secret_enc
		FROM server_settings WHERE id = 1`).Scan(&tokenEnc, &username, &secretEnc)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && tokenEnc == "") {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	token, err := secret.Decrypt(encKey, tokenEnc)
	if err != nil {
		return nil, fmt.Errorf("decrypt telegram bot token: %w", err)
	}
	hookSecret, err := secret.Decrypt(encKey, secretEnc)
	if err != nil {
		return nil, fmt.Errorf("decrypt telegram webhook secret: %w", err)
	}
	return &TelegramConfig{Token: token, Username: username, WebhookSecret: hookSecret}, nil
}

// SetTelegram swaps the live bot client and the secret its deliveries must carry. A nil
// client means no bot is connected. Hot-reloadable from the settings page.
func (h *Handler) SetTelegram(c *telegram.Client, webhookSecret string) {
	h.telegramMu.Lock()
	h.telegram, h.telegramSecret = c, webhookSecret
	h.telegramMu.Unlock()
}

func (h *Handler) getTelegram() (*telegram.Client, string) {
	h.telegramMu.RLock()
	defer h.telegramMu.RUnlock()
	return h.telegram, h.telegramSecret
}

// SetTelegramWebhookBase overrides the address Telegram is told to deliver to. Telegram must
// be able to reach it without signing in, so a deployment that puts its public address behind
// a login (the staging copy, behind Cloudflare Access) names another one here.
func (h *Handler) SetTelegramWebhookBase(base string) {
	h.telegramWebhookBase = strings.TrimRight(base, "/")
}

// newTelegramClient builds a client for a token. Tests point it at a stand-in server.
func (h *Handler) newTelegramClient(token string) *telegram.Client {
	c := telegram.New(token)
	if h.telegramAPIBase != "" {
		c.WithBaseURL(h.telegramAPIBase)
	}
	return c
}

func (h *Handler) telegramWebhookURL() string {
	base := h.telegramWebhookBase
	if base == "" {
		base = h.publicURL()
	}
	return strings.TrimRight(base, "/") + telegramWebhookPath
}

func (h *Handler) assistantEnabled(ctx context.Context) bool {
	var enabled int
	_ = h.db.QueryRowContext(ctx, `SELECT assistant_enabled FROM server_settings WHERE id = 1`).Scan(&enabled)
	return enabled != 0
}

// assistantActive reports whether the assistant can actually work: switched on, a bot
// connected, and the AI model live. The switch alone is not enough - the model can be turned
// off or the bot removed after it was flipped.
func (h *Handler) assistantActive(ctx context.Context) bool {
	bot, _ := h.getTelegram()
	return bot != nil && h.getLLM() != nil && h.assistantEnabled(ctx)
}

// GetAssistantSettings handles GET /v1/settings/assistant. The bot's key is never returned.
// Every signed-in user gets their own link state; the workspace's settings are the admin's.
func (h *Handler) GetAssistantSettings(w http.ResponseWriter, r *http.Request) {
	user, ok := userFromContext(r.Context())
	if !ok {
		h.writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var tokenEnc, username string
	var enabled int
	if err := h.db.QueryRowContext(r.Context(), `
		SELECT assistant_enabled, telegram_bot_token_enc, telegram_bot_username
		FROM server_settings WHERE id = 1`).Scan(&enabled, &tokenEnc, &username); err != nil && !errors.Is(err, sql.ErrNoRows) {
		h.logger.ErrorContext(r.Context(), "assistant settings: query", "error", err)
		h.writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	var linkedName string
	var linked bool
	switch err := h.db.QueryRowContext(r.Context(),
		`SELECT telegram_name FROM telegram_links WHERE user_id = ?`, user.ID).Scan(&linkedName); {
	case err == nil:
		linked = true
	case !errors.Is(err, sql.ErrNoRows):
		h.logger.ErrorContext(r.Context(), "assistant settings: link", "error", err)
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"enabled":   enabled != 0,
		"active":    h.assistantActive(r.Context()),
		"ai_active": h.getLLM() != nil,
		"telegram": map[string]any{
			"configured":   tokenEnc != "",
			"bot_username": username,
		},
		"link": map[string]any{
			"linked":        linked,
			"telegram_name": linkedName,
		},
	})
}

// PatchAssistantSettings handles PATCH /v1/settings/assistant (admin only).
//
// telegram_bot_token is a pointer so "omitted" (keep the stored key) stays distinguishable
// from "" (disconnect the bot). A new key is checked with Telegram and its webhook registered
// before anything is saved, so a key that is stored is a key that works.
func (h *Handler) PatchAssistantSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireAdmin(w, r); !ok {
		return
	}
	if h.demoMode {
		h.writeError(w, http.StatusServiceUnavailable, "not available in the demo")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var req struct {
		Enabled          *bool   `json:"enabled"`
		TelegramBotToken *string `json:"telegram_bot_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	ctx := r.Context()

	if req.TelegramBotToken != nil {
		token := strings.TrimSpace(*req.TelegramBotToken)
		var err error
		if token == "" {
			err = h.disconnectTelegram(ctx)
		} else {
			err = h.connectTelegram(ctx, token)
		}
		var userErr *assistantSettingsError
		if errors.As(err, &userErr) {
			h.writeError(w, http.StatusBadRequest, userErr.msg)
			return
		}
		if err != nil {
			h.logger.ErrorContext(ctx, "assistant settings: telegram", "error", err)
			h.writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}

	if req.Enabled != nil {
		if *req.Enabled {
			// Validated on change, not on mention: only the act of switching it on is
			// checked, so a later save of something else is never blocked by this.
			if !h.assistantEnabled(ctx) {
				if bot, _ := h.getTelegram(); bot == nil {
					h.writeError(w, http.StatusBadRequest, "Connect a Telegram bot before switching the assistant on.")
					return
				}
				if h.getLLM() == nil {
					h.writeError(w, http.StatusBadRequest, "Set up and enable the AI model above before switching the assistant on.")
					return
				}
			}
		}
		v := 0
		if *req.Enabled {
			v = 1
		}
		if _, err := h.db.ExecContext(ctx,
			`UPDATE server_settings SET assistant_enabled = ?, updated_at = datetime('now') WHERE id = 1`, v); err != nil {
			h.logger.ErrorContext(ctx, "assistant settings: save switch", "error", err)
			h.writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}
	h.GetAssistantSettings(w, r)
}

// assistantSettingsError is a failure the admin can act on; its text goes to the page.
type assistantSettingsError struct{ msg string }

func (e *assistantSettingsError) Error() string { return e.msg }

// connectTelegram checks a bot key with Telegram, registers this server as where the bot's
// messages go, and only then stores the key.
func (h *Handler) connectTelegram(ctx context.Context, token string) error {
	if !telegram.ValidToken(token) {
		return &assistantSettingsError{"That doesn't look like a Telegram bot key. Copy the whole key BotFather gave you."}
	}
	hookURL := h.telegramWebhookURL()
	if !strings.HasPrefix(hookURL, "https://") {
		return &assistantSettingsError{"Telegram can only deliver to a public https address, and this server's address is " + hookURL + "."}
	}
	bot := h.newTelegramClient(token)
	username, err := bot.GetMe(ctx)
	if err != nil {
		return &assistantSettingsError{"Telegram did not accept that key: " + telegramReason(err)}
	}
	hookSecret, err := randomURLToken(32)
	if err != nil {
		return err
	}
	if err := bot.SetWebhook(ctx, hookURL, hookSecret); err != nil {
		return &assistantSettingsError{"Telegram could not be pointed at this server: " + telegramReason(err)}
	}
	tokenEnc, err := secret.Encrypt(h.encKey, token)
	if err != nil {
		return fmt.Errorf("encrypt bot token: %w", err)
	}
	secretEnc, err := secret.Encrypt(h.encKey, hookSecret)
	if err != nil {
		return fmt.Errorf("encrypt webhook secret: %w", err)
	}
	// A different bot is a different conversation: nobody has opened a chat with it yet, so
	// links made through the old one could not be messaged. Same bot, same links.
	var previous string
	_ = h.db.QueryRowContext(ctx, `SELECT telegram_bot_username FROM server_settings WHERE id = 1`).Scan(&previous)
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if _, err := tx.ExecContext(ctx, `
		UPDATE server_settings
		SET telegram_bot_token_enc = ?, telegram_bot_username = ?, telegram_webhook_secret_enc = ?, updated_at = datetime('now')
		WHERE id = 1`, tokenEnc, username, secretEnc); err != nil {
		return err
	}
	if previous != username {
		if _, err := tx.ExecContext(ctx, `DELETE FROM telegram_links`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM telegram_link_codes`); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	h.SetTelegram(bot, hookSecret)
	return nil
}

// disconnectTelegram removes the bot: deliveries stop, the key is forgotten, the assistant
// is switched off and every chat is unlinked.
func (h *Handler) disconnectTelegram(ctx context.Context) error {
	if bot, _ := h.getTelegram(); bot != nil {
		// Best effort. The stored secret is about to go, so a delivery that still arrives
		// is refused whether or not Telegram heard this.
		if err := bot.DeleteWebhook(ctx); err != nil {
			h.logger.WarnContext(ctx, "assistant settings: delete telegram webhook", "error", err)
		}
	}
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	for _, q := range []string{
		`UPDATE server_settings
		 SET telegram_bot_token_enc = '', telegram_bot_username = '', telegram_webhook_secret_enc = '',
		     assistant_enabled = 0, updated_at = datetime('now')
		 WHERE id = 1`,
		`DELETE FROM telegram_links`,
		`DELETE FROM telegram_link_codes`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	h.SetTelegram(nil, "")
	return nil
}

// telegramReason is the part of a Telegram failure that is safe and useful to show an admin.
func telegramReason(err error) string {
	var apiErr *telegram.APIError
	if errors.As(err, &apiErr) {
		if apiErr.Code == http.StatusUnauthorized || apiErr.Code == http.StatusNotFound {
			return "the key is not recognised."
		}
		return apiErr.Description
	}
	return "Telegram could not be reached. Try again in a moment."
}

// CreateTelegramLinkCode handles POST /v1/settings/assistant/telegram/link: a one-time link
// that opens the bot and ties the chat that opens it to the signed-in account.
func (h *Handler) CreateTelegramLinkCode(w http.ResponseWriter, r *http.Request) {
	user, ok := userFromContext(r.Context())
	if !ok {
		h.writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	cfg, err := LoadTelegramFromDB(h.db, h.encKey)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "telegram link: load bot", "error", err)
		h.writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if cfg == nil || cfg.Username == "" {
		h.writeError(w, http.StatusBadRequest, "No Telegram bot is connected yet.")
		return
	}
	code, err := randomURLToken(18)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	now := time.Now().UTC()
	// One live code per person: asking again retires the last one.
	if _, err := h.db.ExecContext(r.Context(), `DELETE FROM telegram_link_codes WHERE user_id = ? OR expires_at < ?`,
		user.ID, now.Format(time.RFC3339)); err != nil {
		h.logger.ErrorContext(r.Context(), "telegram link: clear codes", "error", err)
	}
	if _, err := h.db.ExecContext(r.Context(), `
		INSERT INTO telegram_link_codes (code_hash, user_id, expires_at, created_at) VALUES (?, ?, ?, ?)`,
		hashLinkCode(code), user.ID, now.Add(telegramLinkCodeTTL).Format(time.RFC3339), now.Format(time.RFC3339)); err != nil {
		h.logger.ErrorContext(r.Context(), "telegram link: store code", "error", err)
		h.writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"url":             "https://t.me/" + cfg.Username + "?start=" + code,
		"expires_minutes": int(telegramLinkCodeTTL / time.Minute),
	})
}

// DeleteTelegramLink handles DELETE /v1/settings/assistant/telegram/link: the signed-in
// user's chat stops being theirs, and the bot stops answering it.
func (h *Handler) DeleteTelegramLink(w http.ResponseWriter, r *http.Request) {
	user, ok := userFromContext(r.Context())
	if !ok {
		h.writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if _, err := h.db.ExecContext(r.Context(), `DELETE FROM telegram_links WHERE user_id = ?`, user.ID); err != nil {
		h.logger.ErrorContext(r.Context(), "telegram link: delete", "error", err)
		h.writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.GetAssistantSettings(w, r)
}

// TelegramWebhook handles POST /v1/telegram/webhook: one delivery from Telegram.
//
// It answers 404 to anything that does not carry the registered secret, the same as a route
// that is not there. Once a delivery is accepted the answer is always 200: Telegram re-sends
// what it is not thanked for, and a message this server chose to ignore should stay ignored.
func (h *Handler) TelegramWebhook(w http.ResponseWriter, r *http.Request) {
	bot, want := h.getTelegram()
	got := r.Header.Get(telegramSecretHeader)
	if bot == nil || want == "" || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		http.NotFound(w, r)
		return
	}
	var update telegram.Update
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&update); err != nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusOK)

	// A tap on a button under one of the bot's messages.
	if cq := update.CallbackQuery; cq != nil {
		if cq.Message == nil || cq.Message.Chat.Type != "private" {
			return
		}
		userID, linked := h.telegramLinkedUser(r.Context(), cq.Message.Chat.ID)
		if !linked || !h.assistantActive(r.Context()) {
			return
		}
		h.assistantRun(userID, func(ctx context.Context) { h.ownerHandleCallback(ctx, bot, userID, cq) })
		return
	}

	msg := update.Message
	// Private chats only. In a group the bot would be answering people who never linked.
	if msg == nil || msg.Chat.Type != "private" {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), telegramReplyDeadline)
	defer cancel()
	say := func(text string) {
		if err := bot.SendMessage(ctx, msg.Chat.ID, text); err != nil {
			h.logger.WarnContext(ctx, "telegram: send", "error", err)
		}
	}

	text := strings.TrimSpace(msg.Text)
	if code, isStart := strings.CutPrefix(text, "/start"); isStart {
		if code = strings.TrimSpace(code); code != "" {
			name, err := h.redeemTelegramLinkCode(ctx, code, msg.Chat.ID, msg.From.DisplayName())
			switch {
			case err == nil:
				say("Connected to " + name + "'s Bookmeet. This chat is now private to that account.")
			case errors.Is(err, errTelegramLinkCode):
				say("That link has expired or was already used. Open Settings → AI in Bookmeet and connect again.")
			default:
				h.logger.ErrorContext(ctx, "telegram: redeem link code", "error", err)
			}
			return
		}
	}

	userID, linked := h.telegramLinkedUser(ctx, msg.Chat.ID)
	if !linked {
		return // a stranger: no answer, not even a refusal
	}
	if !h.assistantActive(ctx) {
		return // switched off means quiet
	}
	if text == "" {
		return // a photo, a sticker, a voice note: nothing the assistant reads yet
	}
	chatID := msg.Chat.ID
	h.assistantRun(userID, func(ctx context.Context) { h.ownerHandleMessage(ctx, bot, userID, chatID, text) })
}

// telegramLinkedUser returns the account a chat is linked to. Every path that answers a
// chat goes through this first: an unlinked chat is a stranger.
func (h *Handler) telegramLinkedUser(ctx context.Context, chatID int64) (string, bool) {
	var userID string
	if err := h.db.QueryRowContext(ctx, `SELECT user_id FROM telegram_links WHERE chat_id = ?`, chatID).Scan(&userID); err != nil {
		return "", false
	}
	return userID, true
}

// PersonalLinkPage handles GET /i/{token}: where a guest lands from a personal link. The
// booking chat that belongs here is the next piece of work; until then the page confirms the
// link is real and records that it was opened.
func (h *Handler) PersonalLinkPage(w http.ResponseWriter, r *http.Request) {
	l, err := h.personalLinks().ByToken(r.Context(), r.PathValue("token"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	_ = h.personalLinks().MarkOpened(r.Context(), l.ID)
	owner := h.personalLinkOwnerName(r.Context(), l.UserID)
	line := "The booking chat for this link opens here soon. " + owner + " will be in touch if you need a time before then."
	if !l.Active() {
		line = "This link is no longer active. Ask " + owner + " for a new one."
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	fmt.Fprintf(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="robots" content="noindex"><title>Booking with %s</title><style>body{font:16px/1.5 system-ui,sans-serif;max-width:32rem;margin:15vh auto;padding:0 1.25rem;color:#252735}h1{font-weight:300;font-size:1.6rem}</style></head><body><h1>Hi %s</h1><p>%s</p></body></html>`,
		html.EscapeString(owner), html.EscapeString(firstName(l.GuestName)), html.EscapeString(line))
}

var errTelegramLinkCode = errors.New("telegram link code is not valid")

// redeemTelegramLinkCode spends a one-time code and links the chat to the code's account.
// It returns the account holder's name for the confirmation message.
func (h *Handler) redeemTelegramLinkCode(ctx context.Context, code string, chatID int64, telegramName string) (string, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	var userID string
	err = tx.QueryRowContext(ctx,
		`SELECT user_id FROM telegram_link_codes WHERE code_hash = ? AND expires_at > ?`, hashLinkCode(code), now).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errTelegramLinkCode
	}
	if err != nil {
		return "", err
	}
	// Spent whether or not the rest succeeds: a code is a single attempt.
	if _, err := tx.ExecContext(ctx, `DELETE FROM telegram_link_codes WHERE code_hash = ?`, hashLinkCode(code)); err != nil {
		return "", err
	}
	// One chat per account and one account per chat: the new link displaces both.
	if _, err := tx.ExecContext(ctx, `DELETE FROM telegram_links WHERE user_id = ? OR chat_id = ?`, userID, chatID); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO telegram_links (user_id, chat_id, telegram_name, linked_at) VALUES (?, ?, ?, ?)`,
		userID, chatID, telegramName, now); err != nil {
		return "", err
	}
	var name string
	if err := tx.QueryRowContext(ctx, `SELECT name FROM users WHERE id = ?`, userID).Scan(&name); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return name, nil
}

func hashLinkCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// randomURLToken returns n random bytes as URL-safe text (letters, digits, - and _), which is
// also the alphabet Telegram allows in a start parameter and a webhook secret.
func randomURLToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
