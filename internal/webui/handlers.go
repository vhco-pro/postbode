package webui

import (
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/vhco-pro/postbode/internal/queue"
)

// listPageData is the top-level data passed to templates/index.html.
type listPageData struct {
	Token string
	Items []itemView
}

// handleList renders GET / — the F-43 list view. It shows every item
// currently needing a human decision: status=staged (queue.ListReviewable
// — F-43) plus status=suppressed_peppol (F-36, AC-14), which needs an
// explicit override before it can even reach the ordinary staged flow.
// Rejected/uploaded/etc. items have already had their decision made and
// are not part of the review queue's whole reason to exist.
func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	items, err := s.db.ListReviewable(ctx)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	data := listPageData{Token: s.token, Items: make([]itemView, 0, len(items))}
	for _, item := range items {
		data.Items = append(data.Items, s.buildItemView(ctx, item))
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageTemplate.ExecuteTemplate(w, "index.html", data); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

// handlePreview serves GET /preview/{id}: the item's PDF bytes streamed
// straight from its spool path. The id in the URL is only ever used to
// look the item up through internal/queue; the path actually opened
// (item.SpoolPath) comes entirely from the database, never from anything
// user-supplied, so no request can traverse outside the spool directory.
func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	item, err := s.db.GetItem(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if item.SpoolPath == "" {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(item.SpoolPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "application/pdf")
	_, _ = io.Copy(w, f)
}

// handleApprove handles POST /items/{id}/approve (F-43, AC-15). It
// additionally refuses (409) items flagged needs_manual_handling or
// unsupported_type, which internal/queue's own lifecycle graph does not
// know about — those items must never be approvable from the UI (AC-7),
// on top of not being uploadable once approved.
func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()

	item, err := s.db.GetItem(ctx, id)
	if err != nil {
		writeItemLookupError(w, err)
		return
	}
	if item.NeedsManualHandling || item.UnsupportedType {
		http.Error(w, "conflict: item requires manual handling and cannot be approved from the UI", http.StatusConflict)
		return
	}

	if err := s.db.Approve(ctx, id, queue.ActorHuman); err != nil {
		writeTransitionError(w, err)
		return
	}
	s.redirectHome(w, r)
}

// handleReject handles POST /items/{id}/reject (F-43).
func (s *Server) handleReject(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()

	if _, err := s.db.GetItem(ctx, id); err != nil {
		writeItemLookupError(w, err)
		return
	}
	if err := s.db.Reject(ctx, id, queue.ActorHuman); err != nil {
		writeTransitionError(w, err)
		return
	}
	s.redirectHome(w, r)
}

// handleAlreadyInPortal handles POST /items/{id}/already-in-portal
// (AC-13): it sets the item's status to already_in_portal and records a
// vendor_teaching row, atomically, and never calls the upload path at
// all — there is no upload call anywhere in this handler.
func (s *Server) handleAlreadyInPortal(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()

	item, err := s.db.GetItem(ctx, id)
	if err != nil {
		writeItemLookupError(w, err)
		return
	}

	domain := ""
	if msg, err := s.db.GetMessage(ctx, item.GmailMessageID); err == nil {
		domain = vendorDomain(msg.From)
	}
	if domain == "" {
		// Fall back to a per-item key so the teaching row can still be
		// written (vendor_domain is vendor_teaching's primary key and must
		// not be empty) — this only happens when the sender's domain
		// cannot be determined, which should not occur in production once
		// gmailwatch/rules are wired in, but must never crash the handler.
		domain = "unknown-vendor-item-" + strconv.FormatInt(id, 10)
	}

	teaching := queue.VendorTeaching{
		VendorDomain: domain,
		IdentityKey:  item.IdentityKey,
		Reason:       "marked already in portal by reviewer",
		MarkedAt:     time.Now().UTC(),
		Note:         r.FormValue("note"),
	}

	if err := s.db.MarkAlreadyInPortalWithTeaching(ctx, id, queue.ActorHuman, teaching); err != nil {
		writeTransitionError(w, err)
		return
	}
	s.redirectHome(w, r)
}

// handleOverridePeppol handles POST /items/{id}/override-peppol (F-36,
// AC-14): the explicit UI action required before a known-Peppol-vendor
// item can be approved at all. It only performs the
// suppressed_peppol -> staged transition; the human's next click is
// always the ordinary Approve button, never a hidden combined action.
func (s *Server) handleOverridePeppol(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()

	if _, err := s.db.GetItem(ctx, id); err != nil {
		writeItemLookupError(w, err)
		return
	}
	if err := s.db.OverridePeppolSuppression(ctx, id, queue.ActorHuman); err != nil {
		writeTransitionError(w, err)
		return
	}
	s.redirectHome(w, r)
}

// handleApproveAll handles POST /approve-all (F-43, F-34, spec §8): it
// approves every item currently visible in the review queue — status
// staged, minus the same needs_manual_handling/unsupported_type items
// handleApprove refuses individually — re-deriving that set fresh from the
// database rather than trusting anything the client posted, so a stale
// page cannot approve items outside what is actually visible right now.
// A single item's approval failing does not abort the batch: every
// approvable item gets its own independent attempt.
func (s *Server) handleApproveAll(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	items, err := s.db.ListByStatus(ctx, queue.StatusStaged)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	for _, item := range items {
		if !approvable(item) {
			continue
		}
		_ = s.db.Approve(ctx, item.ID, queue.ActorHuman)
	}
	s.redirectHome(w, r)
}

// redirectHome sends a 303 back to the list view, carrying the session
// token forward — a mutating request always arrives with a valid token
// (requireToken already checked), and the redirect target is itself a
// token-gated route, so an unauthenticated follow-up GET would otherwise
// immediately 401.
func (s *Server) redirectHome(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/?t="+url.QueryEscape(s.token), http.StatusSeeOther)
}

// parseID parses a path {id} segment as a positive item id.
func parseID(s string) (int64, bool) {
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// writeItemLookupError maps a GetItem error to the spec §6.3 error table:
// 404 when the item does not exist, 500 for anything else.
func writeItemLookupError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// writeTransitionError maps a lifecycle transition error to the spec §6.3
// error table: 409 when the F-41 graph forbids the requested move (e.g.
// approving something already approved), 404 when the item vanished
// between lookup and transition, 500 otherwise.
func writeTransitionError(w http.ResponseWriter, err error) {
	var invalid *queue.InvalidTransitionError
	if errors.As(err, &invalid) {
		http.Error(w, invalid.Error(), http.StatusConflict)
		return
	}
	http.Error(w, "internal error", http.StatusInternalServerError)
}
