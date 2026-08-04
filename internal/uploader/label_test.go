package uploader_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/vhco-pro/postbode/internal/clearfacts/fake"
	"github.com/vhco-pro/postbode/internal/gmailwatch"
	gmailfake "github.com/vhco-pro/postbode/internal/gmailwatch/fake"
	"github.com/vhco-pro/postbode/internal/queue"
	"github.com/vhco-pro/postbode/internal/uploader"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

const testLabelID = "Label_submitted"

func gmailWatcherFor(t *testing.T, gmailSrv *gmailfake.Server) *gmailwatch.Watcher {
	t.Helper()
	svc, err := gmail.NewService(context.Background(),
		option.WithEndpoint(gmailSrv.URL),
		option.WithHTTPClient(http.DefaultClient),
		option.WithoutAuthentication(),
	)
	if err != nil {
		t.Fatalf("gmail.NewService: %v", err)
	}
	return &gmailwatch.Watcher{Service: svc, UserID: "me"}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 9, Criterion: "Label application (F-14, AC-19): after **all** documents extracted from a message reach terminal `uploaded`, issue exactly one `messages.modify` adding `VH&Co/submitted` and removing `INBOX`. Never modify a message with a non-terminal document." (AC-19: "A message with 2 PDFs where only 1 uploads successfully results in no messages.modify call; once the second succeeds, exactly one modify call is issued adding the submitted label and removing INBOX.")
func TestLabelAppliedOnlyOnceAllDocumentsUploadedAC19(t *testing.T) {
	cfSrv := fake.New()
	defer cfSrv.Close()
	gmailSrv := gmailfake.NewServer()
	defer gmailSrv.Close()

	db := openTestDB(t)
	ctx := context.Background()

	if err := db.SaveSyncState(ctx, queue.SyncState{LabelIDSubmitted: testLabelID}); err != nil {
		t.Fatalf("SaveSyncState: %v", err)
	}

	idA := stageApprovedItemWithFile(t, db, ctx, "msg-1", "hash-a")
	idB := stageApprovedItemWithFile(t, db, ctx, "msg-1", "hash-b")

	watcher := gmailWatcherFor(t, gmailSrv)
	u := &uploader.Uploader{
		DB:            db,
		Client:        newTestClient(t, cfSrv.URL()),
		CompanyNumber: testCompanyNumber,
		LabelApplier:  watcher,
	}

	// Upload item A only. Item B is still approved (not yet uploaded) — no
	// modify call must be issued.
	processed, err := u.UploadOne(ctx)
	if err != nil {
		t.Fatalf("UploadOne (item A): %v", err)
	}
	if !processed {
		t.Fatal("UploadOne (item A): processed = false, want true")
	}
	a, err := db.GetItem(ctx, idA)
	if err != nil {
		t.Fatalf("GetItem(a): %v", err)
	}
	if a.Status != queue.StatusUploaded {
		t.Fatalf("item A Status = %s, want %s", a.Status, queue.StatusUploaded)
	}
	if calls := gmailSrv.Calls(); len(calls) != 0 {
		t.Fatalf("modify calls after only item A uploaded = %d, want 0 (item B is not yet terminal-uploaded)", len(calls))
	}

	// Upload item B. Now every document belonging to msg-1 is uploaded —
	// exactly one modify call must follow.
	processed, err = u.UploadOne(ctx)
	if err != nil {
		t.Fatalf("UploadOne (item B): %v", err)
	}
	if !processed {
		t.Fatal("UploadOne (item B): processed = false, want true")
	}
	b, err := db.GetItem(ctx, idB)
	if err != nil {
		t.Fatalf("GetItem(b): %v", err)
	}
	if b.Status != queue.StatusUploaded {
		t.Fatalf("item B Status = %s, want %s", b.Status, queue.StatusUploaded)
	}

	calls := gmailSrv.Calls()
	if len(calls) != 1 {
		t.Fatalf("modify calls after both items uploaded = %d, want exactly 1", len(calls))
	}
	call := calls[0]
	if call.MessageID != "msg-1" {
		t.Errorf("MessageID = %q, want %q", call.MessageID, "msg-1")
	}
	if got := call.Request.AddLabelIds; len(got) != 1 || got[0] != testLabelID {
		t.Errorf("AddLabelIds = %v, want [%s]", got, testLabelID)
	}
	if got := call.Request.RemoveLabelIds; len(got) != 1 || got[0] != "INBOX" {
		t.Errorf("RemoveLabelIds = %v, want [INBOX]", got)
	}

	msg, err := db.GetMessage(ctx, "msg-1")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if msg.LabeledAt == nil {
		t.Error("message.LabeledAt is nil, want set")
	}
	if msg.AllDocsUploadedAt == nil {
		t.Error("message.AllDocsUploadedAt is nil, want set")
	}

	// A third UploadOne (nothing left claimable) must not issue a second
	// modify call — MarkMessageSubmitted's labeled_at check makes the whole
	// label move idempotent.
	if processed, err := u.UploadOne(ctx); err != nil {
		t.Fatalf("UploadOne (nothing left): %v", err)
	} else if processed {
		t.Error("UploadOne processed an item when nothing was claimable")
	}
	if calls := gmailSrv.Calls(); len(calls) != 1 {
		t.Errorf("modify calls after a redundant UploadOne = %d, want still exactly 1", len(calls))
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 9, Criterion: "Label application (F-14, AC-19): after **all** documents extracted from a message reach terminal `uploaded`, issue exactly one `messages.modify` ... Never modify a message with a non-terminal document." (task: "A message with 3 documents where 2 uploaded and 1 failed must not be labelled.")
func TestLabelNeverAppliedWhenOneDocumentPermanentlyFails(t *testing.T) {
	cfSrv := fake.New()
	defer cfSrv.Close()

	gmailSrv := gmailfake.NewServer()
	defer gmailSrv.Close()

	db := openTestDB(t)
	ctx := context.Background()
	if err := db.SaveSyncState(ctx, queue.SyncState{LabelIDSubmitted: testLabelID}); err != nil {
		t.Fatalf("SaveSyncState: %v", err)
	}

	// Claimed in staged order: A, B, then C (F-53/ClaimApprovedDue orders by
	// staged_at, id) — staging them in this order and driving UploadOne
	// three times, enqueueing the terminal 400 only right before the third
	// call, deterministically makes item C the one that fails.
	idA := stageApprovedItemWithFile(t, db, ctx, "msg-1", "hash-a")
	idB := stageApprovedItemWithFile(t, db, ctx, "msg-1", "hash-b")
	idC := stageApprovedItemWithFile(t, db, ctx, "msg-1", "hash-c")

	watcher := gmailWatcherFor(t, gmailSrv)
	u := &uploader.Uploader{
		DB:            db,
		Client:        newTestClient(t, cfSrv.URL()),
		CompanyNumber: testCompanyNumber,
		LabelApplier:  watcher,
	}

	if processed, err := u.UploadOne(ctx); err != nil || !processed {
		t.Fatalf("UploadOne (item A): processed=%v err=%v", processed, err)
	}
	if processed, err := u.UploadOne(ctx); err != nil || !processed {
		t.Fatalf("UploadOne (item B): processed=%v err=%v", processed, err)
	}
	cfSrv.Enqueue(fake.ScriptedResponse{StatusCode: 400})
	if processed, err := u.UploadOne(ctx); err != nil || !processed {
		t.Fatalf("UploadOne (item C): processed=%v err=%v", processed, err)
	}

	for _, id := range []int64{idA, idB} {
		item, err := db.GetItem(ctx, id)
		if err != nil {
			t.Fatalf("GetItem(%d): %v", id, err)
		}
		if item.Status != queue.StatusUploaded {
			t.Errorf("item %d Status = %s, want %s", id, item.Status, queue.StatusUploaded)
		}
	}
	c, err := db.GetItem(ctx, idC)
	if err != nil {
		t.Fatalf("GetItem(c): %v", err)
	}
	if c.Status != queue.StatusFailed {
		t.Fatalf("item C Status = %s, want %s", c.Status, queue.StatusFailed)
	}

	if calls := gmailSrv.Calls(); len(calls) != 0 {
		t.Errorf("modify calls = %d, want 0 — one document permanently failed, the message must never be labelled", len(calls))
	}
	msg, err := db.GetMessage(ctx, "msg-1")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if msg.LabeledAt != nil {
		t.Error("message.LabeledAt is set, want nil — the message was never fully uploaded")
	}
}
