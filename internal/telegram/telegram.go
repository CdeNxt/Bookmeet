// Package telegram is a small client for the Telegram Bot API: the handful of calls the
// booking assistant needs, hand-rolled like the other integrations here.
//
// The bot token is part of every request URL (api.telegram.org/bot<token>/<method>), and Go's
// HTTP errors quote the URL. Every error this package returns is built without it - see call.
// Do not wrap a raw transport error from here into a log line or an API response.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const defaultBaseURL = "https://api.telegram.org"

// requestTimeout bounds every call. A webhook handler waits on these, and Telegram retries a
// webhook that does not answer, so an unbounded send would turn one slow reply into many.
const requestTimeout = 10 * time.Second

var tokenShape = regexp.MustCompile(`^\d{3,20}:[A-Za-z0-9_-]{20,100}$`)

// ValidToken reports whether s looks like a bot token (digits, a colon, then the secret).
// It is a typo check before the network round-trip, not proof the token works.
func ValidToken(s string) bool { return tokenShape.MatchString(s) }

type Client struct {
	token string
	base  string
	http  *http.Client
}

func New(token string) *Client {
	return &Client{token: token, base: defaultBaseURL, http: &http.Client{Timeout: requestTimeout}}
}

// WithBaseURL points the client at another server. For tests.
func (c *Client) WithBaseURL(base string) *Client {
	c.base = strings.TrimRight(base, "/")
	return c
}

// APIError is Telegram's own refusal: the request arrived and was turned down.
type APIError struct {
	Method      string
	Code        int
	Description string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram %s: %d %s", e.Method, e.Code, e.Description)
}

// Update is the part of an incoming webhook delivery the assistant reads.
type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message"`
}

type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"` // "private", "group", "supergroup", "channel"
}

type User struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

// DisplayName is the person's name as Telegram shows it, falling back to their handle.
func (u *User) DisplayName() string {
	if u == nil {
		return ""
	}
	if name := strings.TrimSpace(u.FirstName + " " + u.LastName); name != "" {
		return name
	}
	return u.Username
}

// GetMe returns the bot's username. It is how a pasted token is checked.
func (c *Client) GetMe(ctx context.Context) (string, error) {
	var out struct {
		Username string `json:"username"`
	}
	if err := c.call(ctx, "getMe", nil, &out); err != nil {
		return "", err
	}
	return out.Username, nil
}

// SetWebhook tells Telegram where to deliver this bot's messages. secret comes back on every
// delivery in the X-Telegram-Bot-Api-Secret-Token header, which is how the receiving end
// knows a delivery is Telegram's.
func (c *Client) SetWebhook(ctx context.Context, webhookURL, secret string) error {
	return c.call(ctx, "setWebhook", map[string]any{
		"url":             webhookURL,
		"secret_token":    secret,
		"allowed_updates": []string{"message", "callback_query"},
	}, nil)
}

// DeleteWebhook stops deliveries.
func (c *Client) DeleteWebhook(ctx context.Context) error {
	return c.call(ctx, "deleteWebhook", nil, nil)
}

// SendMessage sends plain text to a chat.
func (c *Client) SendMessage(ctx context.Context, chatID int64, text string) error {
	return c.call(ctx, "sendMessage", map[string]any{"chat_id": chatID, "text": text}, nil)
}

// call runs one Bot API method. Errors never contain the request URL, and so never the token.
func (c *Client) call(ctx context.Context, method string, params map[string]any, out any) error {
	body := []byte("{}")
	if params != nil {
		var err error
		if body, err = json.Marshal(params); err != nil {
			return fmt.Errorf("telegram %s: encode request: %w", method, err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/bot"+c.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram %s: could not build the request", method)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		// A *url.Error prints the URL it failed on. Keep the cause, drop the address.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("telegram %s: request failed: %s", method, strings.ReplaceAll(err.Error(), c.token, "[token]"))
	}
	defer resp.Body.Close()
	var envelope struct {
		OK          bool            `json:"ok"`
		ErrorCode   int             `json:"error_code"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&envelope); err != nil {
		return fmt.Errorf("telegram %s: unreadable response (HTTP %d)", method, resp.StatusCode)
	}
	if !envelope.OK {
		code := envelope.ErrorCode
		if code == 0 {
			code = resp.StatusCode
		}
		return &APIError{Method: method, Code: code, Description: envelope.Description}
	}
	if out != nil && len(envelope.Result) > 0 {
		if err := json.Unmarshal(envelope.Result, out); err != nil {
			return fmt.Errorf("telegram %s: unexpected result", method)
		}
	}
	return nil
}
