// Package personallink stores personal booking links: a link an owner has made for one named
// guest, limited to the windows the owner chose, usable once.
//
// It is the data layer only. Working out which times are open inside a link's windows, and
// making the booking, stay in the handler package on the shared slot and booking cores.
package personallink

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/calnode/calnode/internal/secret"
	"github.com/calnode/calnode/internal/uid"
)

// Statuses. Open, Proposed and FollowUp are the "active" ones: the guest can still use the
// link. Claiming is transient - a booking attempt holds the link while it runs.
const (
	StatusOpen      = "open"
	StatusProposed  = "proposed" // the guest suggested other times; waiting on the owner
	StatusFollowUp  = "followup" // the guest said they will sort it out with the owner
	StatusClaiming  = "claiming"
	StatusBooked    = "booked"
	StatusExpired   = "expired"
	StatusCancelled = "cancelled"
	StatusReplaced  = "replaced"
)

var (
	ErrNotFound = errors.New("personal link: not found")
	// ErrNotBookable is returned when a link cannot take a booking: already used,
	// cancelled, replaced, expired, or held by another booking attempt right now.
	ErrNotBookable = errors.New("personal link: not bookable")
	ErrInvalid     = errors.New("personal link: invalid")
)

const (
	maxWindows  = 31
	maxNameLen  = 200
	maxPlaceLen = 500 // the same ceiling the booking path puts on an in-person location
	maxTopicLen = 200
	// A claim older than this belongs to a booking attempt that died mid-flight (a crash
	// between claiming and finishing). Booking takes seconds, so two minutes is generous.
	staleClaim = 2 * time.Minute
)

// Window is one stretch of time the owner is offering.
type Window struct {
	Start time.Time
	End   time.Time
}

type Link struct {
	ID           string
	UserID       string
	EventTypeID  string
	GuestName    string
	GuestEmail   string
	GuestPhone   string
	PlaceName    string
	PlaceAddress string
	Topic        string // what the meeting is about, in the owner's words
	// DetailsDone: the guest has been through the email and phone step.
	DetailsDone   bool
	GuestTimezone string // the zone the guest's browser reported; empty until they open the link
	Status        string
	BookingID     string
	ReplacesID    string
	ExpiresAt     time.Time
	LastOpenedAt  time.Time // zero until the guest first opens it
	CreatedAt     time.Time
	UpdatedAt     time.Time
	Windows       []Window
}

// Active reports whether the guest can still use the link.
func (l *Link) Active() bool {
	return l.Status == StatusOpen || l.Status == StatusProposed || l.Status == StatusFollowUp
}

// Covers reports whether a meeting from start to end sits entirely inside one window.
func (l *Link) Covers(start, end time.Time) bool {
	for _, w := range l.Windows {
		if !start.Before(w.Start) && !end.After(w.End) {
			return true
		}
	}
	return false
}

type Service struct {
	db  *sql.DB
	key [32]byte
	now func() time.Time
}

func New(db *sql.DB, key [32]byte) *Service {
	return &Service{db: db, key: key, now: time.Now}
}

// SetClock replaces the service's clock. For tests.
func (s *Service) SetClock(now func() time.Time) { s.now = now }

type CreateParams struct {
	UserID       string
	EventTypeID  string
	GuestName    string
	GuestEmail   string
	GuestPhone   string
	PlaceName    string
	PlaceAddress string
	Topic        string
	// DetailsDone carries "this guest already gave their details" onto a replacement link.
	DetailsDone bool
	Windows     []Window
}

// Create makes a link and returns it with its token. The token is the secret in the URL;
// it can be read again later with Token.
func (s *Service) Create(ctx context.Context, p CreateParams) (*Link, string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", fmt.Errorf("personal link: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	l, token, err := s.insert(ctx, tx, p, "")
	if err != nil {
		return nil, "", err
	}
	if err := tx.Commit(); err != nil {
		return nil, "", fmt.Errorf("personal link: commit: %w", err)
	}
	return l, token, nil
}

