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

const version = 1

type Snapshot struct {
	Version int           `json:"version"`
	Cursor  time.Time     `json:"cursor"`
	Groups  []group.Group `json:"groups"`
}

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
			f.Close()
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
