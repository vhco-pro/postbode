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

// TerminalNotifier posts via terminal-notifier(1) instead of osascript, so
// the notification can actually open the review queue when clicked.
//
// osascript's `display notification` posts the notification *as Script
// Editor*, because that is the app hosting the script. macOS then attributes
// the click to that app, so pressing "Show" opens Script Editor — which is
// what a user reasonably reads as broken. There is no way to attach an
// action, or to change the owning app, from osascript: the notification
// belongs to whichever bundle posted it. terminal-notifier is a real app
// bundle and supports -open, which is the whole reason it exists.
//
// OpenURL is deliberately the bare loopback URL with no token in it. The
// session token is stable and the browser keeps a cookie from any previous
// `postbode review`, so the plain URL works — and a token in a notification
// would be persisted by Notification Center for no benefit.
type TerminalNotifier struct {
	// Path is the resolved terminal-notifier binary.
	Path string
	// OpenURL is opened when the notification is clicked.
	OpenURL string
}

// Notify posts message via terminal-notifier.
func (t TerminalNotifier) Notify(ctx context.Context, message string) error {
	args := []string{"-title", "Postbode", "-message", message}
	if t.OpenURL != "" {
		args = append(args, "-open", t.OpenURL)
	}
	if err := exec.CommandContext(ctx, t.Path, args...).Run(); err != nil {
		return fmt.Errorf("notify: terminal-notifier: %w", err)
	}
	return nil
}

// Best returns the most capable Notifier available on this machine:
// terminal-notifier when it is installed (clickable, opens openURL), and
// osascript otherwise.
//
// The fallback is not a nicety — osascript is always present, so postbode
// never silently loses notifications on a machine without the helper. The
// Homebrew formula depends on terminal-notifier so the good path is the
// default for anyone who installed via brew.
func Best(openURL string) Notifier {
	if path, err := exec.LookPath("terminal-notifier"); err == nil {
		return TerminalNotifier{Path: path, OpenURL: openURL}
	}
	return OSAScript{}
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