// Replace makes a new link in place of oldID and retires the old one, so the old URL stops
// working. What the guest already told us (email, phone) carries over when the new link is
// for the same person and p does not say otherwise.
func (s *Service) Replace(ctx context.Context, userID, oldID string, p CreateParams) (*Link, string, error) {
	old, err := s.Get(ctx, userID, oldID)
	if err != nil {
		return nil, "", err
	}
	if old.Status == StatusBooked || old.Status == StatusClaiming {
		return nil, "", fmt.Errorf("%w: the link has been used", ErrNotBookable)
	}
	p.UserID = userID
	if strings.EqualFold(strings.TrimSpace(p.GuestName), old.GuestName) {
		if p.GuestEmail == "" {
			p.GuestEmail = old.GuestEmail
		}
		if p.GuestPhone == "" {
			p.GuestPhone = old.GuestPhone
		}
		// Someone who already gave an email is not walked through the details step again.
		p.DetailsDone = old.DetailsDone && p.GuestEmail == old.GuestEmail && p.GuestEmail != ""
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", fmt.Errorf("personal link: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	// Guarded on status so a booking that lands between the read above and this write
	// wins: the link it used must not be retired from under it.
	res, err := tx.ExecContext(ctx, `
		UPDATE personal_links SET status = ?, updated_at = ?
		WHERE id = ? AND status NOT IN (?, ?)`,
		StatusReplaced, stamp(s.now()), oldID, StatusBooked, StatusClaiming)
	if err != nil {
		return nil, "", fmt.Errorf("personal link: retire: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, "", fmt.Errorf("%w: the link has been used", ErrNotBookable)
	}
	l, token, err := s.insert(ctx, tx, p, oldID)
	if err != nil {
		return nil, "", err
	}
	if err := tx.Commit(); err != nil {
		return nil, "", fmt.Errorf("personal link: commit: %w", err)
	}
	return l, token, nil
}

func (s *Service) insert(ctx context.Context, tx *sql.Tx, p CreateParams, replacesID string) (*Link, string, error) {
	now := s.now().UTC()
	name := strings.TrimSpace(p.GuestName)
	switch {
	case name == "" || len(name) > maxNameLen || hasControl(name):
		return nil, "", fmt.Errorf("%w: guest name", ErrInvalid)
	case len(p.PlaceName) > maxPlaceLen || len(p.PlaceAddress) > maxPlaceLen || hasControl(p.PlaceName) || hasControl(p.PlaceAddress):
		return nil, "", fmt.Errorf("%w: place", ErrInvalid)
	case len(p.Topic) > maxTopicLen || hasControl(p.Topic):
		return nil, "", fmt.Errorf("%w: topic", ErrInvalid)
	case p.PlaceName != "" && p.PlaceAddress == "":
		// A place is only usable with an address the owner confirmed.
		return nil, "", fmt.Errorf("%w: place has no address", ErrInvalid)
	}
	windows, err := normalizeWindows(p.Windows, now)
	if err != nil {
		return nil, "", err
	}
	var owned int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM event_types WHERE id = ? AND user_id = ?`, p.EventTypeID, p.UserID).Scan(&owned); err != nil {
		return nil, "", fmt.Errorf("personal link: event type: %w", err)
	}
	if owned == 0 {
		return nil, "", fmt.Errorf("%w: event type", ErrInvalid)
	}

	token, err := newToken()
	if err != nil {
		return nil, "", err
	}
	enc, err := secret.Encrypt(s.key, token)
	if err != nil {
		return nil, "", fmt.Errorf("personal link: encrypt token: %w", err)
	}
	l := &Link{
		ID: uid.New(), UserID: p.UserID, EventTypeID: p.EventTypeID,
		GuestName: name, GuestEmail: strings.TrimSpace(p.GuestEmail), GuestPhone: strings.TrimSpace(p.GuestPhone),
		PlaceName: strings.TrimSpace(p.PlaceName), PlaceAddress: strings.TrimSpace(p.PlaceAddress),
		Topic: strings.TrimSpace(p.Topic), DetailsDone: p.DetailsDone && strings.TrimSpace(p.GuestEmail) != "",
		Status: StatusOpen, ReplacesID: replacesID,
		ExpiresAt: windows[len(windows)-1].End, CreatedAt: now, UpdatedAt: now, Windows: windows,
	}
	for _, w := range windows {
		if w.End.After(l.ExpiresAt) {
			l.ExpiresAt = w.End
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO personal_links
		  (id, user_id, event_type_id, token_hash, token_enc, guest_name, guest_email, guest_phone,
		   place_name, place_address, topic, details_done, status, replaces_id, expires_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		l.ID, l.UserID, l.EventTypeID, hashToken(token), enc, l.GuestName, l.GuestEmail, l.GuestPhone,
		l.PlaceName, l.PlaceAddress, l.Topic, l.DetailsDone, l.Status, nullable(replacesID), stamp(l.ExpiresAt), stamp(now), stamp(now)); err != nil {
		return nil, "", fmt.Errorf("personal link: insert: %w", err)
	}
	for _, w := range windows {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO personal_link_windows (id, link_id, start_at, end_at) VALUES (?, ?, ?, ?)`,
			uid.New(), l.ID, stamp(w.Start), stamp(w.End)); err != nil {
			return nil, "", fmt.Errorf("personal link: insert window: %w", err)
		}
	}
	return l, token, nil
}

