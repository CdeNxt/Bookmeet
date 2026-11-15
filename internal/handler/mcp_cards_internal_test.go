package handler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/calnode/calnode/internal/calendar"
	"github.com/calnode/calnode/internal/personallink"
	"github.com/calnode/calnode/internal/slots"
)

// connectCardApp connects as an app that draws MCP Apps cards, the way Claude does.
func connectCardApp(t *testing.T, h *Handler) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	srvT, cliT := mcp.NewInMemoryTransports()
	go func() { _ = h.MCPServer().Run(ctx, srvT) }()
	caps := &mcp.ClientCapabilities{Extensions: map[string]any{
		mcpUIExtension: map[string]any{"mimeTypes": []string{mcpCardMIME}},
	}}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "card-app", Version: "0"}, &mcp.ClientOptions{Capabilities: caps}).Connect(ctx, cliT, nil)
	if err != nil {
		t.Fatalf("MCP connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// callRaw calls a tool and returns the whole result.
func callRaw(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

// approvalOf reads the ticket from a result's structured content and the key from _meta.
func approvalOf(t *testing.T, res *mcp.CallToolResult) (ticket, key string) {
	t.Helper()
	blob, _ := json.Marshal(res.StructuredContent)
	var out struct {
		Approval *mcpApprovalOut `json:"approval"`
	}
	_ = json.Unmarshal(blob, &out)
	if out.Approval == nil || out.Approval.Ticket == "" {
		t.Fatalf("no approval in %s", blob)
	}
	key, _ = res.Meta[approvalKeyMeta].(string)
	if key == "" {
		t.Fatalf("no approval key in _meta: %+v", res.Meta)
	}
	if strings.Contains(string(blob), key) {
		t.Fatal("the approval key is in the structured content, which the model can read")
	}
	return out.Approval.Ticket, key
}

func approve(t *testing.T, cs *mcp.ClientSession, ticket, key string) *mcp.CallToolResult {
	t.Helper()
	return callRaw(t, cs, "owner_approve", map[string]any{"ticket": ticket, "key": key})
}

// The card is served, every assistant tool points at it, and the approval tool is for the
// card alone: hidden from the model by visibility ["app"].
func TestMCPCards_theCardIsServedAndOwnerApproveIsAppOnly(t *testing.T) {
	f := newOwnerFixture(t)
	cs := connectCardApp(t, f.h)
	res, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: mcpCardURI})
	if err != nil || len(res.Contents) != 1 || res.Contents[0].MIMEType != mcpCardMIME || !strings.Contains(res.Contents[0].Text, "ui/initialize") {
		t.Fatalf("card resource: %v %+v", err, res)
	}
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	withCards := map[string]bool{}
	for _, tool := range tools.Tools {
		ui, _ := tool.Meta["ui"].(map[string]any)
		if ui["resourceUri"] == mcpCardURI {
			withCards[tool.Name] = true
		}
		if tool.Name == "owner_approve" {
			vis, _ := ui["visibility"].([]any)
			if len(vis) != 1 || vis[0] != "app" {
				t.Errorf("owner_approve visibility = %v; want [app]", ui["visibility"])
			}
		}
	}
	for _, name := range []string{"check_personal_link_offer", "create_personal_link", "book_for_guest", "answer_guest", "whats_waiting", "list_personal_links", "get_personal_link", "owner_approve"} {
		if !withCards[name] {
			t.Errorf("%s has no card", name)
		}
	}
}

// Times outside the owner's hours on an app that draws cards: no link, an Allow card, and
// one tap makes it. The ticket works once and only with its key.
func TestMCPCards_aTapApprovesALinkOutsideUsualHours(t *testing.T) {
	f := newOwnerFixture(t)
	f.closeDay(t, 3)
	cs := connectCardApp(t, f.h)
	res := callRaw(t, cs, "create_personal_link", offerArgs(3, "09:00", "11:00"))
	if res.IsError {
		t.Fatalf("create on a card app: %+v", res.Content)
	}
	if text := res.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, "only their tap") {
		t.Errorf("the model was not told only the owner's tap approves: %s", text)
	}
	ticket, key := approvalOf(t, res)
	if n := f.count(t, `SELECT COUNT(*) FROM personal_links`); n != 0 {
		t.Fatalf("%d links before the tap", n)
	}
	if bad := approve(t, cs, ticket, "wrong-key"); !bad.IsError {
		t.Fatal("a wrong key approved")
	}
	// A fresh request gets a fresh ticket; the right key spends it.
	res = callRaw(t, cs, "create_personal_link", offerArgs(3, "09:00", "11:00"))
	ticket, key = approvalOf(t, res)
	if ok := approve(t, cs, ticket, key); ok.IsError {
		t.Fatalf("the owner's tap: %+v", ok.Content)
	}
	links := f.links(t)
	if len(links) != 1 || !links[0].OutsideAvailability {
		t.Fatalf("links = %+v; want one link that offers its whole windows", links)
	}
	if again := approve(t, cs, ticket, key); !again.IsError {
		t.Fatal("a ticket approved twice")
	}
}

