// Package state persists the error detector's VictoriaLogs cursor and group
// table as one JSON file, so a restart neither re-announces known errors nor
// loses an open collection window.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"nacfson_pipeline/services/error-pipeline/internal/group"
)

// version is the only on-disk format Load accepts. Save always writes it.
const version = 1

// Snapshot is the persisted detector state.
type Snapshot struct {
	Version int           `json:"version"`
	Cursor  time.Time     `json:"cursor"`
	Groups  []group.Group `json:"groups"`
}

// Load reads the snapshot at path. A missing file is not an error: it returns
// an empty Snapshot. Corrupt JSON or an unsupported version returns an error,
// and the caller decides whether to start fresh.
func Load(path string) (Snapshot, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Snapshot{}, nil
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("read state %s: %w", path, err)
	}
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return Snapshot{}, fmt.Errorf("parse state %s: %w", path, err)
	}
	if s.Version != version {
		return Snapshot{}, fmt.Errorf("state %s: unsupported version %d (want %d)", path, s.Version, version)
	}
	return s, nil
}

// Save writes s to path atomically: it writes a temporary file in the same
// directory, fsyncs it, and renames it over path. The stored version is
// always 1, whatever s.Version holds. On failure the temporary file is
// removed.
func Save(path string, s Snapshot) (err error) {
	s.Version = version
	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}

	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp state file: %w", err)
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			f.Close() // best effort; a second Close error is irrelevant here
			os.Remove(tmp)
		}
	}()

	if _, err = f.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err = f.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", tmp, err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err = os.Chmod(tmp, 0o600); err != nil {
		return fmt.Errorf("chmod %s: %w", tmp, err)
	}
	if err = os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s: %w", tmp, err)
	}
	return nil
}
