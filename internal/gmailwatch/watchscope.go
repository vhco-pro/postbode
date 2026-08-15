package gmailwatch

import "strings"

// Watch scope values for the F-11 gmail.watch config key.
const (
	// WatchAll is the default: every message that arrives in the mailbox
	// except the ones that are structurally not incoming mail — sent, drafts,
	// trash and spam.
	//
	// This is deliberately wider than "the INBOX". Mail can land in the
	// mailbox without ever carrying the INBOX label: Gmail's "Check mail from
	// other accounts" POP3 fetcher inserts messages with an empty label set
	// when "Archive incoming messages" is on, and so does any client using
	// users.messages.import/insert. Those are real invoices arriving at a real
	// mailbox, and an INBOX-only scope misses every one of them silently —
	// exactly the failure G-1 forbids.
	WatchAll = "all"

	// WatchInbox restricts the scope to messages carrying the INBOX label.
	// This is F-11's original scope, kept because the knob is meant to be
	// reversible; it is no longer the default (see WatchAll).
	WatchInbox = "inbox"
)

// excludedLabelIDs are the Gmail system labels that put a message outside
// every watch scope. Drafts and sent mail are the developer's own writing,
// not an incoming invoice; trash and spam are explicit "not wanted" signals
// that Postbode has no business second-guessing.
var excludedLabelIDs = []string{"SENT", "DRAFT", "TRASH", "SPAM"}

// effectiveWatch resolves Config.Watch (case-insensitive, empty means the
// WatchAll default) to one of the two supported scopes. An unrecognised
// value resolves to WatchAll rather than failing: config.Load is the layer
// that rejects malformed configuration (F-29), and the recall-favouring
// scope is the safe interpretation of an ambiguous one.
func (w *Watcher) effectiveWatch() string {
	if strings.EqualFold(strings.TrimSpace(w.Config.Watch), WatchInbox) {
		return WatchInbox
	}
	return WatchAll
}

// inScope reports whether a message carrying labelIDs is inside the
// configured watch scope (F-11).
//
// # Why this is checked on the fetched message, not via history.list's labelId
//
// users.history.list takes a labelId parameter that looks like exactly this
// check and is not. Gmail matches it against the label set recorded in the
// history record itself, and that set is EMPTY for a message inserted by the
// POP3 fetcher or by messages.import — so labelId=INBOX dropped every
// imported message before Postbode ever saw its id. Worse, it did not even
// enforce the scope it claimed to: verified live against the real mailbox,
// a window queried with labelId=INBOX returned messagesAdded records for
// messages whose labels were [DRAFT] and [SENT], while returning zero
// records for the three imported invoices the same window contained without
// the parameter.
//
// The parameter is therefore gone from historySync entirely, and the scope
// is enforced here instead, against the labels Gmail reports for the actual
// message. One authoritative check, on the real label set, applied
// identically to the F-12 history path and the F-13 fallback path.
func (w *Watcher) inScope(labelIDs []string) bool {
	has := func(want string) bool {
		for _, id := range labelIDs {
			if id == want {
				return true
			}
		}
		return false
	}
	for _, excluded := range excludedLabelIDs {
		if has(excluded) {
			return false
		}
	}
	if w.effectiveWatch() == WatchInbox {
		return has("INBOX")
	}
	return true
}
