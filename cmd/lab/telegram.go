package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// telegramStandIn answers the handful of Bot API calls the app makes, keeps the owner's
// chat as Telegram would show it, and delivers what the owner types or taps to the app's
// webhook the way Telegram does: a POST carrying the secret the app registered.
type telegramStandIn struct {
	mu       sync.Mutex
	hookURL  string
	secret   string
	nextID   int64
	updateID int64
	chat     []*chatMessage
	version  int64     // bumps on every change, for the page to poll
	lastCall time.Time // the app's last call, to tell when it has finished
	typing   time.Time
	notice   string // the last tap's pop-up answer
	http     *http.Client
}

type chatButton struct {
	Text string `json:"text"`
	Data string `json:"data,omitempty"`
	URL  string `json:"url,omitempty"`
}

type chatMessage struct {
	ID      int64          `json:"id"`
	From    string         `json:"from"` // "bot" or "owner"
	Text    string         `json:"text"`
	Buttons [][]chatButton `json:"buttons,omitempty"`
	Edited  bool           `json:"edited,omitempty"`
	At      time.Time      `json:"at"`
}

func newTelegramStandIn() *telegramStandIn {
	return &telegramStandIn{nextID: 100, http: &http.Client{Timeout: 30 * time.Second}}
}

func (t *telegramStandIn) clearChat() {
	t.mu.Lock()
	t.chat, t.notice = nil, ""
	t.version++
	t.mu.Unlock()
}

func buttonsFrom(markup any) [][]chatButton {
	m, _ := markup.(map[string]any)
	rows, _ := m["inline_keyboard"].([]any)
	var out [][]chatButton
	for _, row := range rows {
		var r []chatButton
		cells, _ := row.([]any)
		for _, c := range cells {
			b, _ := c.(map[string]any)
			text, _ := b["text"].(string)
			data, _ := b["callback_data"].(string)
			url, _ := b["url"].(string)
			r = append(r, chatButton{Text: text, Data: data, URL: url})
		}
		out = append(out, r)
	}
	return out
}

// serveAPI handles POST /tg/bot<token>/<method>.
func (t *telegramStandIn) serveAPI(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/tg/bot")
	i := strings.LastIndex(rest, "/")
	if i < 0 {
		http.NotFound(w, r)
		return
	}
	token, method := rest[:i], rest[i+1:]
	reply := func(result any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
	}
	if token != botToken {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 401, "description": "Unauthorized"})
		return
	}
	var p map[string]any
	_ = json.NewDecoder(r.Body).Decode(&p)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastCall = time.Now()
	t.version++
	switch method {
	case "getMe":
		reply(map[string]any{"id": 1, "is_bot": true, "first_name": "Bookmeet Lab", "username": "bookmeet_lab_bot"})
	case "setWebhook":
		t.hookURL, _ = p["url"].(string)
		t.secret, _ = p["secret_token"].(string)
		reply(true)
	case "deleteWebhook":
		t.hookURL, t.secret = "", ""
		reply(true)
	case "sendMessage":
		t.nextID++
		text, _ := p["text"].(string)
		t.chat = append(t.chat, &chatMessage{ID: t.nextID, From: "bot", Text: text, Buttons: buttonsFrom(p["reply_markup"]), At: time.Now()})
		t.typing = time.Time{}
		reply(map[string]any{"message_id": t.nextID})
	case "editMessageText":
		id, _ := p["message_id"].(float64)
		for _, m := range t.chat {
			if m.ID == int64(id) {
				m.Text, _ = p["text"].(string)
				m.Buttons = buttonsFrom(p["reply_markup"])
				m.Edited = true
			}
		}
		reply(true)
	case "answerCallbackQuery":
		t.notice, _ = p["text"].(string)
		reply(true)
	case "sendChatAction":
		t.typing = time.Now()
		reply(true)
	default:
		reply(true)
	}
}

func (t *telegramStandIn) deliver(ctx context.Context, update map[string]any) error {
	t.mu.Lock()
	hook, secret := t.hookURL, t.secret
	t.updateID++
	update["update_id"] = t.updateID
	t.lastCall = time.Now()
	t.mu.Unlock()
	if hook == "" {
		return fmt.Errorf("the app has not registered a webhook")
	}
	raw, _ := json.Marshal(update)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hook, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", secret)
	resp, err := t.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the app answered the delivery with %d", resp.StatusCode)
	}
	return nil
}

// deliverMessage is the owner typing in their chat with the bot.
func (t *telegramStandIn) deliverMessage(ctx context.Context, text string) error {
	t.mu.Lock()
	t.nextID++
	id := t.nextID
	t.chat = append(t.chat, &chatMessage{ID: id, From: "owner", Text: text, At: time.Now()})
	t.version++
	t.mu.Unlock()
	return t.deliver(ctx, map[string]any{"message": map[string]any{
		"message_id": id, "text": text,
		"from": map[string]any{"id": ownerChat, "first_name": "Jason"},
		"chat": map[string]any{"id": ownerChat, "type": "private"},
	}})
}

// deliverTap is the owner pressing a button under one of the bot's messages.
func (t *telegramStandIn) deliverTap(ctx context.Context, messageID int64, data string) error {
	t.mu.Lock()
	t.notice = ""
	t.mu.Unlock()
	return t.deliver(ctx, map[string]any{"callback_query": map[string]any{
		"id": fmt.Sprintf("cb%d", time.Now().UnixNano()), "data": data,
		"from":    map[string]any{"id": ownerChat, "first_name": "Jason"},
		"message": map[string]any{"message_id": messageID, "chat": map[string]any{"id": ownerChat, "type": "private"}},
	}})
}

// findButton returns the newest button whose label contains want, with its message.
func (t *telegramStandIn) findButton(want string) (int64, string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	want = strings.ToLower(want)
	for i := len(t.chat) - 1; i >= 0; i-- {
		for _, row := range t.chat[i].Buttons {
			for _, b := range row {
				if b.Data != "" && strings.Contains(strings.ToLower(b.Text), want) {
					return t.chat[i].ID, b.Data, true
				}
			}
		}
	}
	return 0, "", false
}

// settle waits until the app has stopped calling for `quiet`, or `max` has passed. The
// assistant works after the webhook has been answered, so this is how a script knows the
// reply is complete.
func (t *telegramStandIn) settle(ctx context.Context, quiet, max time.Duration) {
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		t.mu.Lock()
		idle := time.Since(t.lastCall)
		typing := !t.typing.IsZero() && time.Since(t.typing) < 6*time.Second
		t.mu.Unlock()
		if idle >= quiet && !typing {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (t *telegramStandIn) snapshot() (msgs []chatMessage, version int64, typing bool, notice string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, m := range t.chat {
		msgs = append(msgs, *m)
	}
	return msgs, t.version, !t.typing.IsZero() && time.Since(t.typing) < 6*time.Second, t.notice
}

// botTextSince returns everything the bot has on screen from message index `from` on.
func (t *telegramStandIn) botTextSince(from int) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var b strings.Builder
	for i := from; i < len(t.chat); i++ {
		if t.chat[i].From == "bot" {
			b.WriteString(t.chat[i].Text + "\n")
		}
	}
	// A panel made before `from` may have been edited since; include the newest one.
	for i := len(t.chat) - 1; i >= 0 && i < from; i-- {
		if t.chat[i].From == "bot" && t.chat[i].Edited {
			b.WriteString(t.chat[i].Text + "\n")
			break
		}
	}
	return b.String()
}

func (t *telegramStandIn) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.chat)
}
