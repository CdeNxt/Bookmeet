package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/calnode/calnode/internal/booking"
	"github.com/calnode/calnode/internal/mailer"
	"github.com/calnode/calnode/internal/personallink"
	"github.com/calnode/calnode/internal/uid"
)

// The approval email: how an owner with no BookMeet card in front of them and no text
// channel decides something only they may decide (J, 2026-11-24).
//
//   - It is sent only when there is no Telegram chat to ask on. A card app never sends one:
//     the approval is on the card.
//   - The email carries a link to a BookMeet page. Opening it changes nothing, so a mail
//     scanner cannot approve. The page needs the owner's own signed-in session and a button
//     press, so an agent that can read the mailbox cannot approve either.
//   - It runs the same paths as the card's taps (approveDirectNow, approveLinkNow,
//     approveSuggestionNow) and waives only the reasons the page showed.
//   - The email names kinds of reason only. Calendar titles are shown on the signed-in page.

const (
	emailApprovalDirect     = approveDirect
	emailApprovalLink       = approveLink
	emailApprovalSuggestion = approveSuggestion

	emailApprovalLifetime = 7 * 24 * time.Hour
	emailApprovalsPerHour = 10
	emailApprovalWorking  = "working"
)

// emailApprovalPayload is what exactly one emailed approval would do.
type emailApprovalPayload struct {
	Slug     string                     `json:"slug,omitempty"` // direct
	Name     string                     `json:"name,omitempty"`
	Email    string                     `json:"email,omitempty"`
	Start    time.Time                  `json:"start,omitzero"`
	Link     *personallink.CreateParams `json:"link,omitempty"`
	Replaces string                     `json:"replaces,omitempty"`
	HeldID   int64                      `json:"held_id,omitempty"` // suggestion
}

// emailOwnerApproval stores one pending decision and emails the owner its link. latest is
// the last moment the decision is still worth making.
func (h *Handler) emailOwnerApproval(ctx context.Context, userID, kind string, p emailApprovalPayload, latest time.Time, subject, body string) error {
	if !h.isEmailEnabled() {
		return errors.New("email is not set up")
	}
	var to string
	if err := h.db.QueryRowContext(ctx, `SELECT email FROM users WHERE id = ? AND archived_at IS NULL`, userID).Scan(&to); err != nil {
		return fmt.Errorf("owner email: %w", err)
	}
	now := time.Now().UTC()
	var recent int
	if err := h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM owner_email_approvals WHERE user_id = ? AND created_at > ?`,
		userID, now.Add(-time.Hour).Format(time.RFC3339)).Scan(&recent); err != nil {
		return fmt.Errorf("count approvals: %w", err)
	}
	if recent >= emailApprovalsPerHour {
		return errors.New("too many approval emails in the last hour")
	}
	blob, err := json.Marshal(p)
	if err != nil {
		return err
	}
	token, err := randomURLToken(32)
	if err != nil {
		return err
	}
	expires := now.Add(emailApprovalLifetime)
	if latest.Before(expires) {
		expires = latest
	}
	id := uid.New()
	if _, err := h.db.ExecContext(ctx, `
		INSERT INTO owner_email_approvals (id, token_hash, user_id, kind, payload, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, id, hashAPIKey(token), userID, kind, string(blob),
		now.Format(time.RFC3339), expires.UTC().Format(time.RFC3339)); err != nil {
		return fmt.Errorf("save approval: %w", err)
	}
	address := strings.TrimRight(h.publicURL(), "/") + "/approve/" + token
	text := body + "\n\nReview and decide:\n" + address +
		"\n\nOpening the link changes nothing. Nothing happens until you sign in to BookMeet there and tap a button."
	err = h.mailer.Send(ctx, mailer.Message{To: []string{to}, Subject: oneLine(subject), Text: text})
	if err != nil {
		// Nobody has the link, so the row is no use; a retry makes a new one.
		_, _ = h.db.ExecContext(ctx, `DELETE FROM owner_email_approvals WHERE id = ?`, id)
		return fmt.Errorf("send approval email: %w", err)
	}
	return nil
}

// oneLine keeps a name someone typed from breaking a mail header.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// ── asking, when there is no Telegram ───────────────────────────────────────

