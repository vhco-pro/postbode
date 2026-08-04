package uploader

import (
	"context"
	"fmt"
	"time"

	"github.com/vhco-pro/postbode/internal/extract"
)

// PruneSpool runs the F-24 spool-pruning tick: it fetches every uploaded
// item older than retentionDays (via queue.DB.ListUploadedOlderThan) and
// removes its spool file via extract.PruneUploadedItems. Cadence (how often
// a caller invokes this) is the daemon's concern, per extract.PruneUploadedItem's
// own doc comment — this method only supplies the "which items" half that
// only the uploader (owner of upload timing) can answer.
func (u *Uploader) PruneSpool(ctx context.Context, retentionDays int) (prunedCount int, errs []error) {
	now := u.now()
	cutoff := now.Add(-time.Duration(retentionDays) * 24 * time.Hour)

	items, err := u.DB.ListUploadedOlderThan(ctx, cutoff)
	if err != nil {
		return 0, []error{fmt.Errorf("uploader: PruneSpool: list uploaded items: %w", err)}
	}
	return extract.PruneUploadedItems(items, retentionDays, now)
}
