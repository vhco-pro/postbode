// daemon.go wires cmd/postbode's `daemon` subcommand (F-60, also installed
// as postboded — see main.go's arg0 alias). Every testable decision (poll
// cadence, F-15's uploader-refusal scope, graceful shutdown) already lives
// in internal/daemon (Phase 13); this file only builds the real,
// live-credentialed services internal/daemon.Daemon needs and wires
// process signal handling — the same "thin main over tested internal
// packages" shape cmd/spike already established.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/vhco-pro/postbode/internal/clearfacts"
	"github.com/vhco-pro/postbode/internal/cli"
	"github.com/vhco-pro/postbode/internal/config"
	"github.com/vhco-pro/postbode/internal/daemon"
	"github.com/vhco-pro/postbode/internal/extract"
	"github.com/vhco-pro/postbode/internal/gmailwatch"
	"github.com/vhco-pro/postbode/internal/keychain"
	"github.com/vhco-pro/postbode/internal/logrotate"
	"github.com/vhco-pro/postbode/internal/notify"
	"github.com/vhco-pro/postbode/internal/rules"
	"github.com/vhco-pro/postbode/internal/uploader"
	"github.com/vhco-pro/postbode/internal/webui"
	"golang.org/x/oauth2"
	"google.golang.org/api/gmail/v1"
)

// defaultLogPath is F-65's local rotated daemon log location.
func defaultLogPath(home string) string {
	return filepath.Join(home, "Library", "Logs", "Postbode", "postbode.log")
}

// defaultConfigPath mirrors cmd/spike's ~/.config/postbode/config.yaml
// (spec §6.5) — the same file `postbode spike` writes
// administration.company_number into.
func defaultConfigPath(home string) string {
	return filepath.Join(home, ".config", "postbode", "config.yaml")
}

// defaultCredentialsPath resolves the Google OAuth desktop-app credentials
// file (D-2). Resolution order, first hit wins:
//
//  1. POSTBODE_CREDENTIALS_PATH
//  2. ~/Library/Application Support/Postbode/credentials.json
//  3. ./credentials.json
//
// (2) exists because of how this actually gets installed. A relative
// "credentials.json" only resolves when the daemon is started from the
// repo checkout; a Homebrew-managed launchd service has no meaningful
// working directory, so it would never find the file and would sit at
// "not yet authenticated" forever — with no error, since a missing
// credentials file is a legitimate pre-bootstrap state. (3) is kept so
// running from the checkout during development still works unchanged.
func defaultCredentialsPath(home string) string {
	if v := os.Getenv("POSTBODE_CREDENTIALS_PATH"); v != "" {
		return v
	}
	installed := filepath.Join(home, "Library", "Application Support", "Postbode", "credentials.json")
	if _, err := os.Stat(installed); err == nil {
		return installed
	}
	return "credentials.json"
}

// legacyGmailTokenCachePath is cmd/spike's pre-Keychain, on-disk Gmail
// token cache location. It is read exactly once, at daemon startup, as a
// migration source into the Keychain (F-62) — after migration the disk
// file is left in place (untouched, still gitignored, F-63) rather than
// deleted, since removing a developer's only credential cache on a
// best-effort migration path is a worse failure mode than a redundant
// file.
func legacyGmailTokenCachePath(home string) string {
	return filepath.Join(home, "Library", "Application Support", "Postbode", "token.json")
}

