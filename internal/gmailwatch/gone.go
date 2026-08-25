package gmailwatch

import (
	"errors"
	"net/http"

	"google.golang.org/api/googleapi"
)

// isMessageGone reports whether err is Gmail's 404 for a message id that no
// longer exists — the shape of messages.get on a message deleted (or purged
// from Trash) after it was listed by history.list or messages.list.
//
// This is deliberately narrow. Only a 404 counts: a 403, a 429 or a 5xx all
// mean the message may still exist and must keep the poll's ordinary error
// path, because retrying is the only thing that can recover them. A 404
// cannot be recovered by any number of retries, so treating it as fatal
// converts one dead message into an unbounded outage (see Poll).
func isMessageGone(err error) bool {
	var gerr *googleapi.Error
	if errors.As(err, &gerr) {
		return gerr.Code == http.StatusNotFound
	}
	return false
}