// noWayToAsk is the agent's answer when neither Telegram nor email can reach the owner.
const noWayToAsk = "BookMeet has no way to ask the owner: no Telegram assistant is connected and the approval email could not be sent (email is set up in BookMeet, Settings → Email)"

func (h *Handler) emailDirectApproval(ctx context.Context, o *mcpOwner, et *ownerEventType, name, email string, start time.Time) string {
	v := h.timeVerdictFor(ctx, et.Slug, et.Minutes, start, "", o.UserID)
	body := fmt.Sprintf("Your AI app asked to book a time that does not book as things stand.\n\nGuest: %s <%s>\nMeeting: %s\nWhen: %s (%s)\n\n%s",
		name, email, et.Name, o.when(start), o.zone(), strings.Join(v.ownerReasons(), "\n"))
	if err := h.emailOwnerApproval(ctx, o.UserID, emailApprovalDirect,
		emailApprovalPayload{Slug: et.Slug, Name: name, Email: email, Start: start}, start,
		"Approve: book "+name+" on "+o.when(start)+"?", body); err != nil {
		h.logger.WarnContext(ctx, "approval email: direct", "error", err)
		return "nothing was booked, and " + noWayToAsk
	}
	return "nothing is booked yet. The owner has been emailed a link to review it and book it with their own tap. get_booking or list_bookings will show it once they do"
}

func (h *Handler) emailLinkApproval(ctx context.Context, o *mcpOwner, et *ownerEventType, p personallink.CreateParams, replaces string, check *mcpOfferCheck) string {
	var lines []string
	for _, w := range check.Windows {
		line := w.Text
		if w.NeedsApproval {
			line += " (outside your usual booking rules)"
		}
		lines = append(lines, line)
	}
	last := p.Windows[len(p.Windows)-1].End
	body := fmt.Sprintf("Your AI app asked for a personal link with times outside your usual booking rules.\n\nGuest: %s\nMeeting: %s\nTimes (%s):\n%s",
		p.GuestName, et.Name, o.zone(), strings.Join(lines, "\n"))
	if err := h.emailOwnerApproval(ctx, o.UserID, emailApprovalLink,
		emailApprovalPayload{Link: &p, Replaces: replaces}, last, "Approve: a booking link for "+p.GuestName+"?", body); err != nil {
		h.logger.WarnContext(ctx, "approval email: link", "error", err)
		return "no link was made, and " + noWayToAsk + ". Or offer times inside their hours instead"
	}
	return "no link is made yet. The owner has been emailed a link to review the times and create it with their own tap; the new link's address is shown to them there. list_personal_links will show it once they do"
}

// emailSuggestionApproval emails the owner the times a guest suggested, with anything else
// the guest left, each time marked with what is in its way.
func (h *Handler) emailSuggestionApproval(ctx context.Context, l *personallink.Link, held personallink.Held, others []personallink.Held) error {
	o, err := h.mcpOwnerByID(ctx, l.UserID)
	if err != nil {
		return err
	}
	var starts []string
	_ = json.Unmarshal([]byte(held.Content), &starts)
	var lines []string
	var last time.Time
	for _, s := range starts {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			continue
		}
		if t.After(last) {
			last = t
		}
		note := "free as things stand"
		if reasons := h.suggestedTimeVerdict(ctx, l, t).ownerReasons(); len(reasons) > 0 {
			note = strings.Join(reasons, " ")
		}
		lines = append(lines, o.when(t)+": "+note)
	}
	meeting := h.eventTypeNames(ctx, l.UserID)[l.EventTypeID]
	body := fmt.Sprintf("%s suggested other times for %s.\n\nTimes in %s:\n%s", l.GuestName, meeting, o.zone(), strings.Join(lines, "\n"))
	for _, item := range others {
		if item.Kind != personallink.HeldSuggestions {
			body += "\n\nTyped by " + l.GuestName + ":\n\"" + item.Content + "\""
		}
	}
	return h.emailOwnerApproval(ctx, l.UserID, emailApprovalSuggestion,
		emailApprovalPayload{HeldID: held.ID}, last, l.GuestName+" suggested other times", body)
}

