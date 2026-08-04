package webui

import (
	"context"
	"fmt"
	"strings"

	"github.com/vhco-pro/postbode/internal/queue"
)

// badge is one F-47 flag badge rendered next to an item, with a
// human-readable reason where one is available. Badges are informational
// only — see doc.go and CLAUDE.md: the UI must never auto-suppress or
// auto-reject an item because a badge is present.
type badge struct {
	Label  string
	Detail string
}

// itemView is everything the index template needs to render one row: the
// underlying queue.Item, the message it came from, its badges and whether
// the Approve action is currently offered.
type itemView struct {
	Item       *queue.Item
	Sender     string
	Subject    string
	Badges     []badge
	Approvable bool
}

// approvable reports whether item may be approved from the UI (AC-7): it
// must be staged, and must carry neither needs_manual_handling nor
// unsupported_type — both of those require a human to handle the document
// outside Postbode entirely, so an Approve button would be actively wrong.
func approvable(item *queue.Item) bool {
	return item.Status == queue.StatusStaged && !item.NeedsManualHandling && !item.UnsupportedType
}

// buildItemView assembles one itemView for item, looking up its message
// (for sender/subject) and, where a flag is set, the supporting detail for
// its badge (F-47). Lookup failures degrade to an empty detail rather than
// failing the whole page — a missing cross-reference must never hide an
// item from review.
func (s *Server) buildItemView(ctx context.Context, item *queue.Item) itemView {
	v := itemView{
		Item:       item,
		Approvable: approvable(item),
	}

	if msg, err := s.db.GetMessage(ctx, item.GmailMessageID); err == nil {
		v.Sender = msg.From
		v.Subject = msg.Subject
	}

	if item.NeedsManualHandling {
		v.Badges = append(v.Badges, badge{Label: "needs manual handling"})
	}
	if item.UnsupportedType {
		v.Badges = append(v.Badges, badge{Label: "unsupported type"})
	}
	if item.LowConfidence {
		v.Badges = append(v.Badges, badge{Label: "low confidence"})
	}
	if item.PossibleDuplicate {
		v.Badges = append(v.Badges, badge{Label: "possible duplicate", Detail: s.duplicateDetail(ctx, item)})
	}
	if item.ProbablyAlreadyHandled {
		v.Badges = append(v.Badges, badge{Label: "probably already handled", Detail: s.teachingDetail(ctx, item)})
	}

	return v
}

// duplicateDetail renders the "possible duplicate of #N (status: X, uuid:
// Y, staged: Z)" detail for a possible_duplicate badge, from the item it
// is linked to. Returns a generic fallback if the link can't be resolved
// (LinkedItemID unset, or the linked item is gone) — the badge itself
// still renders either way, since suppressing the badge on a lookup
// failure would hide the exact signal it exists to surface.
func (s *Server) duplicateDetail(ctx context.Context, item *queue.Item) string {
	if item.LinkedItemID == nil {
		return "no linked item recorded"
	}
	linked, err := s.db.GetItem(ctx, *item.LinkedItemID)
	if err != nil {
		return fmt.Sprintf("of #%d (details unavailable)", *item.LinkedItemID)
	}
	uuid := linked.UUID
	if uuid == "" {
		uuid = "none yet"
	}
	return fmt.Sprintf("of #%d — status: %s, uuid: %s, staged: %s",
		linked.ID, linked.Status, uuid, linked.StagedAt.Format("2006-01-02 15:04 MST"))
}

// teachingDetail renders the F-35 "reason and teaching date" detail for a
// probably_already_handled badge, looked up by the item's identity_key
// against the vendor_teaching row an earlier "already in portal" action
// recorded (AC-13). The L4 engine that sets ProbablyAlreadyHandled at
// staging time is Phase 12's job — this only supports rendering it.
func (s *Server) teachingDetail(ctx context.Context, item *queue.Item) string {
	vt, err := s.db.GetVendorTeachingByIdentityKey(ctx, item.IdentityKey)
	if err != nil {
		return "reason unavailable"
	}
	note := ""
	if vt.Note != "" {
		note = " — " + vt.Note
	}
	return fmt.Sprintf("%s (taught %s, vendor %s)%s",
		vt.Reason, vt.MarkedAt.Format("2006-01-02"), vt.VendorDomain, note)
}

// vendorDomain extracts the domain portion of an RFC 5322 "From" header
// value such as `Name <billing@example.com>` or a bare address, lower-cased.
// Used only to derive vendor_teaching's primary key when recording an
// "already in portal" action (F-34) — never used to route a spool file
// path, so it carries no path-traversal risk.
func vendorDomain(from string) string {
	addr := from
	if i := strings.LastIndexByte(from, '<'); i != -1 {
		if j := strings.IndexByte(from[i:], '>'); j != -1 {
			addr = from[i+1 : i+j]
		}
	}
	at := strings.LastIndexByte(addr, '@')
	if at == -1 || at == len(addr)-1 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(addr[at+1:]))
}
