package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Drive-time estimates for in-person bookings (event_types.travel_mode = 'google').
//
// The Routes API is asked for the driving time between two addresses. Every answer is
// cached, a daily counter caps the lookups, and any failure (no key, quota, Google error,
// unroutable address) falls back to the event type's fixed drive time, so a travel problem
// can never take bookings offline. The host's home address and the addresses of their
// other meetings are only ever sent to Google as route endpoints; they are never returned
// to the booker.

const (
	travelModeFixed  = "fixed"
	travelModeGoogle = "google"

	routeDailyCap  = 400
	routeCacheTTL  = 12 * time.Hour
	routeCacheSize = 2000
)

var routesClient = &http.Client{
	Timeout:       5 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

type routeCacheEntry struct {
	minutes int
	expires time.Time
}

var (
	routeCacheMu sync.Mutex
	routeCache   = map[string]routeCacheEntry{}
)

// routesKey is the server-side key for the Routes API. It reuses the Places key when no
// dedicated one is set, since both are normally enabled on the same Google project.
func routesKey() string {
	if k := strings.TrimSpace(os.Getenv("BOOKMEET_GOOGLE_ROUTES_KEY")); k != "" {
		return k
	}
	return strings.TrimSpace(os.Getenv("BOOKMEET_GOOGLE_PLACES_KEY"))
}

func routeCacheKey(origin, dest string) string {
	return strings.ToLower(strings.Join(strings.Fields(origin), " ")) + "\x00" +
		strings.ToLower(strings.Join(strings.Fields(dest), " "))
}

// routeMinutes returns the driving time in whole minutes (rounded up) between two
// addresses, or an error when it cannot be worked out. Callers fall back to a fixed time.
func (h *Handler) routeMinutes(ctx context.Context, origin, dest string) (int, error) {
	origin, dest = strings.TrimSpace(origin), strings.TrimSpace(dest)
	if origin == "" || dest == "" {
		return 0, fmt.Errorf("travel: missing address")
	}
	if strings.EqualFold(strings.Join(strings.Fields(origin), " "), strings.Join(strings.Fields(dest), " ")) {
		return 0, nil
	}
	key := routesKey()
	if key == "" {
		return 0, fmt.Errorf("travel: no Routes API key configured")
	}
	ck := routeCacheKey(origin, dest)
	now := time.Now()
	routeCacheMu.Lock()
	if e, ok := routeCache[ck]; ok && now.Before(e.expires) {
		routeCacheMu.Unlock()
		return e.minutes, nil
	}
	routeCacheMu.Unlock()

	// Reserve quota before contacting Google; the database counter is shared across
	// concurrent requests and survives restarts.
	res, err := h.db.ExecContext(ctx, `
		INSERT INTO bookmeet_route_usage (id, day, count) VALUES (1, ?, 1)
		ON CONFLICT(id) DO UPDATE SET day = excluded.day,
		  count = CASE WHEN day = excluded.day THEN count + 1 ELSE 1 END
		WHERE day < excluded.day OR (day = excluded.day AND count < ?)`,
		now.UTC().Format(time.DateOnly), routeDailyCap)
	if err != nil {
		return 0, fmt.Errorf("travel: quota check: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return 0, fmt.Errorf("travel: daily Routes quota reached")
	}

	minutes, err := googleRouteMinutes(ctx, key, origin, dest)
	if err != nil {
		return 0, err
	}
	routeCacheMu.Lock()
	if len(routeCache) >= routeCacheSize {
		routeCache = map[string]routeCacheEntry{}
	}
	routeCache[ck] = routeCacheEntry{minutes: minutes, expires: now.Add(routeCacheTTL)}
	routeCacheMu.Unlock()
	return minutes, nil
}

// routesEndpoint is Google's Routes API; BOOKMEET_ROUTES_URL points it at a stand-in for tests.
func routesEndpoint() string {
	if u := strings.TrimSpace(os.Getenv("BOOKMEET_ROUTES_URL")); u != "" {
		return u
	}
	return "https://routes.googleapis.com/directions/v2:computeRoutes"
}

func googleRouteMinutes(ctx context.Context, key, origin, dest string) (int, error) {
	body, err := json.Marshal(map[string]any{
		"origin":            map[string]any{"address": origin},
		"destination":       map[string]any{"address": dest},
		"travelMode":        "DRIVE",
		"routingPreference": "TRAFFIC_UNAWARE",
	})
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, routesEndpoint(), bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Goog-Api-Key", key)
	req.Header.Set("X-Goog-FieldMask", "routes.duration")
	res, err := routesClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("travel: Routes request: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("travel: Routes returned status %d", res.StatusCode)
	}
	var out struct {
		Routes []struct {
			Duration string `json:"duration"`
		} `json:"routes"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&out); err != nil {
		return 0, err
	}
	if len(out.Routes) == 0 {
		return 0, fmt.Errorf("travel: no route found")
	}
	return durationToMinutes(out.Routes[0].Duration)
}

// durationToMinutes parses the Routes API duration ("1234s") into whole minutes, rounded up.
func durationToMinutes(d string) (int, error) {
	secs, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(d), "s"), 64)
	if err != nil || secs < 0 {
		return 0, fmt.Errorf("travel: bad duration %q", d)
	}
	return int((secs + 59) / 60), nil
}

// travelQuery says where the guest wants to meet, so busy time can be widened by the drive
// to and from each other meeting. A nil *travelQuery means "no per-meeting travel".
type travelQuery struct {
	Dest     string
	Fallback int

	mu sync.Mutex
	// done remembers each origin's answer for this request, and failed stops further
	// Google calls once one has failed, so a Google outage costs one timeout, not one per meeting.
	done      map[string]int
	failed    bool
	Fallbacks int
}

func (t *travelQuery) minutes(ctx context.Context, h *Handler, origin string) int {
	t.mu.Lock()
	if m, ok := t.done[origin]; ok {
		t.mu.Unlock()
		return m
	}
	failed := t.failed
	t.mu.Unlock()

	minutes, used := t.Fallback, false
	if origin != "" && !failed {
		m, err := h.routeMinutes(ctx, origin, t.Dest)
		if err == nil {
			minutes, used = m, true
		} else {
			h.logger.WarnContext(ctx, "drive time unavailable, using the fixed estimate", "error", err)
			t.mu.Lock()
			t.failed = true
			t.mu.Unlock()
		}
	}
	t.mu.Lock()
	if t.done == nil {
		t.done = map[string]int{}
	}
	t.done[origin] = minutes
	if !used {
		t.Fallbacks++
	}
	t.mu.Unlock()
	return minutes
}

// hostHomeAddress is the host's saved starting address ("" when none).
func (h *Handler) hostHomeAddress(ctx context.Context, userID string) string {
	var addr string
	if err := h.db.QueryRowContext(ctx, `SELECT home_address FROM users WHERE id = ?`, userID).Scan(&addr); err != nil {
		return ""
	}
	return strings.TrimSpace(addr)
}

// travelNeighbours reports the host's nearest meetings around [start, end): the origin of
// the trip to this meeting (the previous in-person meeting's address, else home) and the
// address of the next in-person meeting, if one follows within the same stretch of day.
func (h *Handler) travelNeighbours(ctx context.Context, hostID string, start, end time.Time) (origin, next string) {
	const horizon = 12 * time.Hour
	origin = h.hostHomeAddress(ctx, hostID)
	var prevLoc string
	err := h.db.QueryRowContext(ctx, `
		SELECT COALESCE(b.physical_location, '') FROM bookings b
		JOIN booking_hosts bh ON bh.booking_id = b.id
		WHERE bh.user_id = ? AND b.status != 'cancelled'
		  AND b.end_at <= ? AND b.end_at >= ?
		ORDER BY b.end_at DESC LIMIT 1`,
		hostID, start.UTC().Format(time.RFC3339Nano), start.Add(-horizon).UTC().Format(time.RFC3339Nano)).Scan(&prevLoc)
	if err == nil && strings.TrimSpace(prevLoc) != "" {
		origin = strings.TrimSpace(prevLoc)
	}
	var nextLoc string
	err = h.db.QueryRowContext(ctx, `
		SELECT COALESCE(b.physical_location, '') FROM bookings b
		JOIN booking_hosts bh ON bh.booking_id = b.id
		WHERE bh.user_id = ? AND b.status != 'cancelled'
		  AND b.start_at >= ? AND b.start_at <= ?
		ORDER BY b.start_at ASC LIMIT 1`,
		hostID, end.UTC().Format(time.RFC3339Nano), end.Add(horizon).UTC().Format(time.RFC3339Nano)).Scan(&nextLoc)
	if err == nil {
		next = strings.TrimSpace(nextLoc)
	}
	return origin, next
}
