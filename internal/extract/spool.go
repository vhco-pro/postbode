package extract

import (
	"fmt"
	"os"
	"path/filepath"
)

// writeFileFunc matches os.WriteFile's signature — the seam tests use to
// inject a failing writer that simulates a full disk without touching the
// real filesystem (spec §8 fail-safe).
type writeFileFunc func(name string, data []byte, perm os.FileMode) error

// Spooler writes extracted document bytes to a directory at mode 0600
// (F-24). Its base directory is injectable via NewSpooler so production
// points it at DefaultSpoolDir() and every test uses a t.TempDir() —
// never a real home directory.
type Spooler struct {
	baseDir   string
	writeFile writeFileFunc
}

// NewSpooler builds a Spooler rooted at baseDir, writing through the real
// filesystem.
func NewSpooler(baseDir string) *Spooler {
	return &Spooler{baseDir: baseDir, writeFile: os.WriteFile}
}

// NewFailingSpooler builds a Spooler whose every Write call fails with
// err — a test-only constructor for exercising the spec §8 disk-full
// fail-safe without needing to actually fill a disk.
func NewFailingSpooler(baseDir string, err error) *Spooler {
	return &Spooler{
		baseDir: baseDir,
		writeFile: func(string, []byte, os.FileMode) error {
			return err
		},
	}
}

// NewSpoolerForTest builds a Spooler whose Write calls the supplied
// shouldFail function before every real write; a non-nil return simulates
// a failed write (e.g. disk full) for that call without touching the real
// filesystem, while a nil return performs the real write normally. Used
// by tests exercising ordering across a partial failure among several
// documents in one message (spec §8).
func NewSpoolerForTest(baseDir string, shouldFail func() error) *Spooler {
	return &Spooler{
		baseDir: baseDir,
		writeFile: func(name string, data []byte, perm os.FileMode) error {
			if err := shouldFail(); err != nil {
				return err
			}
			return os.WriteFile(name, data, perm)
		},
	}
}

// DefaultSpoolDir returns the production spool location,
// ~/Library/Application Support/Postbode/spool (F-24).
func DefaultSpoolDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("extract: DefaultSpoolDir: %w", err)
	}
	return filepath.Join(home, "Library", "Application Support", "Postbode", "spool"), nil
}

// Write spools data under name at mode 0600 (F-24), creating the base
// directory at 0700 if it does not exist yet.
func (s *Spooler) Write(name string, data []byte) (string, error) {
	if err := os.MkdirAll(s.baseDir, 0o700); err != nil {
		return "", fmt.Errorf("extract: spool: mkdir %s: %w", s.baseDir, err)
	}
	path := filepath.Join(s.baseDir, name)
	if err := s.writeFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("extract: spool: write %s: %w", path, err)
	}
	return path, nil
}

// Remove deletes a previously spooled file. A file that is already gone
// is not an error — Remove is used both for retention pruning and for
// best-effort cleanup after a partial multi-document spool failure, and
// neither caller should have to special-case "already removed".
func (s *Spooler) Remove(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("extract: spool: remove %s: %w", path, err)
	}
	return nil
}
