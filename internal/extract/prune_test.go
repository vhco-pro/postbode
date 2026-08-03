package extract_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vhco-pro/postbode/internal/extract"
	"github.com/vhco-pro/postbode/internal/queue"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 5, Criterion: "Spool pruning after `retention_days` (default 30) after successful upload, run on a daemon tick (F-24 — cadence is unspecified in the spec, see OQ-P5)"
func TestPruneUploadedItem(t *testing.T) {
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	newFile := func(t *testing.T) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "item.pdf")
		if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
			t.Fatalf("seed spool file: %v", err)
		}
		return path
	}

	recentUpload := now.Add(-10 * 24 * time.Hour)
	agedUpload := now.Add(-31 * 24 * time.Hour)
	exactlyAtBoundary := now.Add(-30 * 24 * time.Hour)

	tests := []struct {
		name       string
		buildItem  func(t *testing.T) *queue.Item
		wantPruned bool
	}{
		{
			name: "uploaded and older than retention_days is pruned",
			buildItem: func(t *testing.T) *queue.Item {
				return &queue.Item{Status: queue.StatusUploaded, UploadedAt: &agedUpload, SpoolPath: newFile(t)}
			},
			wantPruned: true,
		},
		{
			name: "uploaded but within retention_days is kept",
			buildItem: func(t *testing.T) *queue.Item {
				return &queue.Item{Status: queue.StatusUploaded, UploadedAt: &recentUpload, SpoolPath: newFile(t)}
			},
			wantPruned: false,
		},
		{
			name: "exactly at the retention_days boundary is pruned",
			buildItem: func(t *testing.T) *queue.Item {
				return &queue.Item{Status: queue.StatusUploaded, UploadedAt: &exactlyAtBoundary, SpoolPath: newFile(t)}
			},
			wantPruned: true,
		},
		{
			name: "staged (not yet uploaded) is never pruned regardless of age",
			buildItem: func(t *testing.T) *queue.Item {
				return &queue.Item{Status: queue.StatusStaged, SpoolPath: newFile(t)}
			},
			wantPruned: false,
		},
		{
			name: "uploaded with no UploadedAt timestamp is never pruned",
			buildItem: func(t *testing.T) *queue.Item {
				return &queue.Item{Status: queue.StatusUploaded, SpoolPath: newFile(t)}
			},
			wantPruned: false,
		},
		{
			name: "uploaded with no spool path is a no-op, not an error",
			buildItem: func(t *testing.T) *queue.Item {
				return &queue.Item{Status: queue.StatusUploaded, UploadedAt: &agedUpload, SpoolPath: ""}
			},
			wantPruned: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item := tt.buildItem(t)
			path := item.SpoolPath

			pruned, err := extract.PruneUploadedItem(item, extract.DefaultRetentionDays, now)
			if err != nil {
				t.Fatalf("PruneUploadedItem: %v", err)
			}
			if pruned != tt.wantPruned {
				t.Fatalf("pruned = %v, want %v", pruned, tt.wantPruned)
			}

			if path == "" {
				return
			}
			_, statErr := os.Stat(path)
			exists := statErr == nil
			if tt.wantPruned && exists {
				t.Errorf("spool file %s still exists after it should have been pruned", path)
			}
			if !tt.wantPruned && !exists {
				t.Errorf("spool file %s was removed but should have been kept", path)
			}
		})
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 5, Criterion: "exposed as a callable function (the daemon tick that calls it lands later — cadence is unspecified, plan OQ-P5)"
func TestPruneUploadedItemsCallableAsABatchAndToleratesMissingFiles(t *testing.T) {
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	aged := now.Add(-45 * 24 * time.Hour)

	f1 := filepath.Join(t.TempDir(), "a.pdf")
	if err := os.WriteFile(f1, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	// f2 deliberately does not exist on disk — "already pruned or never
	// written" must not be an error (idempotent pruning).
	f2 := filepath.Join(t.TempDir(), "missing.pdf")

	items := []*queue.Item{
		{Status: queue.StatusUploaded, UploadedAt: &aged, SpoolPath: f1},
		{Status: queue.StatusUploaded, UploadedAt: &aged, SpoolPath: f2},
	}

	pruned, errs := extract.PruneUploadedItems(items, extract.DefaultRetentionDays, now)
	if len(errs) != 0 {
		t.Fatalf("PruneUploadedItems errs = %v, want none (a missing file is not an error)", errs)
	}
	if pruned != 1 {
		t.Fatalf("pruned = %d, want 1 (only f1 existed on disk to remove)", pruned)
	}
}
