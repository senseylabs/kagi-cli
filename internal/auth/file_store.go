//go:build !darwin

package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type fileStore struct {
	path string
}

// NewTokenStore returns a file-based token store for non-macOS systems.
func NewTokenStore() TokenStore {
	home, _ := os.UserHomeDir()
	return &fileStore{
		path: filepath.Join(home, ".kagi", "credentials"),
	}
}

func (f *fileStore) Save(creds Credentials) error {
	dir := filepath.Dir(f.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create credentials directory: %w", err)
	}

	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize credentials: %w", err)
	}

	// Write to a temp file in the same directory, then atomically rename it over
	// the target. os.WriteFile truncates in place, so a concurrent reader (e.g.
	// another kagi process loading credentials at startup) could otherwise observe
	// a half-written file and fail to parse it. Rename is atomic on POSIX and
	// effectively so on NTFS, so readers always see the old or the new complete
	// file, never a partial one. os.CreateTemp creates the file with 0600.
	tmp, err := os.CreateTemp(dir, ".credentials-*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temp credentials file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // best-effort cleanup if we bail before rename

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("failed to write temp credentials file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close temp credentials file: %w", err)
	}
	if err := os.Rename(tmpName, f.path); err != nil {
		return fmt.Errorf("failed to write credentials file: %w", err)
	}
	return nil
}

func (f *fileStore) Load() (Credentials, error) {
	data, err := os.ReadFile(f.path)
	if err != nil {
		return Credentials{}, fmt.Errorf("no credentials found at %s: %w", f.path, err)
	}

	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return Credentials{}, fmt.Errorf("failed to parse credentials: %w", err)
	}
	return creds, nil
}

func (f *fileStore) Delete() error {
	if err := os.Remove(f.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete credentials: %w", err)
	}
	return nil
}
