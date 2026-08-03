package extract_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/vhco-pro/postbode/internal/extract"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 5, Criterion: "Spool write to `~/Library/Application Support/Postbode/spool/` at mode `0600`, referenced by the item (F-24)"
func TestSpoolerWriteModeIs0600(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spool")
	sp := extract.NewSpooler(dir)

	path, err := sp.Write("abc.pdf", []byte("%PDF-1.4 fake content"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s): %v", path, err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("spool file mode = %o, want 0600", perm)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "%PDF-1.4 fake content" {
		t.Errorf("spooled content = %q, want the original bytes unchanged", data)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 5, Criterion: "Make the base dir injectable so tests use a temp dir."
func TestSpoolerBaseDirIsInjectable(t *testing.T) {
	dir1 := filepath.Join(t.TempDir(), "spool-a")
	dir2 := filepath.Join(t.TempDir(), "spool-b")

	sp1 := extract.NewSpooler(dir1)
	sp2 := extract.NewSpooler(dir2)

	path1, err := sp1.Write("x.pdf", []byte("one"))
	if err != nil {
		t.Fatalf("sp1.Write: %v", err)
	}
	path2, err := sp2.Write("x.pdf", []byte("two"))
	if err != nil {
		t.Fatalf("sp2.Write: %v", err)
	}
	if filepath.Dir(path1) != dir1 {
		t.Errorf("sp1 wrote under %q, want %q", filepath.Dir(path1), dir1)
	}
	if filepath.Dir(path2) != dir2 {
		t.Errorf("sp2 wrote under %q, want %q", filepath.Dir(path2), dir2)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 5, Criterion: "Fail-safe on disk-full (spec §8): no queue row committed, message-id NOT marked seen, error logged, retried next poll. Test it with an injected failing writer."
func TestFailingSpoolerReturnsInjectedError(t *testing.T) {
	dir := t.TempDir()
	injected := errors.New("no space left on device")
	sp := extract.NewFailingSpooler(dir, injected)

	_, err := sp.Write("x.pdf", []byte("data"))
	if err == nil {
		t.Fatal("Write: err = nil, want the injected disk-full error")
	}
	if !errors.Is(err, injected) {
		t.Errorf("Write error does not wrap the injected error: %v", err)
	}

	// Nothing should have been written to the real filesystem.
	entries, rerr := os.ReadDir(dir)
	if rerr != nil {
		t.Fatalf("ReadDir: %v", rerr)
	}
	if len(entries) != 0 {
		t.Errorf("directory has %d entries after a failing write, want 0", len(entries))
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 5, Criterion: "Spool write to `~/Library/Application Support/Postbode/spool/`"
func TestDefaultSpoolDirIsUnderApplicationSupport(t *testing.T) {
	dir, err := extract.DefaultSpoolDir()
	if err != nil {
		t.Fatalf("DefaultSpoolDir: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory available in this environment: %v", err)
	}
	want := filepath.Join(home, "Library", "Application Support", "Postbode", "spool")
	if dir != want {
		t.Errorf("DefaultSpoolDir() = %q, want %q", dir, want)
	}
}
