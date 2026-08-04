package logrotate_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vhco-pro/postbode/internal/logrotate"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 13, Criterion: "F-65: Logs are local, rotated, and never contain message bodies or attachment contents."
func TestWriterRotatesWhenOverMaxBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "postbode.log")
	w := &logrotate.Writer{Path: path, MaxBytes: 20, MaxBackups: 3}
	t.Cleanup(func() { _ = w.Close() })

	line := []byte("0123456789\n") // 11 bytes
	for i := 0; i < 5; i++ {
		if _, err := w.Write(line); err != nil {
			t.Fatalf("Write(%d): %v", i, err)
		}
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("current log file missing: %v", err)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("expected a rotated backup %s.1 to exist: %v", path, err)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 13, Criterion: "F-65: Logs are local, rotated ..."
func TestWriterEvictsBeyondMaxBackups(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "postbode.log")
	w := &logrotate.Writer{Path: path, MaxBytes: 10, MaxBackups: 2}
	t.Cleanup(func() { _ = w.Close() })

	line := []byte("0123456789\n")
	for i := 0; i < 40; i++ {
		if _, err := w.Write(line); err != nil {
			t.Fatalf("Write(%d): %v", i, err)
		}
	}

	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Errorf("expected %s.3 not to exist (MaxBackups=2), stat err = %v", path, err)
	}
	if _, err := os.Stat(path + ".2"); err != nil {
		t.Errorf("expected %s.2 to exist: %v", path, err)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 13, Criterion: "F-65: Logs are local, rotated, and never contain message bodies or attachment contents."
func TestWriterPreservesContentAcrossRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "postbode.log")
	w := &logrotate.Writer{Path: path, MaxBytes: 15, MaxBackups: 2}
	t.Cleanup(func() { _ = w.Close() })

	if _, err := w.Write([]byte("first-line\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := w.Write([]byte("second-line-forces-rotation\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	backup, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if !bytes.Contains(backup, []byte("first-line")) {
		t.Errorf("backup file missing pre-rotation content: %q", backup)
	}

	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read current: %v", err)
	}
	if !strings.Contains(string(current), "second-line-forces-rotation") {
		t.Errorf("current file missing post-rotation content: %q", current)
	}
}