// An app that does not draw cards gets no ticket, and cannot use owner_approve at all.
func TestMCPCards_aTextAppCannotApprove(t *testing.T) {
	f := newOwnerFixture(t)
	f.closeDay(t, 3)
	cards := connectCardApp(t, f.h)
	res := callRaw(t, cards, "create_personal_link", offerArgs(3, "09:00", "11:00"))
	ticket, key := approvalOf(t, res)

	text := connectMCPInternal(t, f.h)
	if msg := toolError(t, text, "create_personal_link", offerArgs(3, "09:00", "11:00")); strings.Contains(msg, "ticket") {
		t.Errorf("a text app was handed an approval: %s", msg)
	}
	if msg := toolError(t, text, "owner_approve", map[string]any{"ticket": ticket, "key": key}); !strings.Contains(msg, "card") {
		t.Errorf("owner_approve from a text app: %s", msg)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM personal_links`); n != 0 {
		t.Fatalf("%d links made without a card tap", n)
	}
}

// A direct booking outside hours, approved with a tap on the card.
func TestMCPCards_aTapApprovesADirectBookingOutsideHours(t *testing.T) {
	f := newOwnerFixture(t)
	f.closeDay(t, 3)
	cs := connectCardApp(t, f.h)
	res := callRaw(t, cs, "book_for_guest", map[string]any{"guest_name": "Ann Agent", "guest_email": "ann@example.com", "event_type": "intro", "date": date(3), "time": "09:00"})
	ticket, key := approvalOf(t, res)
	if n := f.count(t, `SELECT COUNT(*) FROM bookings`); n != 0 {
		t.Fatalf("%d bookings before the tap", n)
	}
	if ok := approve(t, cs, ticket, key); ok.IsError {
		t.Fatalf("the owner's tap: %+v", ok.Content)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM bookings`); n != 1 {
		t.Fatalf("%d bookings after the tap; want 1", n)
	}
}

// A guest's suggestion over a calendar event: Book anyway on the card, and the event's
// title never appears in anything the app or model is given.
func TestMCPCards_aTapBooksASuggestionAnywayAndNamesNothing(t *testing.T) {
	f := newChatFixture(t)
	f.throughDetails(t)
	clash := f.at(21, 0)
	f.cal.mu.Lock()
	f.cal.busy = []slots.Interval{{Start: clash.Add(-30 * time.Minute), End: clash.Add(30 * time.Minute)}}
	f.cal.titled = []calendar.BusyEvent{{Title: "Dentist", Start: clash.Add(-30 * time.Minute), End: clash.Add(30 * time.Minute)}}
	f.cal.mu.Unlock()
	links := f.h.personalLinks()
	starts, _ := json.Marshal([]string{clash.UTC().Format(time.RFC3339)})
	if err := links.Hold(context.Background(), f.link.ID, personallink.HeldSuggestions, string(starts)); err != nil {
		t.Fatal(err)
	}
	items, _ := links.HeldItems(context.Background(), f.link.ID)
	cs := connectCardApp(t, f.h)
	res := callRaw(t, cs, "answer_guest", map[string]any{"link_id": f.link.ID, "held_id": items[len(items)-1].ID, "answer": "book", "at": clash.UTC().Format(time.RFC3339)})
	if blob, _ := json.Marshal(res); strings.Contains(string(blob), "Dentist") {
		t.Fatalf("a calendar title reached the app: %s", blob)
	}
	ticket, key := approvalOf(t, res)
	ok := approve(t, cs, ticket, key)
	if blob, _ := json.Marshal(ok); ok.IsError || strings.Contains(string(blob), "Dentist") {
		t.Fatalf("the owner's tap: %s", blob)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM bookings`); n != 1 {
		t.Fatalf("%d bookings after Book anyway; want 1", n)
	}
}
