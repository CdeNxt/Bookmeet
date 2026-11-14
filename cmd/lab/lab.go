package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/calnode/calnode/internal/db"
	"github.com/calnode/calnode/internal/keyvault"
	"github.com/calnode/calnode/internal/personallink"
)

// The seeded owner. Everything here is throwaway and local.
const (
	ownerName  = "Jason Lab"
	ownerEmail = "owner@lab.test"
	ownerZone  = "America/Los_Angeles"
	ownerChat  = int64(1001)
	botToken   = "100000001:LABxStandInxNotAxRealxTelegramxKeyx00"
)

// canaries are words that belong to OTHER people in the seeded world: guests of other
// bookings and links, and what those meetings are about. A guest's chat has no business
// knowing any of them, so if one ever shows up on a guest's screen, or in what the model
// is told on a guest's behalf, something leaked.
var canaries = []string{"Zephyrine", "Quartermaine", "Ottoline", "Brackenridge", "Thaddeus", "Wolfsbane", "Nightjar", "canary.test"}

type modelConfig struct {
	Endpoint string `json:"endpoint"` // the real model address, e.g. https://api.openai.com/v1
	Model    string `json:"model"`
	APIKey   string `json:"api_key"`
}

type lab struct {
	root, dir string
	secret    string // the app's platform secret for this lab session
	password  string

	mu       sync.Mutex // guards the fields below
	app      *exec.Cmd
	appLog   *os.File
	db       *sql.DB
	links    *personallink.Service
	userID   string
	types    map[string]string // slug -> event type id
	model    modelConfig
	status   string
	problems []string
	ready    bool

	busy sync.Mutex // one world-changing operation at a time (reset, run, model change)

	http *http.Client // signed in as the owner
	tg   *telegramStandIn
	rec  *recorder
}