// normalizeWindows validates the offered windows, sorts them and merges any that touch or
// overlap, so "9 to 11" and "11 to 12" on one day behave as the single window they describe.
func normalizeWindows(in []Window, now time.Time) ([]Window, error) {
	if len(in) == 0 || len(in) > maxWindows {
		return nil, fmt.Errorf("%w: needs between 1 and %d windows", ErrInvalid, maxWindows)
	}
	ws := make([]Window, len(in))
	for i, w := range in {
		ws[i] = Window{Start: w.Start.UTC().Truncate(time.Second), End: w.End.UTC().Truncate(time.Second)}
		if !ws[i].Start.Before(ws[i].End) {
			return nil, fmt.Errorf("%w: a window ends before it starts", ErrInvalid)
		}
	}
	sort.Slice(ws, func(i, j int) bool { return ws[i].Start.Before(ws[j].Start) })
	out := ws[:1]
	for _, w := range ws[1:] {
		last := &out[len(out)-1]
		if !w.Start.After(last.End) {
			if w.End.After(last.End) {
				last.End = w.End
			}
			continue
		}
		out = append(out, w)
	}
	if !out[len(out)-1].End.After(now) {
		return nil, fmt.Errorf("%w: every window is in the past", ErrInvalid)
	}
	return out, nil
}

const linkColumns = `id, user_id, event_type_id, guest_name, guest_email, guest_phone, place_name, place_address, topic, details_done, guest_timezone,
	status, COALESCE(booking_id, ''), COALESCE(replaces_id, ''), expires_at, COALESCE(last_opened_at, ''), created_at, updated_at`

// ByToken finds the link a URL token belongs to. An unknown token and a malformed one are
// the same answer, so the endpoint that calls this gives nothing away.
func (s *Service) ByToken(ctx context.Context, token string) (*Link, error) {
	if token == "" {
		return nil, ErrNotFound
	}
	return s.load(ctx, `SELECT `+linkColumns+` FROM personal_links WHERE token_hash = ?`, hashToken(token))
}

// Get returns one of the owner's links.
func (s *Service) Get(ctx context.Context, userID, id string) (*Link, error) {
	return s.load(ctx, `SELECT `+linkColumns+` FROM personal_links WHERE id = ? AND user_id = ?`, id, userID)
}

