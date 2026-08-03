package extract

import (
	"os"
	"time"

	"github.com/vhco-pro/postbode/internal/queue"
)

// DefaultRetentionDays is F-24's default retention_days.
const DefaultRetentionDays = 30

// PruneUploadedItem removes item's spool file once retentionDays have
// elapsed since it reached status=uploaded (F-24). It is a no-op — never
// an error — for any item that is not uploaded, has no uploaded_at, or
// has no spool path; and a spool file that is already gone is likewise
// not an error, so pruning is safe to call repeatedly or redundantly.
//
// Cadence: F-24 states only the retention *condition* ("pruned
// retention_days after successful upload"), not when pruning runs — see
// plan OQ-P5. This function is the callable primitive Phase 5 owns;
// wiring it to a recurring daemon tick, and supplying it the list of
// uploaded items to check, is a later phase's job (the uploader, Phase
// 9, is what actually knows an item just became uploaded).
func PruneUploadedItem(item *queue.Item, retentionDays int, now time.Time) (pruned bool, err error) {
	if item == nil || item.Status != queue.StatusUploaded || item.UploadedAt == nil || item.SpoolPath == "" {
		return false, nil
	}
	age := now.Sub(*item.UploadedAt)
	if age < time.Duration(retentionDays)*24*time.Hour {
		return false, nil
	}
	if rmErr := os.Remove(item.SpoolPath); rmErr != nil {
		if os.IsNotExist(rmErr) {
			return false, nil
		}
		return false, rmErr
	}
	return true, nil
}

// PruneUploadedItems runs PruneUploadedItem over a batch of items —
// typically supplied by the caller via one of queue.DB's item-listing
// methods. A single item's removal error is collected, not fatal to the
// rest of the batch.
func PruneUploadedItems(items []*queue.Item, retentionDays int, now time.Time) (prunedCount int, errs []error) {
	for _, item := range items {
		ok, err := PruneUploadedItem(item, retentionDays, now)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if ok {
			prunedCount++
		}
	}
	return prunedCount, errs
}
