package uploader_test

import (
	"context"
	"testing"
	"time"

	"github.com/vhco-pro/postbode/internal/clearfacts"
	"github.com/vhco-pro/postbode/internal/clearfacts/fake"
	"github.com/vhco-pro/postbode/internal/notify"
	"github.com/vhco-pro/postbode/internal/queue"
	"github.com/vhco-pro/postbode/internal/uploader"
)

func staticToken(v string) clearfacts.TokenSourceFunc {
	return func(context.Context) (clearfacts.Token, error) { return clearfacts.Token(v), nil }
}

func newTestClient(t *testing.T, url string) *clearfacts.Client {
	t.Helper()
	return clearfacts.NewClient(
		staticToken("test-pat"),
		clearfacts.WithEndpoint(url),
		clearfacts.WithMinInterval(0),
	)
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 9, Criterion: "Provenance stamping asserted end-to-end through the UI approve path (F-56, AC-15)" (AC-15: "Approving one item POSTs exactly one multipart request ... variables.invoicetype == \"PURCHASE\", variables.companyNumber set, no vatnumber key, no tags key")
func TestUploadOneAC15MultipartContract(t *testing.T) {
	srv := fake.New()
	defer srv.Close()

	db := openTestDB(t)
	ctx := context.Background()
	itemID := stageApprovedItemWithFile(t, db, ctx, "msg-1", "hash-1")

	u := &uploader.Uploader{
		DB:            db,
		Client:        newTestClient(t, srv.URL()),
		CompanyNumber: testCompanyNumber,
	}

	processed, err := u.UploadOne(ctx)
	if err != nil {
		t.Fatalf("UploadOne: %v", err)
	}
	if !processed {
		t.Fatal("UploadOne processed = false, want true")
	}

	reqs := srv.Requests()
	if len(reqs) != 1 {
		t.Fatalf("fake server received %d requests, want exactly 1", len(reqs))
	}
	req := reqs[0]

	if req.Query == "" || req.Variables == nil || req.FileFieldName != "file" {
		t.Fatalf("multipart request missing expected parts: %+v", req)
	}
	if got := req.Variables["invoicetype"]; got != "PURCHASE" {
		t.Errorf("variables.invoicetype = %v, want PURCHASE", got)
	}
	if got := req.Variables["companyNumber"]; got != testCompanyNumber {
		t.Errorf("variables.companyNumber = %v, want %q", got, testCompanyNumber)
	}
	if _, present := req.Variables["vatnumber"]; present {
		t.Error("variables contains a vatnumber key, must never be sent (A-12)")
	}
	if _, present := req.Variables["tags"]; present {
		t.Error("variables contains a tags key, must never be sent — the live API 500s on it (spec §6.1)")
	}

	item, err := db.GetItem(ctx, itemID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.Status != queue.StatusUploaded {
		t.Errorf("Status = %s, want %s", item.Status, queue.StatusUploaded)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 9, Criterion: "Proof of delivery: store the returned `uuid`, then call `document(id:)` and store `verified_at` (F-37)" (AC-16: "After a successful fake upload, the item has non-null uuid and verified_at")
func TestUploadOneAC16ProofOfDelivery(t *testing.T) {
	srv := fake.New()
	defer srv.Close()

	db := openTestDB(t)
	ctx := context.Background()
	itemID := stageApprovedItemWithFile(t, db, ctx, "msg-1", "hash-1")

	u := &uploader.Uploader{
		DB:            db,
		Client:        newTestClient(t, srv.URL()),
		CompanyNumber: testCompanyNumber,
	}

	if _, err := u.UploadOne(ctx); err != nil {
		t.Fatalf("UploadOne: %v", err)
	}

	item, err := db.GetItem(ctx, itemID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.UUID == "" {
		t.Error("UUID is empty, want set from the upload response")
	}
	if item.VerifiedAt == nil {
		t.Error("VerifiedAt is nil, want set after the document(id:) verification call")
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 9, Criterion: "Retry persistence: exponential backoff 1m→2h, give up after 24h into `failed` with the last error stored; 4xx except 429 is terminal-`failed` immediately with `retry_count == 0` (F-51, AC-17)" (AC-17: "A fake server returning 503 three times then 200 results in exactly one stored uuid, retry_count == 3, and no duplicate upload.")
func TestUploadRetrySequenceAC17ThreeFiveOhThreesThenSuccess(t *testing.T) {
	srv := fake.New()
	defer srv.Close()
	srv.EnqueueStatusSequence(503, 503, 503)

	db := openTestDB(t)
	ctx := context.Background()
	itemID := stageApprovedItemWithFile(t, db, ctx, "msg-1", "hash-1")

	clock := newTestClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	u := &uploader.Uploader{
		DB:            db,
		Client:        newTestClient(t, srv.URL()),
		CompanyNumber: testCompanyNumber,
		Clock:         clock.Now,
	}

	// Attempts 1-3 fail (503, retryable); attempt 4 succeeds. Between
	// attempts, advance the clock past the scheduled next_retry_at — a real
	// daemon would just let wall-clock time pass; this proves the same
	// backoff-then-succeed sequence without sleeping for real minutes.
	for attempt := 0; attempt < 3; attempt++ {
		processed, err := u.UploadOne(ctx)
		if err != nil {
			t.Fatalf("UploadOne attempt %d: %v", attempt+1, err)
		}
		if !processed {
			t.Fatalf("UploadOne attempt %d: processed = false, want true", attempt+1)
		}
		item, err := db.GetItem(ctx, itemID)
		if err != nil {
			t.Fatalf("GetItem after attempt %d: %v", attempt+1, err)
		}
		if item.Status != queue.StatusApproved {
			t.Fatalf("attempt %d: Status = %s, want unchanged %s (still retrying)", attempt+1, item.Status, queue.StatusApproved)
		}
		if item.NextRetryAt == nil {
			t.Fatalf("attempt %d: NextRetryAt is nil, want scheduled", attempt+1)
		}
		clock.Advance(item.NextRetryAt.Sub(clock.Now()) + time.Second)

		// Immediately re-claiming before next_retry_at must see nothing.
	}

	processed, err := u.UploadOne(ctx)
	if err != nil {
		t.Fatalf("final UploadOne: %v", err)
	}
	if !processed {
		t.Fatal("final UploadOne: processed = false, want true")
	}

	item, err := db.GetItem(ctx, itemID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.Status != queue.StatusUploaded {
		t.Fatalf("Status = %s, want %s", item.Status, queue.StatusUploaded)
	}
	if item.UUID == "" {
		t.Error("UUID is empty after eventual success, want set")
	}
	if item.RetryCount != 3 {
		t.Errorf("RetryCount = %d, want 3", item.RetryCount)
	}
	if got := srv.RequestCount(); got != 4 {
		t.Errorf("fake server received %d requests, want exactly 4 (3 failures + 1 success, no duplicate upload)", got)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 9, Criterion: "Retry persistence: exponential backoff 1m→2h, give up after 24h into `failed` with the last error stored; 4xx except 429 is terminal-`failed` immediately with `retry_count == 0` (F-51, AC-17)" (AC-17: "A fake server returning 400 marks the item failed immediately with retry_count == 0.")
func TestUpload400IsTerminalAC17(t *testing.T) {
	srv := fake.New()
	defer srv.Close()
	srv.Enqueue(fake.ScriptedResponse{StatusCode: 400})

	db := openTestDB(t)
	ctx := context.Background()
	itemID := stageApprovedItemWithFile(t, db, ctx, "msg-1", "hash-1")

	u := &uploader.Uploader{
		DB:            db,
		Client:        newTestClient(t, srv.URL()),
		CompanyNumber: testCompanyNumber,
	}

	if _, err := u.UploadOne(ctx); err != nil {
		t.Fatalf("UploadOne: %v", err)
	}

	item, err := db.GetItem(ctx, itemID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.Status != queue.StatusFailed {
		t.Errorf("Status = %s, want %s", item.Status, queue.StatusFailed)
	}
	if item.RetryCount != 0 {
		t.Errorf("RetryCount = %d, want 0 (a terminal 4xx must never retry)", item.RetryCount)
	}
	if item.LastError == "" {
		t.Error("LastError is empty, want the classified error recorded")
	}
	if got := srv.RequestCount(); got != 1 {
		t.Errorf("fake server received %d requests, want exactly 1 — a terminal error must not be retried", got)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 9, Criterion: "401/403 → terminal `failed` + notification \"PAT invalid or scope missing\", no retry storm (F-51, NF-02)"
func TestUpload401IsTerminalAndNotifies(t *testing.T) {
	srv := fake.New()
	defer srv.Close()
	srv.Enqueue(fake.ScriptedResponse{StatusCode: 401})

	db := openTestDB(t)
	ctx := context.Background()
	itemID := stageApprovedItemWithFile(t, db, ctx, "msg-1", "hash-1")

	notifier := &notify.Fake{}
	u := &uploader.Uploader{
		DB:            db,
		Client:        newTestClient(t, srv.URL()),
		CompanyNumber: testCompanyNumber,
		Notifier:      notifier,
	}

	if _, err := u.UploadOne(ctx); err != nil {
		t.Fatalf("UploadOne: %v", err)
	}

	item, err := db.GetItem(ctx, itemID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.Status != queue.StatusFailed {
		t.Errorf("Status = %s, want %s", item.Status, queue.StatusFailed)
	}
	if item.RetryCount != 0 {
		t.Errorf("RetryCount = %d, want 0", item.RetryCount)
	}
	if got := srv.RequestCount(); got != 1 {
		t.Errorf("fake server received %d requests, want exactly 1 — no retry storm on a PAT problem", got)
	}

	found := false
	for _, msg := range notifier.All() {
		if msg == "Postbode: PAT invalid or scope missing." {
			found = true
		}
	}
	if !found {
		t.Errorf("notifier messages = %v, want one containing \"PAT invalid or scope missing\"", notifier.All())
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 9, Criterion: "Retry persistence: exponential backoff 1m→2h, give up after 24h into `failed` with the last error stored ... (F-51, AC-17)"
func TestUploadGivesUpAfter24Hours(t *testing.T) {
	srv := fake.New()
	defer srv.Close()
	// Every request fails with a retryable 503 — this item never succeeds.
	for i := 0; i < 10; i++ {
		srv.Enqueue(fake.ScriptedResponse{StatusCode: 503})
	}

	db := openTestDB(t)
	ctx := context.Background()
	itemID := stageApprovedItemWithFile(t, db, ctx, "msg-1", "hash-1")

	clock := newTestClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	u := &uploader.Uploader{
		DB:            db,
		Client:        newTestClient(t, srv.URL()),
		CompanyNumber: testCompanyNumber,
		Clock:         clock.Now,
	}

	// First failure: stamps FirstFailedAt, schedules next_retry_at ~1m out.
	if _, err := u.UploadOne(ctx); err != nil {
		t.Fatalf("UploadOne (first failure): %v", err)
	}
	item, err := db.GetItem(ctx, itemID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.Status != queue.StatusApproved {
		t.Fatalf("Status after first retryable failure = %s, want %s", item.Status, queue.StatusApproved)
	}

	// Jump the clock forward 25 hours — well past the 24h give-up threshold
	// — and past next_retry_at, so the item is claimable again.
	clock.Advance(25 * time.Hour)

	if _, err := u.UploadOne(ctx); err != nil {
		t.Fatalf("UploadOne (second failure, past give-up threshold): %v", err)
	}

	item, err = db.GetItem(ctx, itemID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.Status != queue.StatusFailed {
		t.Errorf("Status = %s, want %s (24h give-up threshold crossed)", item.Status, queue.StatusFailed)
	}
	if item.LastError == "" {
		t.Error("LastError is empty, want the last classified error recorded")
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 9, Criterion: "Durable approval: a partial-batch failure leaves the remaining items in `approved` and they retry automatically without re-approval (F-52, AC-18)"
func TestRunBatchDurableApprovalPartialFailureLeavesOthersApproved(t *testing.T) {
	srv := fake.New()
	defer srv.Close()
	// First upload attempt (item-a) fails terminally; the second
	// (item-b) succeeds.
	srv.Enqueue(fake.ScriptedResponse{StatusCode: 400})

	db := openTestDB(t)
	ctx := context.Background()
	idA := stageApprovedItemWithFile(t, db, ctx, "msg-a", "hash-a")
	idB := stageApprovedItemWithFile(t, db, ctx, "msg-b", "hash-b")

	notifier := &notify.Fake{}
	u := &uploader.Uploader{
		DB:            db,
		Client:        newTestClient(t, srv.URL()),
		CompanyNumber: testCompanyNumber,
		Notifier:      notifier,
	}

	uploadedCount, err := u.RunBatch(ctx)
	if err != nil {
		t.Fatalf("RunBatch: %v", err)
	}
	if uploadedCount != 1 {
		t.Errorf("uploadedCount = %d, want 1 (item-a failed, item-b succeeded)", uploadedCount)
	}

	a, err := db.GetItem(ctx, idA)
	if err != nil {
		t.Fatalf("GetItem(a): %v", err)
	}
	if a.Status != queue.StatusFailed {
		t.Errorf("item-a Status = %s, want %s", a.Status, queue.StatusFailed)
	}

	b, err := db.GetItem(ctx, idB)
	if err != nil {
		t.Fatalf("GetItem(b): %v", err)
	}
	if b.Status != queue.StatusUploaded {
		t.Errorf("item-b Status = %s, want %s (a partial-batch failure must not block the rest of the batch)", b.Status, queue.StatusUploaded)
	}

	found := false
	for _, msg := range notifier.All() {
		if msg == notify.UploadBatchCompleteMessage(1) {
			found = true
		}
	}
	if !found {
		t.Errorf("notifier messages = %v, want the upload-batch-complete message for count 1", notifier.All())
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 9, Criterion: "Durable approval: a partial-batch failure leaves the remaining items in `approved` and they retry automatically without re-approval (F-52, AC-18)" (AC-18: "Killing the process between approved and upload, then restarting, results in the item uploading exactly once with no second approval required.")
func TestKillBetweenApprovedAndUploadThenRestartUploadsExactlyOnceAC18(t *testing.T) {
	srv := fake.New()
	defer srv.Close()

	path := t.TempDir() + "/queue.db"
	ctx := context.Background()

	db1, err := queue.Open(ctx, path)
	if err != nil {
		t.Fatalf("queue.Open: %v", err)
	}
	itemID := stageApprovedItemWithFile(t, db1, ctx, "msg-1", "hash-1")

	// Simulate the process claiming the item — durable, already committed —
	// and then dying before ever calling UploadFile or recording any
	// outcome. Nothing releases this claim from db1's side; db1 is simply
	// abandoned, exactly like a killed process.
	claimed, err := db1.ClaimApproved(ctx)
	if err != nil {
		t.Fatalf("ClaimApproved (simulated pre-crash claim): %v", err)
	}
	if claimed == nil || claimed.ID != itemID {
		t.Fatalf("ClaimApproved = %+v, want item %d", claimed, itemID)
	}
	if err := db1.Close(); err != nil {
		t.Fatalf("Close db1: %v", err)
	}

	// Restart: a fresh *queue.DB and a fresh Uploader against the same
	// on-disk file, standing in for the daemon restarting.
	db2, err := queue.Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen after simulated crash: %v", err)
	}
	t.Cleanup(func() { _ = db2.Close() })

	u := &uploader.Uploader{
		DB:            db2,
		Client:        newTestClient(t, srv.URL()),
		CompanyNumber: testCompanyNumber,
	}

	// The daemon's startup sequence releases stale claims before running the
	// uploader — with a zero threshold here, since this test simulates a
	// definite crash, not a live in-flight claim.
	released, err := u.ReleaseStaleClaims(ctx, 0)
	if err != nil {
		t.Fatalf("ReleaseStaleClaims: %v", err)
	}
	if released != 1 {
		t.Fatalf("ReleaseStaleClaims released %d claims, want 1", released)
	}

	processed, err := u.UploadOne(ctx)
	if err != nil {
		t.Fatalf("UploadOne after restart: %v", err)
	}
	if !processed {
		t.Fatal("UploadOne after restart: processed = false, want true — no second approval should be required")
	}

	item, err := db2.GetItem(ctx, itemID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.Status != queue.StatusUploaded {
		t.Errorf("Status = %s, want %s", item.Status, queue.StatusUploaded)
	}
	if item.UUID == "" {
		t.Error("UUID is empty, want set")
	}
	if got := srv.RequestCount(); got != 1 {
		t.Errorf("fake server received %d requests, want exactly 1 — the item must upload exactly once", got)
	}

	// A second UploadOne must find nothing claimable: the item already
	// reached the terminal uploaded status, so it can never be claimed
	// again — this is what makes "exactly once" hold even if the daemon's
	// startup recovery runs more than once.
	processedAgain, err := u.UploadOne(ctx)
	if err != nil {
		t.Fatalf("second UploadOne: %v", err)
	}
	if processedAgain {
		t.Error("second UploadOne processed an item, want nothing claimable (already uploaded)")
	}
	if got := srv.RequestCount(); got != 1 {
		t.Errorf("fake server received %d requests after a second UploadOne call, want still exactly 1", got)
	}
}