// List returns the owner's links, newest first: the log a lost link is resent from.
func (s *Service) List(ctx context.Context, userID string) ([]*Link, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM personal_links WHERE user_id = ? ORDER BY created_at DESC, rowid DESC LIMIT 200`, userID)
	if err != nil {
		return nil, fmt.Errorf("personal link: list: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("personal link: list: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("personal link: list: %w", err)
	}
	out := make([]*Link, 0, len(ids))
	for _, id := range ids {
		l, err := s.Get(ctx, userID, id)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, nil
}

func (s *Service) load(ctx context.Context, query string, args ...any) (*Link, error) {
	var l Link
	var expires, opened, created, updated string
	err := s.db.QueryRowContext(ctx, query, args...).Scan(
		&l.ID, &l.UserID, &l.EventTypeID, &l.GuestName, &l.GuestEmail, &l.GuestPhone, &l.PlaceName, &l.PlaceAddress, &l.Topic, &l.DetailsDone, &l.GuestTimezone,
		&l.Status, &l.BookingID, &l.ReplacesID, &expires, &opened, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("personal link: load: %w", err)
	}
	l.ExpiresAt, l.LastOpenedAt, l.CreatedAt, l.UpdatedAt = parse(expires), parse(opened), parse(created), parse(updated)

	// Expiry is applied when a link is read rather than by a timer: nothing depends on the
	// moment it flips, only on what a reader sees. A link waiting on the owner's answer to
	// suggested times is left alone - those times can fall after the offered windows.
	if (l.Status == StatusOpen || l.Status == StatusFollowUp) && !l.ExpiresAt.After(s.now()) {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE personal_links SET status = ?, updated_at = ? WHERE id = ? AND status = ?`,
			StatusExpired, stamp(s.now()), l.ID, l.Status); err != nil {
			return nil, fmt.Errorf("personal link: expire: %w", err)
		}
		l.Status = StatusExpired
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT start_at, end_at FROM personal_link_windows WHERE link_id = ? ORDER BY start_at`, l.ID)
	if err != nil {
		return nil, fmt.Errorf("personal link: windows: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var a, b string
		if err := rows.Scan(&a, &b); err != nil {
			return nil, fmt.Errorf("personal link: windows: %w", err)
		}
		l.Windows = append(l.Windows, Window{Start: parse(a), End: parse(b)})
	}
	return &l, rows.Err()
}

// Token returns the link's URL token, so the owner can be shown the same link again.
func (s *Service) Token(ctx context.Context, userID, id string) (string, error) {
	var enc string
	err := s.db.QueryRowContext(ctx,
		`SELECT token_enc FROM personal_links WHERE id = ? AND user_id = ?`, id, userID).Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("personal link: token: %w", err)
	}
	token, err := secret.Decrypt(s.key, enc)
	if err != nil {
		return "", fmt.Errorf("personal link: decrypt token: %w", err)
	}
	return token, nil
}

// MarkOpened records that the guest opened the link.
func (s *Service) MarkOpened(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE personal_links SET last_opened_at = ? WHERE id = ?`, stamp(s.now()), id)
	return err
}

// SaveGuestDetails stores what the guest told the assistant. An empty value leaves the
// stored one as it is.
func (s *Service) SaveGuestDetails(ctx context.Context, id, email, phone string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE personal_links
		SET guest_email = CASE WHEN ? != '' THEN ? ELSE guest_email END,
		    guest_phone = CASE WHEN ? != '' THEN ? ELSE guest_phone END,
		    updated_at = ?
		WHERE id = ?`, email, email, phone, phone, stamp(s.now()), id)
	return err
}

// SetDetailsDone records that the guest has finished the email and phone step.
func (s *Service) SetDetailsDone(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE personal_links SET details_done = 1, updated_at = ? WHERE id = ?`, stamp(s.now()), id)
	return err
}

// SetGuestTimezone records the zone the guest's browser reported.
func (s *Service) SetGuestTimezone(ctx context.Context, id, tz string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE personal_links SET guest_timezone = ? WHERE id = ?`, tz, id)
	return err
}

// Message is one entry in the guest's conversation on a link.
type Message struct {
	ID        int64
	Role      string // "guest" or "assistant"
	Kind      string // "text", or a card kind the page knows how to draw
	Content   string
	CreatedAt time.Time
}

// AddMessage appends to the link's conversation.
func (s *Service) AddMessage(ctx context.Context, linkID, role, kind, content string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO personal_link_messages (link_id, role, kind, content, created_at) VALUES (?, ?, ?, ?, ?)`,
		linkID, role, kind, content, stamp(s.now()))
	return err
}

