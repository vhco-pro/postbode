package cli

import (
	"fmt"
	"net/url"
	"os/exec"
	"strings"

	"github.com/vhco-pro/postbode/internal/webui"
)

// Launcher opens a URL in the user's default browser. Production shells
// out to macOS's `open`; every caller depends on this interface instead,
// so tests can exercise Review without ever launching a browser (mirrors
// internal/notify.Notifier's Fake pattern).
type Launcher interface {
	Open(url string) error
}

// MacOpenLauncher is the production Launcher: `open <url>`.
type MacOpenLauncher struct{}

// Open implements Launcher.
func (MacOpenLauncher) Open(rawURL string) error {
	if err := exec.Command("open", rawURL).Run(); err != nil {
		return fmt.Errorf("cli: open %s: %w", rawURL, err)
	}
	return nil
}

// ReviewURL builds the tokenized review UI URL (F-46) against addr
// (production always passes webui.DefaultAddr).
func ReviewURL(addr, token string) string {
	return fmt.Sprintf("http://%s/?t=%s", addr, url.QueryEscape(token))
}

// Review implements `postbode review` (F-46): read the session token the
// daemon wrote to tokenPath at startup and open the tokenized review URL
// via launcher. Returns a clear, actionable error — never a bare
// os.ReadFile error — when the token file is missing or empty, since that
// is exactly the "daemon isn't running" case (OQ-P8).
func Review(tokenPath, addr string, launcher Launcher) error {
	token, err := webui.ReadTokenFile(tokenPath)
	if err != nil {
		return fmt.Errorf("postbode: no session token at %s — is the daemon running? (%w)", tokenPath, err)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("postbode: session token at %s is empty — is the daemon running?", tokenPath)
	}
	return launcher.Open(ReviewURL(addr, token))
}
