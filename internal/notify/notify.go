// Package notify sends macOS user notifications for Postbode's two F-45
// events: a batch of items staged for review, and an upload batch
// completing. Production prefers terminal-notifier, with an osascript fallback.
// Callers use Notifier so tests never post desktop notifications (AC-28, NF-09).
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

// OSAScript is the fallback Notifier (F-45): one `osascript -e 'display
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
// AppleScript cannot attach a click action. terminal-notifier supports both
// -open and -execute; production uses the latter to run `postbode review` with
// the current token, without persisting that token in Notification Center.
type TerminalNotifier struct {
	// Path is the resolved terminal-notifier binary.
	Path string
	// OpenURL is opened when the notification is clicked.
	OpenURL string
	// Execute is a shell-quoted absolute command, without session secrets.
	Execute string
}

// Notify posts message via terminal-notifier.
func (t TerminalNotifier) Notify(ctx context.Context, message string) error {
	args := []string{"-title", "Postbode", "-message", message}
	if t.Execute != "" {
		args = append(args, "-execute", t.Execute)
	} else if t.OpenURL != "" {
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
	if path := helperPath(exec.LookPath); path != "" {
		return TerminalNotifier{Path: path, OpenURL: openURL}
	}
	return OSAScript{}
}

// BestReview opens the review UI using the current token even in a browser
// without a session cookie. Only the executable path is stored by macOS.
func BestReview(executable string) Notifier {
	n := Best("")
	if t, ok := n.(TerminalNotifier); ok {
		t.Execute = "'" + strings.ReplaceAll(executable, "'", "'\"'\"'") + "' review"
		return t
	}
	return n
}

func helperPath(lookup func(string) (string, error)) string {
	// launchd does not inherit the interactive shell's Homebrew PATH.
	for _, candidate := range []string{"terminal-notifier", "/opt/homebrew/bin/terminal-notifier", "/usr/local/bin/terminal-notifier"} {
		if path, err := lookup(candidate); err == nil {
			return path
		}
	}
	return ""
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
