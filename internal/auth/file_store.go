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

	if err := os.WriteFile(f.path, data, 0600); err != nil {
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
