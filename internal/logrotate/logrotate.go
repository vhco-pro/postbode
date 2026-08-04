// Package logrotate is a minimal, stdlib-only size-based rotating
// io.Writer for Postbode's local daemon log (F-65, NF-05): local only,
// rotated, no telemetry. It deliberately does not decide WHAT gets
// written — the daemon's own log lines are built by callers that already
// know never to include a message body or attachment content (see
// internal/cli's doc comment on the same rule); this package only owns
// "when does the current file get rotated out."
package logrotate

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// defaultMaxBytes is the size at which the current log file is rotated
// when a caller does not specify one.
const defaultMaxBytes = 10 * 1024 * 1024 // 10 MiB

// defaultMaxBackups is how many rotated-out files are kept when a caller
// does not specify one.
const defaultMaxBackups = 5

// Writer is an io.WriteCloser that appends to Path, rotating to
// Path.1, Path.2, ... (oldest evicted beyond MaxBackups) whenever a write
// would take the current file over MaxBytes.
type Writer struct {
	// Path is the active log file's path. Required.
	Path string
	// MaxBytes is the rotation threshold. <= 0 defaults to 10 MiB.
	MaxBytes int64
	// MaxBackups is how many rotated files are retained. <= 0 defaults to 5.
	MaxBackups int

	mu   sync.Mutex
	file *os.File
	size int64
}

func (w *Writer) maxBytes() int64 {
	if w.MaxBytes <= 0 {
		return defaultMaxBytes
	}
	return w.MaxBytes
}

func (w *Writer) maxBackups() int {
	if w.MaxBackups <= 0 {
		return defaultMaxBackups
	}
	return w.MaxBackups
}

// Write implements io.Writer. It rotates first if the current file would
// exceed MaxBytes after accepting p in full — a single Write is never
// split across two files, since log lines must stay intact for `postbode
// log`/grep to parse.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		if err := w.open(); err != nil {
			return 0, err
		}
	}
	if w.size > 0 && w.size+int64(len(p)) > w.maxBytes() {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	if err != nil {
		return n, fmt.Errorf("logrotate: write %s: %w", w.Path, err)
	}
	return n, nil
}

// Close closes the current file, if open.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func (w *Writer) open() error {
	if err := os.MkdirAll(filepath.Dir(w.Path), 0o700); err != nil {
		return fmt.Errorf("logrotate: mkdir: %w", err)
	}
	f, err := os.OpenFile(w.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("logrotate: open %s: %w", w.Path, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("logrotate: stat %s: %w", w.Path, err)
	}
	w.file = f
	w.size = info.Size()
	return nil
}

// rotate closes the current file, shifts Path.N -> Path.N+1 (dropping
// anything beyond MaxBackups), moves Path -> Path.1, and reopens a fresh
// empty Path.
func (w *Writer) rotate() error {
	if w.file != nil {
		if err := w.file.Close(); err != nil {
			return fmt.Errorf("logrotate: close before rotate: %w", err)
		}
		w.file = nil
	}

	backups := w.maxBackups()
	oldest := fmt.Sprintf("%s.%d", w.Path, backups)
	if err := os.Remove(oldest); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("logrotate: remove oldest backup: %w", err)
	}
	for i := backups - 1; i >= 1; i-- {
		from := fmt.Sprintf("%s.%d", w.Path, i)
		to := fmt.Sprintf("%s.%d", w.Path, i+1)
		if err := os.Rename(from, to); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("logrotate: shift backup %s -> %s: %w", from, to, err)
		}
	}
	if err := os.Rename(w.Path, w.Path+".1"); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("logrotate: rename current to .1: %w", err)
	}
	w.size = 0
	return w.open()
}
