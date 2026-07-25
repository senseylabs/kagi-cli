package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

type fileStore struct {
	path string
	// locateErr is set when the store could not be located (home directory
	// unresolvable). It is surfaced from every operation so we never silently
	// write plaintext credentials to a relative path in the current directory.
	locateErr error
}

// newFileStore returns the plaintext file-backed fallback store used when no OS
// secret service is available. It is unexported: callers reach it through
// NewTokenStore, which decides between the keyring and this fallback.
func newFileStore() *fileStore {
	home, err := os.UserHomeDir()
	if err != nil {
		return &fileStore{locateErr: fmt.Errorf("cannot store credentials: home directory unavailable: %w", err)}
	}
	return &fileStore{
		path: filepath.Join(home, ".kagi", "credentials"),
	}
}

func (f *fileStore) Save(creds Credentials) error {
	if f.locateErr != nil {
		return f.locateErr
	}
	dir := filepath.Dir(f.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create credentials directory: %w", err)
	}

	data, err := json.MarshalIndent(creds, "", "  ") //nolint:gosec // serializing credentials for the local token store is this store's purpose; the file is written 0600
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
	// Harden the mode explicitly: this file holds plaintext credentials, and we
	// do not want to rely solely on os.CreateTemp's default. Chmod before the
	// rename so the target is never briefly world-readable.
	if err := os.Chmod(tmpName, 0600); err != nil {
		tmp.Close()
		return fmt.Errorf("failed to set credentials file mode: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close temp credentials file: %w", err)
	}
	if err := os.Rename(tmpName, f.path); err != nil {
		return fmt.Errorf("failed to write credentials file: %w", err)
	}
	// Belt-and-suspenders: enforce 0600 on the final path too, in case a
	// pre-existing target carried looser bits that survived the rename.
	if err := os.Chmod(f.path, 0600); err != nil {
		return fmt.Errorf("failed to set credentials file mode: %w", err)
	}
	return nil
}

func (f *fileStore) Load() (Credentials, error) {
	if f.locateErr != nil {
		return Credentials{}, f.locateErr
	}
	data, err := os.ReadFile(f.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// Keep the "no credentials found" substring and wrap both the
			// sentinel and the backend cause so errors.Is matches either.
			return Credentials{}, fmt.Errorf("no credentials found at %s: %w", f.path,
				errors.Join(ErrNoCredentials, err))
		}
		return Credentials{}, fmt.Errorf("failed to read credentials file %s: %w", f.path, err)
	}

	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return Credentials{}, fmt.Errorf("failed to parse credentials: %w", err)
	}
	return creds, nil
}

func (f *fileStore) Delete() error {
	if f.locateErr != nil {
		return f.locateErr
	}
	if err := os.Remove(f.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete credentials: %w", err)
	}
	return nil
}
