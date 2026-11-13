package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/calnode/calnode/internal/llm"
)

const testBotToken = "123456789:AAExampleTokenValue_abcdefghijklmnop"

// fakeTelegram stands in for api.telegram.org and records what the server asked of it.
type fakeTelegram struct {
	mu       sync.Mutex
	srv      *httptest.Server
	username string
	reject   bool // answer every call the way Telegram answers a bad key
	nextID   int64
	calls    []fakeTelegramCall
}

type fakeTelegramCall struct {
	method string
	body   map[string]any
}

func newFakeTelegram(t *testing.T) *fakeTelegram {
	t.Helper()
	ft := &fakeTelegram{username: "bookmeet_test_bot"}
	ft.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		ft.mu.Lock()
		ft.calls = append(ft.calls, fakeTelegramCall{method, body})
		reject, username := ft.reject, ft.username
		ft.mu.Unlock()
		switch {
		case reject:
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"ok":false,"error_code":401,"description":"Unauthorized"}`))
		case method == "getMe":
			fmt.Fprintf(w, `{"ok":true,"result":{"id":1,"username":%q}}`, username)
		case method == "sendMessage":
			ft.mu.Lock()
			ft.nextID++
			id := ft.nextID
			ft.mu.Unlock()
			fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d}}`, id)
		default:
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		}
	}))
	t.Cleanup(ft.srv.Close)
	return ft
}

func (ft *fakeTelegram) of(method string) []map[string]any {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	var out []map[string]any
	for _, c := range ft.calls {
		if c.method == method {
			out = append(out, c.body)
		}
	}
	return out
}

// sent returns the text of every message the bot sent to chatID.
func (ft *fakeTelegram) sent(chatID int64) []string {
	var out []string
	for _, body := range ft.of("sendMessage") {
		if body["chat_id"] == float64(chatID) {
			out = append(out, body["text"].(string))
		}
	}
	return out
}

type assistantFixture struct {
	*inviteFixture
	tg *fakeTelegram
}

func newAssistantFixture(t *testing.T) *assistantFixture {
	t.Helper()
	f := &assistantFixture{inviteFixture: newInviteFixture(t, false), tg: newFakeTelegram(t)}
	f.h.telegramAPIBase = f.tg.srv.URL
	f.h.assistantInline = true
	return f
}

func (f *assistantFixture) patch(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	return f.do(t, f.h.PatchAssistantSettings, http.MethodPatch, "/v1/settings/assistant", body)
}

func (f *assistantFixture) connectBot(t *testing.T) {
	t.Helper()
	if rec := f.patch(t, `{"telegram_bot_token":"`+testBotToken+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("connect bot: %d - %s", rec.Code, rec.Body.String())
	}
}

// switchOn connects the bot, makes the AI model live and flips the assistant on.
func (f *assistantFixture) switchOn(t *testing.T) {
	t.Helper()
	f.connectBot(t)
	f.h.SetLLM(llm.New(llm.Config{Endpoint: "http://127.0.0.1:1/v1", Model: "test"}))
	if rec := f.patch(t, `{"enabled":true}`); rec.Code != http.StatusOK {
		t.Fatalf("switch on: %d - %s", rec.Code, rec.Body.String())
	}
}