// reportChatByEmail is reportChat for an owner with no Telegram chat: only suggested times
// need a decision, so only they send an email, carrying the rest the guest left with them.
func (h *Handler) reportChatByEmail(ctx context.Context, l *personallink.Link, fresh []personallink.Held) {
	if !h.isEmailEnabled() {
		return // nowhere to send it yet; the items stay held and go out once there is
	}
	links := h.personalLinks()
	var suggestions *personallink.Held
	ids := make([]int64, 0, len(fresh))
	for i := range fresh {
		ids = append(ids, fresh[i].ID)
		if fresh[i].Kind == personallink.HeldSuggestions {
			if suggestions != nil {
				_ = links.ResolveHeld(ctx, suggestions.ID, "superseded")
			}
			suggestions = &fresh[i]
		}
	}
	if suggestions == nil {
		return
	}
	if err := h.emailSuggestionApproval(ctx, l, *suggestions, fresh); err != nil {
		h.logger.WarnContext(ctx, "personal link report: email", "error", err)
		return // not marked: the next sweep tries again
	}
	if err := links.MarkHeldReported(ctx, ids, 0); err != nil {
		h.logger.WarnContext(ctx, "personal link report: mark held", "error", err)
	}
	_ = links.MarkReported(ctx, l.ID)
	if l.Active() {
		_ = links.SetStatus(ctx, l.ID, personallink.StatusProposed)
	}
}

// ── the page ────────────────────────────────────────────────────────────────

type approvalPage struct {
	Title    string
	Lines    []string
	Notes    []string // warnings: why it does not book as things stand
	Quotes   []string // what the guest typed
	Choices  []approvalChoice
	Hidden   map[string]string
	Result   string
	Copy     []string // values to copy after a link is made
	SignIn   []approvalSignIn
	BadState string
}

type approvalChoice struct {
	Label, Value, Detail string
	Secondary            bool
}

type approvalSignIn struct{ Label, URL string }

