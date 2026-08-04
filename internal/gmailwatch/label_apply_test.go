package gmailwatch_test

import (
	"context"
	"testing"

	"google.golang.org/api/gmail/v1"

	"github.com/vhco-pro/postbode/internal/gmailwatch"
	"github.com/vhco-pro/postbode/internal/gmailwatch/fake"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 7, Criterion: "AC-19/AC-20 are Phase 9's (label move) — implement `ApplyLabel`/`messages.modify` as a callable primitive here but do NOT wire the "all docs uploaded" trigger; that is the uploader's."
func TestApplyLabelIssuesExactlyOneModifyCallAddingLabelAndRemovingInbox(t *testing.T) {
	ctx := context.Background()

	gmailSrv := fake.NewServer()
	defer gmailSrv.Close()

	db := openTestDB(t)
	svc := gmailServiceFor(t, gmailSrv)
	w := newTestWatcher(t, svc, db)

	if err := w.ApplyLabel(ctx, "msg-1", "Label_42"); err != nil {
		t.Fatalf("ApplyLabel: %v", err)
	}

	calls := gmailSrv.Calls()
	if len(calls) != 1 {
		t.Fatalf("ApplyLabel issued %d modify call(s), want exactly 1", len(calls))
	}
	call := calls[0]
	if call.MessageID != "msg-1" {
		t.Errorf("MessageID = %q, want %q", call.MessageID, "msg-1")
	}
	if got := call.Request.AddLabelIds; len(got) != 1 || got[0] != "Label_42" {
		t.Errorf("AddLabelIds = %v, want [Label_42]", got)
	}
	if got := call.Request.RemoveLabelIds; len(got) != 1 || got[0] != "INBOX" {
		t.Errorf("RemoveLabelIds = %v, want [INBOX]", got)
	}
}

func TestApplyLabelRejectsEmptyLabelID(t *testing.T) {
	ctx := context.Background()
	gmailSrv := fake.NewServer()
	defer gmailSrv.Close()

	db := openTestDB(t)
	svc := gmailServiceFor(t, gmailSrv)
	w := newTestWatcher(t, svc, db)

	if err := w.ApplyLabel(ctx, "msg-1", ""); err == nil {
		t.Error("ApplyLabel with empty labelID: want an error, got nil")
	}
	if len(gmailSrv.Calls()) != 0 {
		t.Error("ApplyLabel with empty labelID must not issue a modify call")
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 7, Criterion: "Label resolution at startup (F-15, amended v1.3 + v1.6): name is `VH&Co/submitted`. ... `ResolveLabel` already implements this — use it, don't reimplement."
func TestResolveAndPersistSubmittedLabelPersistsIntoSyncState(t *testing.T) {
	ctx := context.Background()

	gmailSrv := fake.NewServer()
	defer gmailSrv.Close()
	gmailSrv.Labels = []*gmail.Label{{Id: "Label_99", Name: gmailwatch.SubmittedLabelName}}

	db := openTestDB(t)
	svc := gmailServiceFor(t, gmailSrv)
	w := newTestWatcher(t, svc, db)

	if err := w.ResolveAndPersistSubmittedLabel(ctx); err != nil {
		t.Fatalf("ResolveAndPersistSubmittedLabel: %v", err)
	}

	st, err := db.GetSyncState(ctx)
	if err != nil {
		t.Fatalf("GetSyncState: %v", err)
	}
	if st.LabelIDSubmitted != "Label_99" {
		t.Errorf("sync_state.LabelIDSubmitted = %q, want %q", st.LabelIDSubmitted, "Label_99")
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 7, Criterion: "Only an *authenticated* `labels.list` returning without the name counts as absent → fail loudly, refuse to start the uploader (the watcher may still poll and stage). An auth error / network failure / pending re-auth is **NOT** absent and must not trigger the refusal path."
func TestResolveAndPersistSubmittedLabelReturnsErrLabelNotFoundWhenAbsent(t *testing.T) {
	ctx := context.Background()

	gmailSrv := fake.NewServer()
	defer gmailSrv.Close()
	gmailSrv.Labels = nil // authenticated, successful list — simply no such label

	db := openTestDB(t)
	svc := gmailServiceFor(t, gmailSrv)
	w := newTestWatcher(t, svc, db)

	err := w.ResolveAndPersistSubmittedLabel(ctx)
	if err != gmailwatch.ErrLabelNotFound {
		t.Errorf("ResolveAndPersistSubmittedLabel err = %v, want gmailwatch.ErrLabelNotFound", err)
	}

	st, gerr := db.GetSyncState(ctx)
	if gerr != nil {
		t.Fatalf("GetSyncState: %v", gerr)
	}
	if st.LabelIDSubmitted != "" {
		t.Errorf("sync_state.LabelIDSubmitted = %q, want empty (never persisted on refusal)", st.LabelIDSubmitted)
	}
}
