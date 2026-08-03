package queue_test

import (
	"context"
	"errors"
	"testing"

	"github.com/vhco-pro/postbode/internal/queue"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 4, Criterion: "Lifecycle `staged → approved → uploaded` with terminals `rejected`, `already_in_portal`, `failed`, `suppressed_peppol`, plus `duplicate_linked` (see Design correction / OQ-P1); every transition logged with timestamp and actor `human`/`daemon` (F-41)"
func TestLifecycleHappyPathStagedApprovedUploaded(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	stageMessage(t, db, ctx, "msg-1")
	id := stageItem(t, db, ctx, "msg-1", "hash-a")

	if err := db.Approve(ctx, id, queue.ActorHuman); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if err := db.MarkUploaded(ctx, id, "uuid-1", 3); err != nil {
		t.Fatalf("MarkUploaded: %v", err)
	}

	item, err := db.GetItem(ctx, id)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.Status != queue.StatusUploaded {
		t.Errorf("Status = %s, want %s", item.Status, queue.StatusUploaded)
	}
	if item.UUID != "uuid-1" {
		t.Errorf("UUID = %q, want %q", item.UUID, "uuid-1")
	}
	if item.AmountOfPages != 3 {
		t.Errorf("AmountOfPages = %d, want 3", item.AmountOfPages)
	}
	if item.ApprovedAt == nil {
		t.Error("ApprovedAt is nil, want set")
	}
	if item.UploadedAt == nil {
		t.Error("UploadedAt is nil, want set")
	}

	transitions, err := db.Transitions(ctx, id)
	if err != nil {
		t.Fatalf("Transitions: %v", err)
	}
	wantSequence := []queue.Status{queue.StatusStaged, queue.StatusApproved, queue.StatusUploaded}
	if len(transitions) != len(wantSequence) {
		t.Fatalf("got %d transitions, want %d: %+v", len(transitions), len(wantSequence), transitions)
	}
	for i, want := range wantSequence {
		if transitions[i].To != want {
			t.Errorf("transitions[%d].To = %s, want %s", i, transitions[i].To, want)
		}
		if transitions[i].At.IsZero() {
			t.Errorf("transitions[%d].At is zero, want a timestamp", i)
		}
	}
	if transitions[0].From != "" {
		t.Errorf("transitions[0].From = %q, want empty (initial staging)", transitions[0].From)
	}
	if transitions[0].Actor != queue.ActorDaemon {
		t.Errorf("transitions[0].Actor = %s, want %s (StageItem always logs as daemon)", transitions[0].Actor, queue.ActorDaemon)
	}
	if transitions[1].Actor != queue.ActorHuman {
		t.Errorf("transitions[1].Actor = %s, want %s (Approve was called with ActorHuman)", transitions[1].Actor, queue.ActorHuman)
	}
	if transitions[2].Actor != queue.ActorDaemon {
		t.Errorf("transitions[2].Actor = %s, want %s (MarkUploaded always logs as daemon)", transitions[2].Actor, queue.ActorDaemon)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 4, Criterion: "Lifecycle `staged → approved → uploaded` with terminals `rejected`, `already_in_portal`, `failed`, `suppressed_peppol`, plus `duplicate_linked` (see Design correction / OQ-P1); every transition logged with timestamp and actor `human`/`daemon` (F-41)"
func TestLifecycleReachesEachTerminalStatus(t *testing.T) {
	tests := []struct {
		name string
		run  func(ctx context.Context, db *queue.DB, id int64) error
		want queue.Status
	}{
		{
			name: "rejected",
			run:  func(ctx context.Context, db *queue.DB, id int64) error { return db.Reject(ctx, id, queue.ActorHuman) },
			want: queue.StatusRejected,
		},
		{
			name: "already_in_portal",
			run: func(ctx context.Context, db *queue.DB, id int64) error {
				return db.MarkAlreadyInPortal(ctx, id, queue.ActorHuman)
			},
			want: queue.StatusAlreadyInPortal,
		},
		{
			name: "failed",
			run: func(ctx context.Context, db *queue.DB, id int64) error {
				if err := db.Approve(ctx, id, queue.ActorHuman); err != nil {
					return err
				}
				return db.MarkFailed(ctx, id, "503 after giving up")
			},
			want: queue.StatusFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openTestDB(t)
			ctx := context.Background()
			stageMessage(t, db, ctx, "msg-"+tt.name)
			id := stageItem(t, db, ctx, "msg-"+tt.name, "hash-"+tt.name)

			if err := tt.run(ctx, db, id); err != nil {
				t.Fatalf("%s: %v", tt.name, err)
			}

			item, err := db.GetItem(ctx, id)
			if err != nil {
				t.Fatalf("GetItem: %v", err)
			}
			if item.Status != tt.want {
				t.Errorf("Status = %s, want %s", item.Status, tt.want)
			}
		})
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 4, Criterion: "Lifecycle `staged → approved → uploaded` with terminals `rejected`, `already_in_portal`, `failed`, `suppressed_peppol`, plus `duplicate_linked` (see Design correction / OQ-P1); every transition logged with timestamp and actor `human`/`daemon` (F-41)"
func TestInvalidTransitionsAreRejectedNotSilentlyApplied(t *testing.T) {
	tests := []struct {
		name string
		run  func(ctx context.Context, db *queue.DB, id int64) error
	}{
		{"staged directly to uploaded", func(ctx context.Context, db *queue.DB, id int64) error {
			return db.MarkUploaded(ctx, id, "uuid-x", 1)
		}},
		{"staged directly to failed", func(ctx context.Context, db *queue.DB, id int64) error {
			return db.MarkFailed(ctx, id, "boom")
		}},
		{"approve an already-rejected item", func(ctx context.Context, db *queue.DB, id int64) error {
			if err := db.Reject(ctx, id, queue.ActorHuman); err != nil {
				return err
			}
			return db.Approve(ctx, id, queue.ActorHuman)
		}},
		{"approve an already-uploaded item (re-upload)", func(ctx context.Context, db *queue.DB, id int64) error {
			if err := db.Approve(ctx, id, queue.ActorHuman); err != nil {
				return err
			}
			if err := db.MarkUploaded(ctx, id, "uuid-y", 1); err != nil {
				return err
			}
			return db.Approve(ctx, id, queue.ActorHuman)
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openTestDB(t)
			ctx := context.Background()
			stageMessage(t, db, ctx, "msg-1")
			id := stageItem(t, db, ctx, "msg-1", "hash-a")

			err := tt.run(ctx, db, id)

			var invalidErr *queue.InvalidTransitionError
			if err == nil {
				t.Fatal("expected an error for an invalid final transition, got nil")
			}
			if !errors.As(err, &invalidErr) {
				t.Fatalf("error is not an *InvalidTransitionError: %v", err)
			}
		})
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 4, Criterion: "Lifecycle `staged → approved → uploaded` with terminals `rejected`, `already_in_portal`, `failed`, `suppressed_peppol`, plus `duplicate_linked` (see Design correction / OQ-P1); every transition logged with timestamp and actor `human`/`daemon` (F-41)"
func TestInvalidTransitionLeavesNoHalfState(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	stageMessage(t, db, ctx, "msg-1")
	id := stageItem(t, db, ctx, "msg-1", "hash-a")

	// staged -> uploaded is not in the F-41 graph.
	if err := db.MarkUploaded(ctx, id, "uuid-should-not-stick", 5); err == nil {
		t.Fatal("MarkUploaded on a staged item succeeded; want an InvalidTransitionError")
	}

	item, getErr := db.GetItem(ctx, id)
	if getErr != nil {
		t.Fatalf("GetItem: %v", getErr)
	}
	if item.Status != queue.StatusStaged {
		t.Errorf("Status = %s after a rejected transition attempt, want unchanged %s", item.Status, queue.StatusStaged)
	}
	if item.UUID != "" {
		t.Errorf("UUID = %q after a rejected transition attempt, want empty — no partial write should have applied", item.UUID)
	}
	if item.UploadedAt != nil {
		t.Error("UploadedAt is set after a rejected transition attempt, want nil — no partial write should have applied")
	}

	transitions, err := db.Transitions(ctx, id)
	if err != nil {
		t.Fatalf("Transitions: %v", err)
	}
	if len(transitions) != 1 {
		t.Fatalf("got %d transition log rows after a rejected transition attempt, want exactly 1 (only the initial staging)", len(transitions))
	}
}
