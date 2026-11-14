package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// recorder sits between the app and the AI model. The app is given the lab's address as its
// model endpoint; every request is passed on to the real one unchanged and both sides are
// kept, so "what was the model told, and what did it say" has an exact answer.
//
// Only bodies are kept. The Authorization header carrying the model key is passed through
// and never stored.
type recorder struct {
	upstream func() string
	mu       sync.Mutex
	calls    []modelCall
	seq      int
	http     *http.Client
}

type modelCall struct {
	Seq      int             `json:"seq"`
	At       time.Time       `json:"at"`
	Side     string          `json:"side"` // "guest", "owner" or "other": whose assistant asked
	Millis   int64           `json:"ms"`
	Status   int             `json:"status"`
	Request  json.RawMessage `json:"request"`
	Response json.RawMessage `json:"response"`
}

func newRecorder(upstream func() string) *recorder {
	return &recorder{upstream: upstream, http: &http.Client{Timeout: 5 * time.Minute}}
}

// sideOf tells whose assistant a request is from, by how its instructions open.
func sideOf(body []byte) string {
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &req) != nil || len(req.Messages) == 0 {
		return "other"
	}
	sys, _ := req.Messages[0].Content.(string)
	switch {
	case strings.Contains(sys, "a guest who opened a personal booking link"):
		return "guest"
	case strings.Contains(sys, "show_details"):
		return "owner"
	}
	return "other"
}

// serve handles /llm/*: the model endpoint the app is pointed at.
func (rec *recorder) serve(w http.ResponseWriter, r *http.Request) {
	up := rec.upstream()
	if up == "" {
		http.Error(w, `{"error":"no model set in the lab"}`, http.StatusServiceUnavailable)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	req, err := http.NewRequestWithContext(r.Context(), r.Method, up+strings.TrimPrefix(r.URL.Path, "/llm"), bytes.NewReader(body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	for _, h := range []string{"Authorization", "Content-Type", "Accept"} {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	started := time.Now()
	resp, err := rec.http.Do(req)
	if err != nil {
		rec.keep(body, []byte(`{"lab":"the model could not be reached"}`), 0, started)
		http.Error(w, `{"error":"the model could not be reached"}`, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for _, h := range []string{"Content-Type"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	// Copy as it arrives, so a streamed reply still streams, and keep a copy.
	var kept bytes.Buffer
	buf := make([]byte, 16<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			kept.Write(buf[:n])
			_, _ = w.Write(buf[:n])
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		if rerr != nil {
			break
		}
	}
	rec.keep(body, kept.Bytes(), resp.StatusCode, started)
}

func asJSON(b []byte) json.RawMessage {
	if json.Valid(b) {
		return b
	}
	quoted, _ := json.Marshal(string(b)) // a streamed reply, or an error page: keep it as text
	return quoted
}

func (rec *recorder) keep(request, response []byte, status int, started time.Time) {
	if len(request) == 0 { // a connection check, nothing worth reading
		return
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.seq++
	rec.calls = append(rec.calls, modelCall{
		Seq: rec.seq, At: started, Side: sideOf(request), Millis: time.Since(started).Milliseconds(),
		Status: status, Request: asJSON(request), Response: asJSON(response),
	})
	if len(rec.calls) > 400 {
		rec.calls = rec.calls[len(rec.calls)-400:]
	}
}

// mark returns a position; since(mark) is every call made after it.
func (rec *recorder) mark() int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.seq
}

func (rec *recorder) since(mark int) []modelCall {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	var out []modelCall
	for _, c := range rec.calls {
		if c.Seq > mark {
			out = append(out, c)
		}
	}
	return out
}