func newLab(root, dir string) *lab {
	jar, _ := cookiejar.New(nil)
	l := &lab{root: root, dir: dir, http: &http.Client{Jar: jar, Timeout: 60 * time.Second}, types: map[string]string{}}
	l.tg = newTelegramStandIn()
	l.rec = newRecorder(func() string { l.mu.Lock(); defer l.mu.Unlock(); return l.model.Endpoint })
	l.secret = randomHex(32)
	l.password = "lab-" + randomHex(8)
	if raw, err := os.ReadFile(filepath.Join(dir, "config.json")); err == nil {
		_ = json.Unmarshal(raw, &l.model)
	}
	_ = os.WriteFile(filepath.Join(dir, "sign-in.txt"), []byte("Admin app: "+appURL+"/admin\nEmail: "+ownerEmail+"\nPassword: "+l.password+"\n(new each time the lab starts)\n"), 0o600)
	return l
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (l *lab) setStatus(s string) {
	l.mu.Lock()
	l.status = s
	l.mu.Unlock()
	log.Print(s)
}

func (l *lab) problem(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	l.mu.Lock()
	l.problems = append(l.problems, msg)
	l.mu.Unlock()
	log.Print("problem: " + msg)
}

func (l *lab) dbPath() string       { return filepath.Join(l.dir, "lab.db") }
func (l *lab) snapshotPath() string { return filepath.Join(l.dir, "snapshot.db") }

// ---- the app process ----

func (l *lab) boot(ctx context.Context) error {
	l.busy.Lock()
	defer l.busy.Unlock()
	l.setStatus("Building the app from this checkout…")
	build := exec.CommandContext(ctx, "go", "build", "-o", filepath.Join(l.dir, "bookmeet"), "./cmd/calnode")
	build.Dir = l.root
	if out, err := build.CombinedOutput(); err != nil {
		return fmt.Errorf("build failed:\n%s", out)
	}
	for _, f := range []string{l.dbPath(), l.dbPath() + "-wal", l.dbPath() + "-shm", l.snapshotPath()} {
		_ = os.Remove(f)
	}
	if err := l.startApp(); err != nil {
		return err
	}
	l.setStatus("Setting up the test world…")
	if err := l.seed(ctx); err != nil {
		return fmt.Errorf("set up the world: %w", err)
	}
	l.applyModel(ctx)
	return l.snapshot()
}

func (l *lab) startApp() error {
	logFile, err := os.OpenFile(filepath.Join(l.dir, "app.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	cmd := exec.Command(filepath.Join(l.dir, "bookmeet"))
	cmd.Dir = l.dir
	cmd.Env = append(os.Environ(),
		"PORT="+appPort,
		"BASE_URL="+appURL,
		"DATABASE_URL=sqlite://"+l.dbPath(),
		"DATA_DIR="+filepath.Join(l.dir, "data"),
		"CALNODE_ENCRYPTION_KEY="+l.secret,
		// The app's bot calls come to the lab instead of Telegram.
		"TELEGRAM_API_BASE_URL="+labURL+"/tg",
		// Lets the scenario runner give each conversation its own address, so the per-address
		// request limits apply per conversation as they would to separate guests.
		"TRUSTED_PROXY_CIDRS=127.0.0.1/32,::1/128",
		"LOG_LEVEL=info",
	)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start the app: %w", err)
	}
	l.mu.Lock()
	l.app, l.appLog = cmd, logFile
	l.mu.Unlock()

	deadline := time.Now().Add(40 * time.Second)
	for {
		resp, err := http.Get(appURL + "/version")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the app did not come up; see %s", filepath.Join(l.dir, "app.log"))
		}
		time.Sleep(150 * time.Millisecond)
	}
	// The lab's own view of the database: to check outcomes, and to make a link directly
	// (the app has no API for that; in real use only the assistant makes them).
	conn, err := db.Open("sqlite://" + l.dbPath())
	if err != nil {
		return err
	}
	vault, err := keyvault.Open(conn, l.secret, "", true)
	if err != nil {
		return fmt.Errorf("open the key store: %w", err)
	}
	l.mu.Lock()
	l.db, l.links, l.ready = conn, personallink.New(conn, vault.DEK()), true
	l.mu.Unlock()
	return nil
}

func (l *lab) stopApp() {
	l.mu.Lock()
	cmd, logFile, conn := l.app, l.appLog, l.db
	l.app, l.appLog, l.db, l.ready = nil, nil, nil, false
	l.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}
	if logFile != nil {
		_ = logFile.Close()
	}
}

// snapshot saves the world as it stands, so it can be put back in about a second.
func (l *lab) snapshot() error {
	_ = os.Remove(l.snapshotPath())
	if _, err := l.db.Exec(`VACUUM INTO ?`, l.snapshotPath()); err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	l.setStatus("Ready.")
	return nil
}

// restore puts the world back to the last snapshot: the app is stopped, its database
// replaced, and the app started again. Callers hold l.busy.
func (l *lab) restore() error {
	l.stopApp()
	for _, f := range []string{l.dbPath() + "-wal", l.dbPath() + "-shm"} {
		_ = os.Remove(f)
	}
	src, err := os.ReadFile(l.snapshotPath())
	if err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	if err := os.WriteFile(l.dbPath(), src, 0o600); err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	l.tg.clearChat()
	if err := l.startApp(); err != nil {
		return err
	}
	// The owner's session lives in the database, so the restored one is still good.
	return nil
}

// ---- the app's API, as the owner ----

func (l *lab) api(ctx context.Context, method, path string, body, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, appURL+path, rd)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := l.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, fmt.Errorf("%s %s: unreadable reply", method, path)
		}
	}
	return resp.StatusCode, nil
}

// businessDay returns midnight, in the owner's zone, of the nth weekday after today.
func businessDay(n int) time.Time {
	loc, _ := time.LoadLocation(ownerZone)
	now := time.Now().In(loc)
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	for n > 0 {
		day = day.AddDate(0, 0, 1)
		if wd := day.Weekday(); wd != time.Saturday && wd != time.Sunday {
			n--
		}
	}
	return day
}