// runDaemon implements `postbode daemon` / `postboded` (F-60, F-61): load
// config, resolve secrets via the Keychain (F-62), wire
// internal/gmailwatch + internal/uploader + internal/webui through
// internal/daemon.Daemon, and run until SIGINT/SIGTERM. It never returns a
// non-zero exit for a recoverable runtime condition (NF-06) — the only
// non-zero returns are startup-time configuration/IO failures a human
// must fix before the daemon can do anything at all (a malformed config
// file, an unwritable state directory), which is a different class of
// problem than "Gmail is temporarily unreachable."
func runDaemon(args []string, _, stderr io.Writer) int {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	home, err := userHomeDir()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "postbode: daemon: %v\n", err)
		return 1
	}

	logWriter := &logrotate.Writer{Path: defaultLogPath(home)}
	defer func() { _ = logWriter.Close() }()
	logger := log.New(io.MultiWriter(stderr, logWriter), "", log.LstdFlags|log.LUTC)
	logf := func(format string, args ...any) { logger.Printf(format, args...) }

	cfg, err := config.Load(defaultConfigPath(home))
	if err != nil {
		// F-29: a malformed config file is a "refuse to start" condition,
		// not a recoverable runtime one.
		_, _ = fmt.Fprintf(stderr, "postbode: daemon: %v\n", err)
		return 1
	}

	db, err := cli.OpenDB(context.Background(), cli.DefaultDBPath(home))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "postbode: daemon: %v\n", err)
		return 1
	}
	defer func() { _ = db.Close() }()

	spoolDir, err := extract.DefaultSpoolDir()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "postbode: daemon: %v\n", err)
		return 1
	}

	// Prefer terminal-notifier so clicking the notification opens the review
	// queue; fall back to osascript, whose notifications open Script Editor
	// instead (see notify.TerminalNotifier). The URL carries no token: the
	// token is stable and the browser holds a cookie from any earlier
	// `postbode review`, so the bare loopback URL is enough.
	notifier := notify.Best("http://" + webui.DefaultAddr + "/")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// credentials.json is checked BEFORE anything ever touches the
	// Keychain (store is constructed only inside buildGmailService, once
	// it is known there is a real oauth2.Config to resolve a token
	// against). This ordering is deliberate, not incidental: it is what
	// keeps `go test` — which runs with no credentials.json in
	// cmd/postbode's package directory — from ever shelling out to
	// /usr/bin/security at all, let alone prompting a real Keychain
	// access dialog. No test may touch the real Keychain (this phase's
	// own instructions).
	svc, oauthCfg := buildGmailService(ctx, home, logf)
	if svc == nil {
		// Nothing in Postbode can run without a Gmail identity to poll —
		// unlike a routine re-auth (F-16, handled once already running),
		// this is a bootstrap prerequisite (D-2) no amount of internal
		// retrying can satisfy on its own. Exit 0 (not a crash, not a
		// launchd KeepAlive restart-loop trigger) with a clear,
		// actionable message; the operator authenticates once (currently
		// via `make spike`) and restarts the daemon.
		msg := "Postbode: not yet authenticated with Gmail. Run `make spike` once (or otherwise populate the Keychain/credentials.json), then restart the daemon."
		_, _ = fmt.Fprintln(stderr, "postbode: daemon: "+msg)
		_ = notifier.Notify(ctx, msg)
		return 0
	}

	store := keychain.Store(keychain.DarwinStore{})

	watcher := &gmailwatch.Watcher{
		Service:   svc,
		DB:        db,
		Extractor: extract.New(spoolDir, db),
		Rules:     rules.New(cfg),
		Notifier:  notifier,
		UserID:    "me",
		Config: gmailwatch.Config{
			Watch:             cfg.Gmail.Watch,
			QueryWindowDays:   cfg.Gmail.QueryWindowDays,
			SubmittedLabel:    cfg.Gmail.SubmittedLabel,
			FailureBudget:     cfg.Gmail.FailureBudget,
			ParkRetryCooldown: cfg.Gmail.ParkRetryCooldown,
			ParkRetryAttempts: cfg.Gmail.ParkRetryAttempts,
			PollFailureBudget: cfg.Gmail.PollFailureBudget,
		},
		OAuthConfig: oauthCfg,
		Logf:        logf,
	}
	watcher.Extractor.KnownPeppolGlobs = cfg.Vendors.KnownPeppol

	client := clearfacts.NewClient(keychain.PATSource{Store: store})
	up := &uploader.Uploader{
		DB:            db,
		Client:        client,
		CompanyNumber: cfg.Administration.CompanyNumber,
		LabelApplier:  watcher,
		Notifier:      notifier,
		Logf:          logf,
	}

	// Reuse the existing token rather than minting a new one each start, so
	// a bookmarked review URL survives restarts (see LoadOrCreateToken).
	token, err := webui.LoadOrCreateToken(webui.DefaultTokenPath(home))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "postbode: daemon: %v\n", err)
		return 1
	}
	uiSrv, err := webui.NewServer(db, token)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "postbode: daemon: %v\n", err)
		return 1
	}

	d := &daemon.Daemon{
		DB:            db,
		Watcher:       watcher,
		Uploader:      up,
		Notifier:      notifier,
		PollInterval:  cfg.Gmail.PollInterval,
		RetentionDays: cfg.RetentionDays,
		Logf:          logf,
	}

	// Pressing Approve drains immediately instead of waiting for the next
	// upload tick. Without this the queue is still correct, just slow to
	// visibly react, which reads as broken.
	uiSrv.OnApprove = d.Nudge

	// Bind synchronously, BEFORE announcing startup.
	//
	// This used to happen inside the goroutine below, with the error only
	// logged after wg.Wait() — i.e. at shutdown. A failed bind (almost
	// always "address already in use" from a second postbode) was therefore
	// invisible for the entire life of the daemon, while the log cheerfully
	// claimed "review UI on 127.0.0.1:7391". Polling and uploading kept
	// working, so nothing looked wrong until someone tried to open the queue.
	var wg sync.WaitGroup
	var uiErr, daemonErr error

	ln, lerr := webui.Listen(webui.DefaultAddr)
	if lerr != nil {
		// Not fatal: the watcher must keep staging so no invoice is missed
		// (G-1). But nothing can be approved without the UI, so say so
		// loudly in the log and to the user, rather than pretending.
		logf("daemon: REVIEW UI UNAVAILABLE — could not bind %s: %v", webui.DefaultAddr, lerr)
		logf("daemon: another postbode is probably already running; check `pgrep -fl postbode`. " +
			"Polling and staging continue, but nothing can be approved or uploaded until this is resolved.")
		_ = notifier.Notify(ctx, fmt.Sprintf("Postbode: review UI could not start on %s — another copy may be running.", webui.DefaultAddr))
		wg.Add(1)
	} else {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := uiSrv.Serve(ctx, ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				uiErr = err
				logf("daemon: review UI server error: %v", err)
			}
		}()
	}

	go func() {
		defer wg.Done()
		daemonErr = d.Run(ctx)
	}()

	if lerr == nil {
		logf("daemon: started (review UI on %s)", webui.DefaultAddr)
	} else {
		logf("daemon: started WITHOUT the review UI")
	}
	<-ctx.Done()
	logf("daemon: shutdown signal received, stopping")
	wg.Wait()

	if uiErr != nil {
		logf("daemon: review UI server error: %v", uiErr)
	}
	if daemonErr != nil {
		logf("daemon: run loop error: %v", daemonErr)
	}
	logf("daemon: stopped cleanly")
	return 0
}