var approvalTmpl = template.Must(template.New("approve").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="robots" content="noindex"><title>BookMeet approval</title>
<style>
  :root{color-scheme:light dark;--bg:#eae9e1;--card:#fff;--fg:#252735;--muted:#5c6376;--line:#d8d6cc;--accent:#5965e9;--warn:#8a5a00;--warnbg:#fdf3e1}
  @media(prefers-color-scheme:dark){:root{--bg:#15161e;--card:#1d1f2a;--fg:#f2f2f7;--muted:#b6b7c7;--line:#303342;--warn:#f0c060;--warnbg:#2b2312}}
  body{font:16px/1.5 Inter,system-ui,sans-serif;margin:0;min-height:100vh;display:grid;place-items:center;background:var(--bg);color:var(--fg)}
  main{box-sizing:border-box;width:calc(100% - 32px);max-width:460px;background:var(--card);border:1px solid var(--line);border-radius:20px;padding:24px;margin:16px}
  .brand{font-size:12px;letter-spacing:.08em;color:var(--muted)}
  h1{font-size:20px;margin:8px 0 12px}
  p{margin:6px 0;overflow-wrap:anywhere}
  .note{background:var(--warnbg);color:var(--warn);border-radius:12px;padding:10px 12px;margin-top:12px}
  blockquote{margin:12px 0 0;padding-left:12px;border-left:3px solid var(--line);color:var(--muted);overflow-wrap:anywhere}
  .choice{margin-top:16px}
  button,a.btn{display:block;box-sizing:border-box;width:100%;text-align:center;text-decoration:none;font:inherit;font-weight:600;padding:12px 16px;border-radius:999px;border:0;background:var(--accent);color:#fff;cursor:pointer}
  button.secondary,a.btn.secondary{background:transparent;color:inherit;border:1px solid var(--line)}
  .detail{font-size:14px;color:var(--muted);margin-bottom:6px}
  input[type=text]{box-sizing:border-box;width:100%;font:inherit;padding:10px;border-radius:10px;border:1px solid var(--line);background:transparent;color:inherit;margin-top:10px}
</style></head><body><main>
<div class="brand">BOOKMEET</div>
<h1>{{.Title}}</h1>
{{range .Lines}}<p>{{.}}</p>{{end}}
{{range .Quotes}}<blockquote>{{.}}</blockquote>{{end}}
{{range .Notes}}<div class="note">{{.}}</div>{{end}}
{{if .BadState}}<div class="note">{{.BadState}}</div>{{end}}
{{if .Result}}<p><strong>{{.Result}}</strong></p>{{end}}
{{range .Copy}}<input type="text" readonly value="{{.}}">{{end}}
{{range .SignIn}}<div class="choice"><a class="btn" href="{{.URL}}">{{.Label}}</a></div>{{end}}
{{if .Choices}}<form method="post">
  {{range $k, $v := .Hidden}}<input type="hidden" name="{{$k}}" value="{{$v}}">{{end}}
  {{range .Choices}}<div class="choice">{{if .Detail}}<div class="detail">{{.Detail}}</div>{{end}}<button name="do" value="{{.Value}}"{{if .Secondary}} class="secondary"{{end}}>{{.Label}}</button></div>{{end}}
</form>{{end}}
</main></body></html>`))

func (h *Handler) renderApprovalPage(w http.ResponseWriter, status int, p approvalPage) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	w.WriteHeader(status)
	if err := approvalTmpl.Execute(w, p); err != nil {
		h.logger.Error("approval page", "error", err)
	}
}

type pendingApproval struct {
	id, userID, kind, outcome string
	expires                   time.Time
	payload                   emailApprovalPayload
}

// approvalForRequest finds the approval a request's token names and checks the signed-in
// user is its owner. It writes the page itself when the answer is no.
func (h *Handler) approvalForRequest(w http.ResponseWriter, r *http.Request) (*pendingApproval, *mcpOwner, bool) {
	userID, email, signedIn := h.sessionUser(r)
	if !signedIn {
		// Say nothing about the approval to someone who is not signed in.
		h.setOAuthReturn(w, r.URL.Path)
		p := approvalPage{Title: "Sign in to decide", Lines: []string{"Sign in to BookMeet in this browser. If you are not brought back here, open the link in the email again."}}
		if h.getGoogleAuth() != nil {
			p.SignIn = append(p.SignIn, approvalSignIn{"Sign in with Google", "/v1/auth/login"})
		}
		if h.getMicrosoftAuth() != nil {
			p.SignIn = append(p.SignIn, approvalSignIn{"Sign in with Microsoft", "/v1/auth/microsoft/login"})
		}
		p.SignIn = append(p.SignIn, approvalSignIn{"Sign in to BookMeet", "/admin/login"})
		h.renderApprovalPage(w, http.StatusUnauthorized, p)
		return nil, nil, false
	}
	var a pendingApproval
	var blob, expires string
	err := h.db.QueryRowContext(r.Context(), `
		SELECT id, user_id, kind, payload, expires_at, outcome FROM owner_email_approvals WHERE token_hash = ?`,
		hashAPIKey(r.PathValue("token"))).Scan(&a.id, &a.userID, &a.kind, &blob, &expires, &a.outcome)
	// Another account's approval reads exactly like one that does not exist.
	if errors.Is(err, sql.ErrNoRows) || err == nil && a.userID != userID {
		h.renderApprovalPage(w, http.StatusNotFound, approvalPage{Title: "Nothing to approve",
			Lines: []string{"This link is not an approval for " + email + ". If you have more than one account, sign in with the one the email was sent to."}})
		return nil, nil, false
	}
	if err == nil {
		err = json.Unmarshal([]byte(blob), &a.payload)
	}
	var o *mcpOwner
	if err == nil {
		o, err = h.mcpOwnerByID(r.Context(), userID)
	}
	if err != nil {
		h.logger.ErrorContext(r.Context(), "approval page: load", "error", err)
		h.renderApprovalPage(w, http.StatusInternalServerError, approvalPage{Title: "Something went wrong", Lines: []string{"Try the link again in a moment."}})
		return nil, nil, false
	}
	a.expires, _ = time.Parse(time.RFC3339, expires)
	switch {
	case a.outcome != "":
		h.renderApprovalPage(w, http.StatusOK, approvalPage{Title: "Already answered", Lines: []string{"This request has been answered. Nothing more happens."}})
		return nil, nil, false
	case !time.Now().Before(a.expires):
		h.renderApprovalPage(w, http.StatusGone, approvalPage{Title: "This request has expired", Lines: []string{"Nothing was booked or created."}})
		return nil, nil, false
	}
	return &a, o, true
}

// ApprovalPage handles GET /approve/{token}. It only shows; it never changes anything.
func (h *Handler) ApprovalPage(w http.ResponseWriter, r *http.Request) {
	a, o, ok := h.approvalForRequest(w, r)
	if !ok {
		return
	}
	h.renderApprovalPage(w, http.StatusOK, h.describeApproval(r.Context(), a, o))
}

const exceptionsOffPage = `"Allow exceptions to my availability" is off in BookMeet, Settings → AI, so this cannot be approved.`

// describeApproval draws a pending decision as it stands now.
func (h *Handler) describeApproval(ctx context.Context, a *pendingApproval, o *mcpOwner) approvalPage {
	p := approvalPage{Hidden: map[string]string{}}
	decline := approvalChoice{Label: "Decline", Value: "decline", Secondary: true}
	exceptions := h.assistantExceptions(ctx)
	// timeNotes is what stands in a time's way, with calendar names: this page is the
	// owner's alone.
	timeNotes := func(v timeVerdict) []string {
		out := v.ownerReasons()
		if what := conflictsText(v, o.Loc, o.clock); what != "" {
			out = append(out, "On your calendar: "+what+".")
		}
		if v.Unknown {
			out = append(out, "That time has passed, or your calendar could not be checked.")
		}
		if v.SameStart {
			out = append(out, "Another BookMeet booking starts at exactly that time, which nothing can book over.")
		}
		return out
	}
	// choice is the button for one time, or false when it cannot be booked from here.
	choice := func(v timeVerdict, value string) (approvalChoice, bool) {
		switch {
		case v.Unknown || v.SameStart:
			return approvalChoice{}, false
		case v.free():
			return approvalChoice{Label: "Book it", Value: value}, true
		case !exceptions:
			return approvalChoice{}, false
		}
		return approvalChoice{Label: "Book anyway", Value: value}, true
	}
	shown := func(key string, v timeVerdict) {
		blob, _ := json.Marshal(v.shown())
		p.Hidden[key] = string(blob)
	}
	switch a.kind {
	case emailApprovalDirect:
		d := a.payload
		var meeting string
		var minutes int
		if err := h.db.QueryRowContext(ctx, `SELECT name, duration_minutes FROM event_types WHERE slug = ? AND user_id = ?`, d.Slug, a.userID).Scan(&meeting, &minutes); err != nil {
			return approvalPage{Title: "This can no longer be booked", Lines: []string{"That meeting type is no longer available."}}
		}
		v := h.timeVerdictFor(ctx, d.Slug, minutes, d.Start, "", a.userID)
		p.Title = "Book " + d.Name + "?"
		if !v.free() {
			p.Title = "Book " + d.Name + " anyway?"
		}
		p.Lines = []string{meeting, o.when(d.Start) + " (" + o.zone() + ")", d.Email}
		p.Notes = timeNotes(v)
		shown("shown", v)
		if c, ok := choice(v, "book"); ok {
			p.Choices = append(p.Choices, c)
		} else if !exceptions && !v.free() {
			p.Notes = append(p.Notes, exceptionsOffPage)
		}
		p.Choices = append(p.Choices, decline)
	case emailApprovalLink:
		l := a.payload.Link
		if l == nil {
			return approvalPage{Title: "Nothing to approve"}
		}
		p.Title = "Create a link for " + l.GuestName + "?"
		p.Lines = []string{h.eventTypeNames(ctx, a.userID)[l.EventTypeID]}
		for _, w := range l.Windows {
			p.Lines = append(p.Lines, o.span(w.Start, w.End))
		}
		p.Notes = []string{"Some of these times are outside your usual booking rules. Times you are busy are still left out."}
		if exceptions {
			p.Choices = append(p.Choices, approvalChoice{Label: "Allow and create link", Value: "create"})
		} else {
			p.Notes = append(p.Notes, exceptionsOffPage)
		}
		p.Choices = append(p.Choices, decline)
	case emailApprovalSuggestion:
		links := h.personalLinks()
		held, err := links.HeldByID(ctx, a.userID, a.payload.HeldID)
		if err != nil || held.Outcome != "" {
			return approvalPage{Title: "Already answered", Lines: []string{"This has been answered elsewhere. Nothing more happens."}}
		}
		l, err := links.Get(ctx, a.userID, held.LinkID)
		if err != nil {
			return approvalPage{Title: "That link is no longer available"}
		}
		p.Title = l.GuestName + " suggested other times"
		p.Lines = []string{h.eventTypeNames(ctx, a.userID)[l.EventTypeID], "Times in " + o.zone()}
		items, _ := links.HeldItems(ctx, l.ID)
		for _, item := range items {
			if item.Outcome == "" && item.Kind != personallink.HeldSuggestions {
				p.Quotes = append(p.Quotes, l.GuestName+" wrote: "+item.Content)
			}
		}
		var starts []string
		_ = json.Unmarshal([]byte(held.Content), &starts)
		for i, s := range starts {
			t, err := time.Parse(time.RFC3339, s)
			if err != nil {
				continue
			}
			v := h.suggestedTimeVerdict(ctx, l, t)
			detail := strings.Join(timeNotes(v), " ")
			if c, ok := choice(v, "time-"+strconv.Itoa(i)); ok {
				c.Label += ": " + o.when(t)
				c.Detail = detail
				p.Choices = append(p.Choices, c)
				shown("shown-"+strconv.Itoa(i), v)
			} else {
				p.Notes = append(p.Notes, o.when(t)+": "+detail)
			}
		}
		if !exceptions {
			p.Notes = append(p.Notes, `Times that need an exception cannot be booked while "Allow exceptions to my availability" is off in BookMeet, Settings → AI.`)
		}
		p.Choices = append(p.Choices, approvalChoice{Label: "None of these", Value: "decline", Secondary: true})
	}
	return p
}

// DecideApproval handles POST /approve/{token}: the owner's button.
func (h *Handler) DecideApproval(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	a, o, ok := h.approvalForRequest(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderApprovalPage(w, http.StatusBadRequest, h.describeApproval(r.Context(), a, o))
		return
	}
	ctx := r.Context()
	// Claim it: two presses cannot both act.
	res, err := h.db.ExecContext(ctx, `UPDATE owner_email_approvals SET outcome = ? WHERE id = ? AND outcome = ''`, emailApprovalWorking, a.id)
	if n, _ := rowsAffected(res, err); n != 1 {
		h.renderApprovalPage(w, http.StatusConflict, approvalPage{Title: "Already answered", Lines: []string{"This request is being answered or has been."}})
		return
	}
	do := r.PostForm.Get("do")
	page, outcome, err := h.actOnApproval(ctx, a, o, do, r.PostForm.Get)
	// Not decided (something changed, or it failed): the link keeps working.
	decidedAt := ""
	if err != nil {
		outcome = ""
	}
	if outcome != "" {
		decidedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if _, uerr := h.db.ExecContext(context.WithoutCancel(ctx), `UPDATE owner_email_approvals SET outcome = ?, decided_at = ? WHERE id = ?`, outcome, decidedAt, a.id); uerr != nil {
		h.logger.ErrorContext(ctx, "approval page: record outcome", "error", uerr)
	}
	if err != nil {
		page = h.describeApproval(ctx, a, o)
		page.BadState = capitalise(err.Error()) + "."
	}
	h.renderApprovalPage(w, http.StatusOK, page)
}

func rowsAffected(res sql.Result, err error) (int64, error) {
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + strings.TrimSuffix(s[1:], ".")
}

const approvalViaEmail = "from the approval email"

// actOnApproval does what the button says, through the same paths as the card's taps. An
// empty outcome with a page means nothing was decided and the page asks again.
func (h *Handler) actOnApproval(ctx context.Context, a *pendingApproval, o *mcpOwner, do string, form func(string) string) (approvalPage, string, error) {
	var none approvalPage
	shownAt := func(key string) timeVerdict {
		var s shownReasons
		_ = json.Unmarshal([]byte(form(key)), &s)
		return s.verdict()
	}
	again := func() (approvalPage, string, error) {
		p := h.describeApproval(ctx, a, o)
		p.BadState = "Something changed since this page was drawn, so nothing was done. Check it again."
		return p, "", nil
	}
	done := func(title, result string) approvalPage { return approvalPage{Title: title, Result: result} }

	if do == "decline" {
		if a.kind == emailApprovalSuggestion {
			links := h.personalLinks()
			held, err := links.HeldByID(ctx, a.userID, a.payload.HeldID)
			if err != nil || links.ResolveHeld(ctx, held.ID, "none") != nil {
				return done("Already answered", "This was answered elsewhere."), "declined", nil
			}
			if l, err := links.Get(ctx, a.userID, held.LinkID); err == nil && l.Status == personallink.StatusProposed {
				_ = links.SetStatus(ctx, l.ID, personallink.StatusOpen)
			}
			h.rememberFor(ctx, a.userID, "event", "From the approval email, the owner chose none of a guest's suggested times. The guest was not told.")
			return done("None booked", "The guest has not been told, and the link still works for the times you offered."), "declined", nil
		}
		return done("Declined", "Nothing was booked or created."), "declined", nil
	}
	// The owner's setting is read at the tap, as for a card.
	switch a.kind {
	case emailApprovalDirect:
		if do != "book" {
			return again()
		}
		shown := shownAt("shown")
		if !shown.free() && !h.assistantExceptions(ctx) {
			return again()
		}
		ap := &ownerApproval{UserID: a.userID, Kind: approveDirect, Via: approvalViaEmail}
		d := a.payload
		ap.Direct.Slug, ap.Direct.Name, ap.Direct.Email, ap.Direct.Start, ap.Direct.Shown = d.Slug, d.Name, d.Email, d.Start, shown
		_, out, err := h.approveDirectNow(ctx, o, ap)
		if err != nil {
			return none, "", err
		}
		if out.Booked == nil {
			return again()
		}
		return done("Booked", out.Result), "booked", nil
	case emailApprovalLink:
		if do != "create" || a.payload.Link == nil || !h.assistantExceptions(ctx) {
			return again()
		}
		ap := &ownerApproval{UserID: a.userID, Kind: approveLink, Via: approvalViaEmail, Link: *a.payload.Link, Replaces: a.payload.Replaces}
		_, out, err := h.approveLinkNow(ctx, o, ap)
		if err != nil {
			return none, "", err
		}
		p := done("Link created", "Copy it now and send it to "+a.payload.Link.GuestName+" yourself; BookMeet does not send it.")
		p.Copy = []string{out.Link.URL, out.Link.MessageToSend}
		return p, "created", nil
	case emailApprovalSuggestion:
		index, err := strconv.Atoi(strings.TrimPrefix(do, "time-"))
		links := h.personalLinks()
		held, herr := links.HeldByID(ctx, a.userID, a.payload.HeldID)
		if err != nil || herr != nil || held.Outcome != "" {
			return again()
		}
		var starts []string
		_ = json.Unmarshal([]byte(held.Content), &starts)
		if index < 0 || index >= len(starts) {
			return again()
		}
		at, err := time.Parse(time.RFC3339, starts[index])
		if err != nil {
			return again()
		}
		l, err := links.Get(ctx, a.userID, held.LinkID)
		if err != nil {
			return none, "", errors.New("that link is no longer available")
		}
		key := "shown-" + strconv.Itoa(index)
		shown := shownAt(key)
		v := h.suggestedTimeVerdict(ctx, l, at)
		if form(key) == "" || v.Unknown || v.SameStart || v.beyond(shown) {
			return again()
		}
		if v.free() {
			organizer := booking.Attendee{Name: l.GuestName, Email: l.GuestEmail, IANATimezone: l.GuestTimezone, Locale: l.GuestLocale}
			b, err := h.bookPersonalLink(ctx, l, at, organizer, true)
			if errors.Is(err, personallink.ErrNotBookable) {
				return none, "", errors.New("that link has already been used or closed, so nothing was booked")
			}
			if err != nil {
				return none, "", ownerBookingError(err)
			}
			h.suggestedTimeBooked(ctx, held, l, at, b, false)
			h.rememberFor(ctx, a.userID, "event", "From the approval email, the owner booked a time "+l.GuestName+" suggested.")
			return done("Booked", "Booked "+l.GuestName+" for "+o.when(at)+"."), "booked", nil
		}
		if !h.assistantExceptions(ctx) {
			return again()
		}
		_, out, err := h.approveSuggestionNow(ctx, o, &ownerApproval{UserID: a.userID, Kind: approveSuggestion, Via: approvalViaEmail, HeldID: held.ID, At: at})
		if err != nil {
			return none, "", err
		}
		return done("Booked", out.Result), "booked", nil
	}
	return again()
}