func clockOn(day time.Time, hhmm string) (time.Time, error) {
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return time.Time{}, fmt.Errorf("bad time %q (want HH:MM)", hhmm)
	}
	return time.Date(day.Year(), day.Month(), day.Day(), t.Hour(), t.Minute(), 0, 0, day.Location()), nil
}

// seed builds the world through the app's own API wherever one exists, so what is set up is
// what a real owner would have: an account, weekday hours, three meeting types, the bot
// connected and their chat linked, and a few meetings and links that belong to other people.
func (l *lab) seed(ctx context.Context) error {
	if _, err := l.api(ctx, "POST", "/v1/auth/claim", map[string]string{
		"name": ownerName, "email": ownerEmail, "password": l.password, "timezone": ownerZone,
	}, nil); err != nil {
		return err
	}
	var me struct {
		ID string `json:"id"`
	}
	if _, err := l.api(ctx, "GET", "/v1/users/me", nil, &me); err != nil {
		return err
	}
	l.userID = me.ID

	var rules struct {
		Rules []json.RawMessage `json:"rules"`
	}
	_, _ = l.api(ctx, "GET", "/v1/availability-rules", nil, &rules)
	var n int
	_ = l.db.QueryRow(`SELECT COUNT(*) FROM availability_rules WHERE user_id = ?`, l.userID).Scan(&n)
	if n == 0 {
		for day := 1; day <= 5; day++ {
			if _, err := l.api(ctx, "POST", "/v1/availability-rules", map[string]any{
				"day_of_week": day, "start_time": "09:00", "end_time": "17:00",
			}, nil); err != nil {
				return err
			}
		}
	}

	for _, et := range []map[string]any{
		{"slug": "intro", "name": "Intro call", "duration_minutes": 30, "location_type": "phone", "location_value": "+1 415 555 0100", "max_future_days": 30},
		{"slug": "strategy", "name": "Strategy session", "duration_minutes": 60, "location_type": "phone", "location_value": "+1 415 555 0100", "max_future_days": 30},
		{"slug": "coffee", "name": "Coffee", "duration_minutes": 45, "location_type": "phone", "location_value": "+1 415 555 0100", "allow_in_person": true, "max_future_days": 30},
	} {
		if _, err := l.api(ctx, "POST", "/v1/event-types", et, nil); err != nil {
			return err
		}
	}
	rows, err := l.db.Query(`SELECT slug, id FROM event_types WHERE user_id = ?`, l.userID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var slug, id string
		if rows.Scan(&slug, &id) == nil {
			l.types[slug] = id
		}
	}
	rows.Close()

	// The bot: the app checks the key and registers its webhook with the stand-in, which is
	// how the lab learns where to deliver and with what secret.
	if _, err := l.api(ctx, "PATCH", "/v1/settings/assistant", map[string]any{"telegram_bot_token": botToken}, nil); err != nil {
		return err
	}
	var code struct {
		URL string `json:"url"`
	}
	if _, err := l.api(ctx, "POST", "/v1/settings/assistant/telegram/link", nil, &code); err != nil {
		return err
	}
	if i := strings.Index(code.URL, "?start="); i >= 0 {
		if err := l.tg.deliverMessage(ctx, "/start "+code.URL[i+len("?start="):]); err != nil {
			return fmt.Errorf("link the owner's chat: %w", err)
		}
		l.tg.settle(ctx, 2*time.Second, 15*time.Second)
	}

	// Other people's meetings, through the public booking endpoint a real booker uses.
	for _, b := range []struct {
		day         int
		at          string
		name, email string
		slug        string
	}{
		{2, "10:00", "Zephyrine Quartermaine", "zephyrine@canary.test", "intro"},
		{3, "14:00", "Ottoline Brackenridge", "ottoline@canary.test", "strategy"},
	} {
		start, _ := clockOn(businessDay(b.day), b.at)
		if _, err := l.api(ctx, "POST", "/v1/bookings", map[string]any{
			"event_type_slug": b.slug, "start_at": start.UTC().Format(time.RFC3339),
			"name": b.name, "email": b.email, "timezone": "Europe/London",
		}, nil); err != nil {
			l.problem("could not seed %s's booking: %v", b.name, err)
		}
	}
	// And someone else's personal link, still open.
	a, _ := clockOn(businessDay(4), "09:00")
	z, _ := clockOn(businessDay(4), "12:00")
	if err := retryBusy(func() error {
		_, _, err := l.links.Create(ctx, personallink.CreateParams{
			UserID: l.userID, EventTypeID: l.types["strategy"], GuestName: "Thaddeus Wolfsbane",
			GuestEmail: "thaddeus@canary.test", Topic: "Project Nightjar acquisition", DetailsDone: true,
			Windows: []personallink.Window{{Start: a, End: z}},
		})
		return err
	}); err != nil {
		l.problem("could not seed the other guest's link: %v", err)
	}
	// The assistant's switch, set directly. The app only lets it be switched on while a model
	// is live, but a switch that is on with no model behind it is a state real copies reach
	// (the model is removed later), and it is the one that lets the guest page's buttons be
	// tried here before any model details are entered.
	if err := retryBusy(func() error {
		_, err := l.db.Exec(`UPDATE server_settings SET assistant_enabled = 1 WHERE id = 1`)
		return err
	}); err != nil {
		l.problem("could not switch the assistant on: %v", err)
	}
	l.tg.clearChat()
	return nil
}

