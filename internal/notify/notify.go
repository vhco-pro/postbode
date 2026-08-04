// Package notify sends macOS user notifications for Postbode's two F-45
// events: a batch of items staged for review, and an upload batch
// completing. Production notifications shell out to `osascript`; every
// caller depends on the Notifier interface instead, so tests exercise Fake
// and never execute osascript (AC-28, NF-09).
package notify

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

// Notifier sends one user-facing notification message. Implementations
// must be safe for concurrent use.
type Notifier interface {
	Notify(ctx context.Context, message string) error
}

// OSAScript is the production Notifier (F-45): one `osascript -e 'display
// notification ...'` invocation per call, under the "Postbode" title.
type OSAScript struct{}

// Notify shells out to osascript to display message as a macOS
// notification. message is escaped as an AppleScript string literal so it
// cannot break out of the quoted argument osascript receives.
func (OSAScript) Notify(ctx context.Context, message string) error {
	script := fmt.Sprintf(`display notification %s with title "Postbode"`, appleScriptQuote(message))
	cmd := exec.CommandContext(ctx, "osascript", "-e", script)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("notify: osascript: %w", err)
	}
	return nil
}

// appleScriptQuote renders s as a double-quoted AppleScript string literal,
// escaping backslashes and double quotes.
func appleScriptQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		if r == '"' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

// Fake is a Notifier that records every message it is given instead of
// shelling out. It is the only Notifier any test in this repo may use
// (F-45, AC-28) — production code never runs under it.
type Fake struct {
	mu       sync.Mutex
	Messages []string
}

// Notify records message. It never returns an error.
func (f *Fake) Notify(_ context.Context, message string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Messages = append(f.Messages, message)
	return nil
}

// Count reports how many messages have been recorded so far.
func (f *Fake) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Messages)
}

// All returns a snapshot of every message recorded so far, in order.
func (f *Fake) All() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.Messages))
	copy(out, f.Messages)
	return out
}
