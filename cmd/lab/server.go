package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

//go:embed ui.html
var uiHTML []byte

//go:embed example-scenario.json
var exampleScenario []byte

func (l *lab) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/tg/", l.tg.serveAPI)
	mux.HandleFunc("/llm/", l.rec.serve)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(uiHTML)
	})
	mux.HandleFunc("GET /lab/api/state", l.guard(l.handleState))
	mux.HandleFunc("GET /lab/api/calls", l.guard(l.handleCalls))
	mux.HandleFunc("POST /lab/api/say", l.guard(l.handleSay))
	mux.HandleFunc("POST /lab/api/tap", l.guard(l.handleTap))
	mux.HandleFunc("POST /lab/api/model", l.guard(l.handleModel))
	mux.HandleFunc("POST /lab/api/reset", l.guard(l.handleReset))
	mux.HandleFunc("POST /lab/api/link", l.guard(l.handleLink))
	mux.HandleFunc("POST /lab/api/run", l.guard(l.handleRun))

	// A first scenario, so the folder is not empty and the format is in front of whoever
	// opens it.
	if existing, _ := filepath.Glob(filepath.Join(l.dir, "scenarios", "*.json")); len(existing) == 0 {
		_ = os.WriteFile(filepath.Join(l.dir, "scenarios", "example.json"), exampleScenario, 0o600)
	}
	return mux
}

// guard keeps the lab's controls to this machine. The lab holds a model key and can point
// the app's model traffic anywhere, so a web page open in the same browser must not be able
// to drive it: requests have to be addressed to the lab by its local name (which stops a
// page that re-points its own name at 127.0.0.1) and, when a browser says where the request
// came from, that has to be the lab's own page.
func (l *lab) guard(next http.HandlerFunc) http.HandlerFunc {
	local := func(host string) bool {
		h := strings.ToLower(host)
		return h == "localhost:3066" || h == "127.0.0.1:3066"
	}
	return func(w http.ResponseWriter, r *http.Request) {
		ok := local(r.Host)
		if o := r.Header.Get("Origin"); ok && o != "" {
			u, err := url.Parse(o)
			ok = err == nil && local(u.Host)
		}
		if !ok {
			http.Error(w, "the lab only answers its own page", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func (l *lab) handleState(w http.ResponseWriter, r *http.Request) {
	msgs, version, typing, notice := l.tg.snapshot()
	l.mu.Lock()
	state := map[string]any{
		"status": l.status, "problems": l.problems, "ready": l.ready,
		"model":   map[string]any{"endpoint": l.model.Endpoint, "model": l.model.Model, "key_set": l.model.APIKey != ""},
		"sign_in": map[string]string{"url": appURL + "/admin", "email": ownerEmail, "password": l.password},
		"chat":    msgs, "version": version, "typing": typing, "notice": notice,
		"calls": l.rec.mark(),
	}
	links, userID, ready := l.links, l.userID, l.ready
	l.mu.Unlock()
	// The links that exist right now, with their addresses, so a guest page is one click away.
	var out []map[string]string
	if ready && links != nil {
		if list, err := links.List(r.Context(), userID); err == nil {
			for _, lk := range list {
				item := map[string]string{"guest": lk.GuestName, "status": lk.Status, "topic": lk.Topic}
				if token, err := links.Token(r.Context(), userID, lk.ID); err == nil {
					item["url"] = appURL + "/i/" + token
				}
				out = append(out, item)
			}
		}
	}
	state["links"] = out
	names := []string{}
	if all, err := loadScenarios(filepath.Join(l.dir, "scenarios")); err == nil {
		for _, s := range all {
			names = append(names, s.Side+": "+s.Name)
		}
	} else {
		state["scenario_error"] = err.Error()
	}
	state["scenarios"] = names
	writeJSON(w, state)
}

func (l *lab) handleCalls(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	writeJSON(w, map[string]any{"calls": l.rec.since(after)})
}

func (l *lab) handleSay(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text string `json:"text"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || strings.TrimSpace(req.Text) == "" {
		http.Error(w, "nothing to say", http.StatusBadRequest)
		return
	}
	if err := l.tg.deliverMessage(r.Context(), req.Text); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (l *lab) handleTap(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MessageID int64  `json:"message_id"`
		Data      string `json:"data"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.Data == "" {
		http.Error(w, "nothing to tap", http.StatusBadRequest)
		return
	}
	if err := l.tg.deliverTap(r.Context(), req.MessageID, req.Data); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (l *lab) handleModel(w http.ResponseWriter, r *http.Request) {
	var m modelConfig
	if json.NewDecoder(r.Body).Decode(&m) != nil {
		http.Error(w, "unreadable", http.StatusBadRequest)
		return
	}
	u, err := url.Parse(strings.TrimSpace(m.Endpoint))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		http.Error(w, "The address must start with http:// or https://", http.StatusBadRequest)
		return
	}
	// A saved key was given for one address. It is not sent to a different one unless it
	// is typed again.
	l.mu.Lock()
	old := l.model
	l.mu.Unlock()
	if m.APIKey == "" && old.APIKey != "" {
		if ou, err := url.Parse(old.Endpoint); err != nil || ou.Host != u.Host {
			http.Error(w, "The address changed, so enter the key again.", http.StatusBadRequest)
			return
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := l.setModel(ctx, m); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (l *lab) handleReset(w http.ResponseWriter, _ *http.Request) {
	if err := l.reset(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// handleLink makes a personal link directly, for trying the guest's side without first
// asking the owner's assistant for one.
func (l *lab) handleLink(w http.ResponseWriter, r *http.Request) {
	var ls linkSetup
	if json.NewDecoder(r.Body).Decode(&ls) != nil {
		http.Error(w, "unreadable", http.StatusBadRequest)
		return
	}
	l.busy.Lock()
	defer l.busy.Unlock()
	_, token, err := l.makeLink(r.Context(), ls)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]string{"url": appURL + "/i/" + token})
}

func (l *lab) handleRun(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	if n < 1 {
		n = 1
	}
	if n > 20 {
		n = 20
	}
	if !l.busy.TryLock() {
		http.Error(w, "The lab is busy (another run, or a reset). Try again shortly.\nFAILED\n", http.StatusConflict)
		return
	}
	defer l.busy.Unlock()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	// A run outlives a closed browser tab on purpose: half a run leaves the world mid-way.
	l.runAll(context.WithoutCancel(r.Context()), w, r.URL.Query()["only"], n)
}