// retryBusy runs a write again when the database was busy. The app is the database's main
// user and writes in the background (jobs, reminders), so the lab's occasional write can
// meet one in progress.
func retryBusy(fn func() error) error {
	var err error
	for i := 0; i < 25; i++ {
		if err = fn(); err == nil || !strings.Contains(err.Error(), "locked") {
			return err
		}
		time.Sleep(200 * time.Millisecond)
	}
	return err
}

// applyModel points the app at the model through the lab's recorder and switches the
// assistant on. With no model details yet, the assistant stays off and the lab page says so.
func (l *lab) applyModel(ctx context.Context) {
	l.mu.Lock()
	m := l.model
	// Problems from an earlier attempt at the model are stale; the world's own stay.
	kept := l.problems[:0]
	for _, p := range l.problems {
		if !strings.HasPrefix(p, "model: ") {
			kept = append(kept, p)
		}
	}
	l.problems = kept
	l.mu.Unlock()
	if m.Endpoint == "" || m.Model == "" {
		return
	}
	if _, err := l.api(ctx, "PATCH", "/v1/settings/llm", map[string]any{
		"enabled": true, "endpoint": labURL + "/llm", "model": m.Model, "api_key": m.APIKey,
	}, nil); err != nil {
		l.problem("model: the app refused the settings: %v", err)
		return
	}
	if _, err := l.api(ctx, "PATCH", "/v1/settings/assistant", map[string]any{"enabled": true}, nil); err != nil {
		l.problem("model: the assistant could not be switched on: %v", err)
	}
}

// setModel saves new model details and rebuilds the snapshot around them.
func (l *lab) setModel(ctx context.Context, m modelConfig) error {
	l.busy.Lock()
	defer l.busy.Unlock()
	m.Endpoint = strings.TrimRight(strings.TrimSpace(m.Endpoint), "/")
	m.Model = strings.TrimSpace(m.Model)
	l.mu.Lock()
	if m.APIKey == "" {
		m.APIKey = l.model.APIKey // left blank on the form: keep the one already saved
	}
	l.model = m
	l.mu.Unlock()
	raw, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(filepath.Join(l.dir, "config.json"), raw, 0o600); err != nil {
		return err
	}
	l.applyModel(ctx)
	return l.snapshot()
}

func (l *lab) reset() error {
	l.busy.Lock()
	defer l.busy.Unlock()
	l.setStatus("Putting the world back…")
	if err := l.restore(); err != nil {
		return err
	}
	l.setStatus("Ready.")
	return nil
}
