package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/vhco-pro/postbode/internal/queue"
)

// DefaultDBPath returns the production queue database location under the
// given home directory: "~/Library/Application Support/Postbode/postbode.db"
// — the same Application Support directory internal/extract (spool) and
// internal/webui (session.token) already use. homeDir is a parameter
// (never read from the environment here) so production and tests control
// it explicitly, mirroring webui.DefaultTokenPath.
func DefaultDBPath(homeDir string) string {
	return filepath.Join(homeDir, "Library", "Application Support", "Postbode", "postbode.db")
}

// OpenDB opens (creating if necessary) the queue database at path,
// creating any missing parent directories first. queue.Open itself does
// not create directories, so this wrapper is what lets a first-ever
// `postbode status` succeed against a brand new Application Support
// directory rather than failing with "no such file or directory".
func OpenDB(ctx context.Context, path string) (*queue.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("cli: OpenDB: mkdir: %w", err)
	}
	db, err := queue.Open(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("cli: OpenDB: %w", err)
	}
	return db, nil
}