// Messages returns the link's conversation, oldest first.
func (s *Service) Messages(ctx context.Context, linkID string) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, role, kind, content, created_at FROM personal_link_messages
		WHERE link_id = ? ORDER BY id LIMIT 500`, linkID)
	if err != nil {
		return nil, fmt.Errorf("personal link: messages: %w", err)
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var created string
		if err := rows.Scan(&m.ID, &m.Role, &m.Kind, &m.Content, &created); err != nil {
			return nil, fmt.Errorf("personal link: messages: %w", err)
		}
		m.CreatedAt = parse(created)
		out = append(out, m)
	}
	return out, rows.Err()
}

// SetStatus moves an active link between the active statuses (open, proposed, followup).
// Every other transition has its own method, because each has a rule attached.
func (s *Service) SetStatus(ctx context.Context, id, status string) error {
	if status != StatusOpen && status != StatusProposed && status != StatusFollowUp {
		return fmt.Errorf("%w: status %q", ErrInvalid, status)
	}
	return s.transition(ctx, `
		UPDATE personal_links SET status = ?, updated_at = ?
		WHERE id = ? AND status IN (?, ?, ?)`,
		status, stamp(s.now()), id, StatusOpen, StatusProposed, StatusFollowUp)
}

// Cancel retires an active link at the owner's request.
func (s *Service) Cancel(ctx context.Context, userID, id string) error {
	return s.transition(ctx, `
		UPDATE personal_links SET status = ?, updated_at = ?
		WHERE id = ? AND user_id = ? AND status IN (?, ?, ?)`,
		StatusCancelled, stamp(s.now()), id, userID, StatusOpen, StatusProposed, StatusFollowUp)
}

// Claim takes the link for one booking attempt. It is what makes a link single use: the
// update succeeds for exactly one caller, and everyone else gets ErrNotBookable. The caller
// must finish with MarkBooked, or Release if the booking did not happen.
func (s *Service) Claim(ctx context.Context, id string) error {
	now := s.now().UTC()
	return s.transition(ctx, `
		UPDATE personal_links
		SET claimed_from = CASE WHEN status = ? THEN claimed_from ELSE status END,
		    status = ?, updated_at = ?
		WHERE id = ?
		  AND ( status = ?
		     OR (status IN (?, ?) AND expires_at > ?)
		     OR (status = ? AND updated_at < ?) )`,
		StatusClaiming, StatusClaiming, stamp(now), id,
		StatusProposed, // an owner-approved suggestion may fall after the offered windows
		StatusOpen, StatusFollowUp, stamp(now),
		StatusClaiming, stamp(now.Add(-staleClaim)))
}

// Release gives a claimed link back after a booking attempt that did not happen.
func (s *Service) Release(ctx context.Context, id string) error {
	return s.transition(ctx, `
		UPDATE personal_links SET status = claimed_from, claimed_from = '', updated_at = ?
		WHERE id = ? AND status = ? AND claimed_from != ''`,
		stamp(s.now()), id, StatusClaiming)
}

// MarkBooked finishes a claim: the link is used, and points at its booking.
func (s *Service) MarkBooked(ctx context.Context, id, bookingID string) error {
	return s.transition(ctx, `
		UPDATE personal_links SET status = ?, booking_id = ?, claimed_from = '', updated_at = ?
		WHERE id = ? AND status = ?`,
		StatusBooked, bookingID, stamp(s.now()), id, StatusClaiming)
}

// transition runs a guarded status update and reports ErrNotBookable when the guard
// rejected it, which is how every caller learns the link was not in the state it assumed.
func (s *Service) transition(ctx context.Context, query string, args ...any) error {
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("personal link: update: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotBookable
	}
	return nil
}

func newToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("personal link: token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func parse(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func hasControl(s string) bool { return strings.ContainsAny(s, "\r\n\x00") }
