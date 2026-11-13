package telegram_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/calnode/calnode/internal/telegram"
)

const token = "123456789:AAExampleTokenValue_abcdefghijklmnop"

func TestValidToken(t *testing.T) {
	for s, want := range map[string]bool{
		token:                            true,
		"":                               false,
		"not a token":                    false,
		"123456789":                      false,
		"123456789:short":                false,
		"abc:AAExampleTokenValue_abcdef": false,
		token + " ":                      false,
	} {
		if got := telegram.ValidToken(s); got != want {
			t.Errorf("ValidToken(%q) = %v; want %v", s, got, want)
		}
	}
}

func TestClient_callsTheBotAPI(t *testing.T) {
	type call struct {
		path string
		body map[string]any
	}
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		calls = append(calls, call{r.URL.Path, body})
		if strings.HasSuffix(r.URL.Path, "/getMe") {
			_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1,"username":"bookmeet_test_bot"}}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":7}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer srv.Close()
	c := telegram.New(token).WithBaseURL(srv.URL)
	ctx := context.Background()

	name, err := c.GetMe(ctx)
	if err != nil || name != "bookmeet_test_bot" {
		t.Fatalf("GetMe = %q, %v", name, err)
	}
	if err := c.SetWebhook(ctx, "https://book.example/v1/telegram/webhook", "s3cret"); err != nil {
		t.Fatalf("SetWebhook: %v", err)
	}
	if err := c.SendMessage(ctx, 42, "Connected."); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	if len(calls) != 3 {
		t.Fatalf("%d calls; want 3", len(calls))
	}
	if want := "/bot" + token + "/setWebhook"; calls[1].path != want {
		t.Errorf("setWebhook path = %q; want %q", calls[1].path, want)
	}
	if calls[1].body["url"] != "https://book.example/v1/telegram/webhook" || calls[1].body["secret_token"] != "s3cret" {
		t.Errorf("setWebhook body = %v", calls[1].body)
	}
	if calls[2].body["chat_id"] != float64(42) || calls[2].body["text"] != "Connected." {
		t.Errorf("sendMessage body = %v", calls[2].body)
	}
}

func TestClient_reportsTelegramsRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":401,"description":"Unauthorized"}`))
	}))
	defer srv.Close()

	_, err := telegram.New(token).WithBaseURL(srv.URL).GetMe(context.Background())
	var apiErr *telegram.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != 401 {
		t.Fatalf("err = %v; want an APIError with code 401", err)
	}
}

// The token is in the request URL, and Go's transport errors quote the URL. An error that
// reached a log or an API response would carry the bot's key with it.
func TestClient_errorsNeverCarryTheToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close() // nothing is listening: every call fails in the transport

	c := telegram.New(token).WithBaseURL(base)
	for name, err := range map[string]error{
		"SendMessage": c.SendMessage(context.Background(), 1, "hello"),
		"SetWebhook":  c.SetWebhook(context.Background(), "https://book.example/hook", "s"),
	} {
		if err == nil {
			t.Fatalf("%s to a closed server succeeded", name)
		}
		if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "AAExample") {
			t.Errorf("%s error contains the bot token: %v", name, err)
		}
	}
}