// buildGmailService resolves a Gmail identity from the Keychain (F-62)
// and returns a ready *gmail.Service, or (nil, nil) when Postbode has
// never been authenticated yet (no credentials.json, or no cached token)
// — a bootstrap condition, not a routine re-auth one (see runDaemon's
// doc). oauthCfg is returned alongside svc so the caller can wire it into
// Watcher.OAuthConfig for F-16's one-click re-auth notification URL.
func buildGmailService(ctx context.Context, home string, logf func(string, ...any)) (*gmail.Service, *oauth2.Config) {
	credPath := defaultCredentialsPath(home)
	if !gmailwatch.CredentialsFileExists(credPath) {
		logf("daemon: %s not found — Gmail is not authenticated yet", credPath)
		return nil, nil
	}
	oauthCfg, err := gmailwatch.LoadOAuthConfig(credPath)
	if err != nil {
		logf("daemon: load oauth config: %v", err)
		return nil, nil
	}

	// The Keychain is only ever touched once a real credentials.json
	// (and therefore a real oauth2.Config) is confirmed to exist — see
	// runDaemon's comment on this ordering.
	store := keychain.Store(keychain.DarwinStore{})
	legacyPath := legacyGmailTokenCachePath(home)

	// Every Keychain call is time-bounded. security(1) raises a macOS
	// authorization prompt the first time an item is written or read by a
	// new binary, and a launchd agent has no one to click it — the call
	// then blocks until something kills it. An unbounded Keychain call is
	// therefore a hang, not an error, which is the worst failure shape for
	// a daemon that is supposed to keep polling.
	kcCtx, cancel := context.WithTimeout(ctx, keychainTimeout)
	defer cancel()

	if err := migrateLegacyGmailToken(kcCtx, store, legacyPath); err != nil {
		logf("daemon: legacy Gmail token migration: %v", err)
	}

	tok, err := keychain.LoadGmailToken(kcCtx, store)
	if err != nil {
		// ANY Keychain failure falls back to the on-disk cache — not just
		// ErrNotFound. A denied or timed-out prompt is a storage-preference
		// problem, and treating it as "no credentials" would strand a
		// perfectly usable token on disk and stop the daemon from ever
		// polling: a self-inflicted outage. The token is what matters;
		// where it is kept is secondary.
		legacyTok, lerr := gmailwatch.LoadCachedToken(legacyPath)
		if lerr != nil {
			logf("daemon: no Gmail token in the Keychain (%v) and none cached on disk — not authenticated yet", err)
			return nil, nil
		}
		logf("daemon: Keychain unavailable (%v); using the on-disk token cache at %s. "+
			"To move it into the Keychain, run the daemon once from a terminal and "+
			"click Always Allow on the prompt.", err, legacyPath)
		tok = legacyTok
	}
	svc, err := gmailwatch.NewService(ctx, oauthCfg, tok)
	if err != nil {
		logf("daemon: build gmail service: %v", err)
		return nil, nil
	}
	return svc, oauthCfg
}

// migrateLegacyGmailToken copies cmd/spike's pre-Keychain disk-cached
// Gmail token into the Keychain, exactly once (a no-op once the Keychain
// already has one). See legacyGmailTokenCachePath's doc for why the disk
// file itself is never deleted.
// keychainTimeout bounds every security(1) call. The first read or write
// by a new binary raises a macOS authorization prompt; a launchd agent has
// nobody to click it, so the call would otherwise block forever. Generous
// enough for a human to click Always Allow when run from a terminal, short
// enough that an unattended daemon falls through to the on-disk cache and
// keeps polling.
const keychainTimeout = 20 * time.Second

func migrateLegacyGmailToken(ctx context.Context, store keychain.Store, legacyPath string) error {
	if _, err := keychain.LoadGmailToken(ctx, store); err == nil {
		return nil
	} else if !errors.Is(err, keychain.ErrNotFound) {
		return fmt.Errorf("check existing keychain token: %w", err)
	}
	tok, err := gmailwatch.LoadCachedToken(legacyPath)
	if err != nil {
		// Nothing to migrate — not an error, just "never ran cmd/spike
		// with this home directory" or "already migrated and cleaned up."
		return nil
	}
	if err := keychain.SaveGmailToken(ctx, store, tok); err != nil {
		return fmt.Errorf("save migrated token to keychain: %w", err)
	}
	return nil
}
