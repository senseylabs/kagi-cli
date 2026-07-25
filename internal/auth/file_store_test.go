package auth

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// newTempFileStore builds a fileStore rooted in a temp dir so tests exercise the
// plaintext fallback without touching the real ~/.kagi/credentials.
func newTempFileStore(t *testing.T) *fileStore {
	t.Helper()
	return &fileStore{path: filepath.Join(t.TempDir(), "credentials")}
}

func TestFileStoreLoadNoCredentials(t *testing.T) {
	fs := newTempFileStore(t)

	_, err := fs.Load()
	if err == nil {
		t.Fatal("expected an error loading from an empty store")
	}
	if !errors.Is(err, ErrNoCredentials) {
		t.Errorf("expected errors.Is(err, ErrNoCredentials); got %v", err)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected the underlying fs.ErrNotExist to remain in the chain; got %v", err)
	}
	// cmd/root.go still detects first-run via this substring until it migrates
	// to errors.Is — the contract must hold.
	if !strings.Contains(err.Error(), "no credentials found") {
		t.Errorf("expected message to contain %q; got %q", "no credentials found", err.Error())
	}
}

func TestFileStoreSaveLoadRoundTrip(t *testing.T) {
	store := newTempFileStore(t)

	want := Credentials{
		AccessToken:  "access-123",
		RefreshToken: "refresh-456",
		ExpiresAt:    time.Now().Add(time.Hour).Truncate(time.Second),
		IssuerURL:    "https://issuer.example",
		APIURL:       "https://api.example",
		DevMode:      true,
	}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.AccessToken != want.AccessToken || got.RefreshToken != want.RefreshToken ||
		got.IssuerURL != want.IssuerURL || got.APIURL != want.APIURL || got.DevMode != want.DevMode {
		t.Errorf("round trip mismatch: got %+v want %+v", got, want)
	}
	if !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Errorf("ExpiresAt mismatch: got %v want %v", got.ExpiresAt, want.ExpiresAt)
	}
}

func TestFileStoreSaveSetsMode0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix file modes not meaningful on windows")
	}
	store := newTempFileStore(t)
	if err := store.Save(Credentials{AccessToken: "a"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(store.path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("expected credentials file mode 0600; got %o", perm)
	}
}

func TestFileStoreDeleteMissingIsNoError(t *testing.T) {
	store := newTempFileStore(t)
	if err := store.Delete(); err != nil {
		t.Errorf("Delete on a missing file should be a no-op; got %v", err)
	}
}

// Sanity: the sentinel is wired so both backend causes match through it.
func TestErrNoCredentialsChain(t *testing.T) {
	// keyring Load path uses the same errors.Join shape; verify fs side here.
	store := newTempFileStore(t)
	_, err := store.Load()
	if !errors.Is(err, ErrNoCredentials) || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected chain to satisfy both sentinels; got %v", err)
	}
}