func (f *assistantFixture) settings(t *testing.T) map[string]any {
	t.Helper()
	rec := f.do(t, f.h.GetAssistantSettings, http.MethodGet, "/v1/settings/assistant", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get settings: %d - %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// linkCode asks for a connect link and returns the one-time code inside it.
func (f *assistantFixture) linkCode(t *testing.T) string {
	t.Helper()
	rec := f.do(t, f.h.CreateTelegramLinkCode, http.MethodPost, "/v1/settings/assistant/telegram/link", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("create link: %d - %s", rec.Code, rec.Body.String())
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	code, ok := strings.CutPrefix(out.URL, "https://t.me/bookmeet_test_bot?start=")
	if !ok || code == "" {
		t.Fatalf("connect link = %q; want a t.me link to the bot with a start code", out.URL)
	}
	return code
}

// deliver posts one message to the webhook the way Telegram would.
func (f *assistantFixture) deliver(t *testing.T, secret string, chatID int64, chatType, text string) *httptest.ResponseRecorder {
	t.Helper()
	body := fmt.Sprintf(`{"update_id":1,"message":{"message_id":1,"from":{"id":%d,"first_name":"Jason"},"chat":{"id":%d,"type":%q},"text":%q}}`,
		chatID, chatID, chatType, text)
	req := httptest.NewRequest(http.MethodPost, telegramWebhookPath, strings.NewReader(body))
	if secret != "" {
		req.Header.Set(telegramSecretHeader, secret)
	}
	rec := httptest.NewRecorder()
	f.h.TelegramWebhook(rec, req)
	return rec
}

// secret is what Telegram was told to send back, read from the registration call.
func (f *assistantFixture) secret(t *testing.T) string {
	t.Helper()
	hooks := f.tg.of("setWebhook")
	if len(hooks) == 0 {
		t.Fatal("no webhook was registered")
	}
	return hooks[len(hooks)-1]["secret_token"].(string)
}

func (f *assistantFixture) link(t *testing.T, chatID int64) {
	t.Helper()
	f.deliver(t, f.secret(t), chatID, "private", "/start "+f.linkCode(t))
	var n int
	if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM telegram_links WHERE chat_id = ?`, chatID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("chat %d was not linked (rows %d, err %v)", chatID, n, err)
	}
}

func TestAssistantSettings_connectingABotChecksItAndRegistersTheWebhook(t *testing.T) {
	f := newAssistantFixture(t)
	rec := f.patch(t, `{"telegram_bot_token":"`+testBotToken+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d - %s", rec.Code, rec.Body.String())
	}

	if len(f.tg.of("getMe")) != 1 {
		t.Error("the key was not checked with Telegram")
	}
	hooks := f.tg.of("setWebhook")
	if len(hooks) != 1 || hooks[0]["url"] != "https://book.team.example/v1/telegram/webhook" {
		t.Fatalf("webhook registration = %v; want this server's public address", hooks)
	}
	if s, _ := hooks[0]["secret_token"].(string); len(s) < 32 {
		t.Errorf("webhook secret %q; want a long random value", s)
	}

	// The key is write-only: not in any response, and not in the database in the clear.
	if strings.Contains(rec.Body.String(), "AAExample") {
		t.Error("the response contains the bot key")
	}
	var stored string
	if err := f.h.db.QueryRow(`SELECT telegram_bot_token_enc FROM server_settings WHERE id = 1`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == "" || strings.Contains(stored, "AAExample") {
		t.Errorf("stored key %q; want it present and encrypted", stored)
	}
	tg := f.settings(t)["telegram"].(map[string]any)
	if tg["configured"] != true || tg["bot_username"] != "bookmeet_test_bot" {
		t.Errorf("settings report %v; want the bot connected under its username", tg)
	}
}

func TestAssistantSettings_aKeyThatDoesNotWorkIsNotStored(t *testing.T) {
	f := newAssistantFixture(t)

	if rec := f.patch(t, `{"telegram_bot_token":"not a key"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed key: %d; want 400", rec.Code)
	}
	if n := len(f.tg.calls); n != 0 {
		t.Errorf("a malformed key reached Telegram (%d calls)", n)
	}

	f.tg.reject = true
	rec := f.patch(t, `{"telegram_bot_token":"`+testBotToken+`"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("key Telegram refuses: %d; want 400", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "AAExample") {
		t.Error("the error message contains the bot key")
	}
	if tg := f.settings(t)["telegram"].(map[string]any); tg["configured"] != false {
		t.Error("a refused key was stored")
	}
}

// The staging copy's public address is behind a login Telegram cannot pass, so the delivery
// address can be named separately.
func TestAssistantSettings_theWebhookAddressCanBeOverridden(t *testing.T) {
	f := newAssistantFixture(t)
	f.h.SetTelegramWebhookBase("https://bookmeet-staging.fly.dev/")
	f.connectBot(t)
	if got := f.tg.of("setWebhook")[0]["url"]; got != "https://bookmeet-staging.fly.dev/v1/telegram/webhook" {
		t.Errorf("webhook registered at %v", got)
	}
}

func TestAssistantSettings_aServerTelegramCannotReachSaysSo(t *testing.T) {
	f := newAssistantFixture(t)
	f.h.SetTelegramWebhookBase("http://localhost:3000")
	rec := f.patch(t, `{"telegram_bot_token":"`+testBotToken+`"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "https") {
		t.Fatalf("status %d - %s; want a 400 that names the https requirement", rec.Code, rec.Body.String())
	}
	if n := len(f.tg.calls); n != 0 {
		t.Errorf("Telegram was called %d times for an address it could never reach", n)
	}
}

func TestAssistantSettings_switchingOnNeedsABotAndTheModel(t *testing.T) {
	f := newAssistantFixture(t)

	if rec := f.patch(t, `{"enabled":true}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("no bot: %d; want 400", rec.Code)
	}
	f.connectBot(t)
	if rec := f.patch(t, `{"enabled":true}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bot but no AI model: %d; want 400", rec.Code)
	}
	f.h.SetLLM(llm.New(llm.Config{Endpoint: "http://127.0.0.1:1/v1", Model: "test"}))
	if rec := f.patch(t, `{"enabled":true}`); rec.Code != http.StatusOK {
		t.Fatalf("bot and model: %d - %s", rec.Code, rec.Body.String())
	}
	if s := f.settings(t); s["enabled"] != true || s["active"] != true {
		t.Errorf("settings %v; want enabled and active", s)
	}

	// The model going away afterwards makes the assistant inactive without unsetting the
	// switch, and the switch can still be saved as it stands.
	f.h.SetLLM(nil)
	if s := f.settings(t); s["enabled"] != true || s["active"] != false {
		t.Errorf("with the model off: %v; want enabled but not active", s)
	}
	if rec := f.patch(t, `{"enabled":true}`); rec.Code != http.StatusOK {
		t.Errorf("re-saving an already-on switch with the model off: %d; want it accepted", rec.Code)
	}
}

func TestTelegramWebhook_aOneTimeCodeLinksTheChatOnce(t *testing.T) {
	f := newAssistantFixture(t)
	f.connectBot(t)
	code := f.linkCode(t)

	if rec := f.deliver(t, f.secret(t), 111, "private", "/start "+code); rec.Code != http.StatusOK {
		t.Fatalf("delivery: %d", rec.Code)
	}
	if got := f.tg.sent(111); len(got) != 1 || !strings.Contains(got[0], "Connected to Jeroen Host") {
		t.Fatalf("replies to the linking chat = %v; want one confirmation naming the account", got)
	}
	link := f.settings(t)["link"].(map[string]any)
	if link["linked"] != true || link["telegram_name"] != "Jason" {
		t.Errorf("link state %v; want linked as Jason", link)
	}

	// The same code from someone else's chat: spent, so it links nobody.
	f.deliver(t, f.secret(t), 222, "private", "/start "+code)
	if got := f.tg.sent(222); len(got) != 1 || !strings.Contains(got[0], "expired") {
		t.Errorf("replies to the second chat = %v; want one message saying the link expired", got)
	}
	var n int
	if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM telegram_links WHERE chat_id = 222`).Scan(&n); err != nil || n != 0 {
		t.Errorf("the second chat was linked (rows %d, err %v)", n, err)
	}
}

func TestTelegramWebhook_refusesDeliveriesWithoutTheSecret(t *testing.T) {
	f := newAssistantFixture(t)

	// No bot connected: the route behaves as if it were not there.
	if rec := f.deliver(t, "anything", 111, "private", "hello"); rec.Code != http.StatusNotFound {
		t.Fatalf("no bot connected: %d; want 404", rec.Code)
	}

	f.switchOn(t)
	f.link(t, 111)
	before := len(f.tg.sent(111))
	for name, secret := range map[string]string{"no secret": "", "wrong secret": "guess"} {
		if rec := f.deliver(t, secret, 111, "private", "hello"); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d; want 404", name, rec.Code)
		}
	}
	if got := len(f.tg.sent(111)); got != before {
		t.Errorf("a forged delivery got %d replies", got-before)
	}
}

func TestTelegramWebhook_answersOnlyTheLinkedChatAndOnlyWhenOn(t *testing.T) {
	f := newAssistantFixture(t)
	f.connectBot(t)
	f.link(t, 111)
	secret := f.secret(t)
	linked := len(f.tg.sent(111)) // the confirmation from linking

	// Linked, but the assistant is switched off: quiet.
	f.deliver(t, secret, 111, "private", "book Bill for Tuesday")
	if got := len(f.tg.sent(111)); got != linked {
		t.Fatalf("the bot answered while switched off (%d new messages)", got-linked)
	}

	f.h.SetLLM(llm.New(llm.Config{Endpoint: "http://127.0.0.1:1/v1", Model: "test"}))
	if rec := f.patch(t, `{"enabled":true}`); rec.Code != http.StatusOK {
		t.Fatalf("switch on: %d", rec.Code)
	}
	f.deliver(t, secret, 111, "private", "book Bill for Tuesday")
	if got := len(f.tg.sent(111)); got != linked+1 {
		t.Fatalf("linked chat with the assistant on got %d replies; want 1", got-linked)
	}

	// A stranger who found the bot, and the same bot added to a group: no answer at all.
	f.deliver(t, secret, 999, "private", "hello")
	f.deliver(t, secret, 999, "private", "/start")
	f.deliver(t, secret, 111, "group", "book Bill for Tuesday")
	if got := f.tg.sent(999); len(got) != 0 {
		t.Errorf("a stranger was answered: %v", got)
	}
	if got := len(f.tg.sent(111)); got != linked+1 {
		t.Errorf("a group message was answered")
	}
}

func TestAssistantSettings_unlinkingStopsTheBotAnswering(t *testing.T) {
	f := newAssistantFixture(t)
	f.switchOn(t)
	f.link(t, 111)
	before := len(f.tg.sent(111))

	rec := f.do(t, f.h.DeleteTelegramLink, http.MethodDelete, "/v1/settings/assistant/telegram/link", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("unlink: %d - %s", rec.Code, rec.Body.String())
	}
	f.deliver(t, f.secret(t), 111, "private", "hello")
	if got := len(f.tg.sent(111)); got != before {
		t.Errorf("the bot answered an unlinked chat")
	}
}

func TestAssistantSettings_disconnectingTheBotForgetsEverything(t *testing.T) {
	f := newAssistantFixture(t)
	f.switchOn(t)
	f.link(t, 111)
	secret := f.secret(t)

	if rec := f.patch(t, `{"telegram_bot_token":""}`); rec.Code != http.StatusOK {
		t.Fatalf("disconnect: %d - %s", rec.Code, rec.Body.String())
	}
	if len(f.tg.of("deleteWebhook")) != 1 {
		t.Error("Telegram was not told to stop delivering")
	}
	s := f.settings(t)
	if s["enabled"] != false || s["telegram"].(map[string]any)["configured"] != false || s["link"].(map[string]any)["linked"] != false {
		t.Errorf("after disconnecting: %v; want off, no bot, no link", s)
	}
	// A delivery that still arrives with the old secret is refused.
	if rec := f.deliver(t, secret, 111, "private", "hello"); rec.Code != http.StatusNotFound {
		t.Errorf("delivery after disconnect: %d; want 404", rec.Code)
	}
}

// Saving the same bot's key again must not unlink anyone; a different bot is a different
// conversation nobody has opened yet, so its links start empty.
func TestAssistantSettings_linksSurviveTheSameBotAndNotADifferentOne(t *testing.T) {
	f := newAssistantFixture(t)
	f.connectBot(t)
	f.link(t, 111)

	f.connectBot(t)
	if link := f.settings(t)["link"].(map[string]any); link["linked"] != true {
		t.Fatal("re-saving the same bot unlinked the chat")
	}

	f.tg.mu.Lock()
	f.tg.username = "another_bot"
	f.tg.mu.Unlock()
	f.connectBot(t)
	if link := f.settings(t)["link"].(map[string]any); link["linked"] != false {
		t.Error("a chat linked through the old bot is still linked to the new one")
	}
}
