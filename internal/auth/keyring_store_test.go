package auth

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// selectTokenStore encodes the sticky-backend decision. Verify the full table
// without touching a real OS secret service.
func TestSelectTokenStoreStickiness(t *testing.T) {
	cases := []struct {
		name      string
		available bool
		everUsed  bool
		wantType  string
	}{
		{"available uses keyring", true, false, "*auth.keyringStore"},
		{"available wins even if used before", true, true, "*auth.keyringStore"},
		// The crux of FIX #5: unavailable + used-before must NOT fall back to the
		// plaintext file; it must surface the outage.
		{"unavailable but used before errors", false, true, "*auth.unavailableKeyringStore"},
		{"unavailable and never used falls back", false, false, "*auth.fileStore"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := selectTokenStore(tc.available, tc.everUsed)
			if gotType := typeName(got); gotType != tc.wantType {
				t.Errorf("selectTokenStore(%v, %v) = %s; want %s",
					tc.available, tc.everUsed, gotType, tc.wantType)
			}
		})
	}
}

// Once the sticky marker is present, a lost secret service must fail loudly on
// every operation rather than stranding credentials or writing plaintext.
func TestUnavailableKeyringStoreFailsLoudly(t *testing.T) {
	var s TokenStore = &unavailableKeyringStore{}

	if err := s.Save(Credentials{AccessToken: "a"}); err == nil ||
		!strings.Contains(err.Error(), "could not reach the OS secret service") {
		t.Errorf("Save error = %v; want one mentioning the secret service", err)
	}
	if _, err := s.Load(); err == nil ||
		!strings.Contains(err.Error(), "could not reach the OS secret service") {
		t.Errorf("Load error = %v; want one mentioning the secret service", err)
	}
	if err := s.Delete(); err == nil ||
		!strings.Contains(err.Error(), "could not reach the OS secret service") {
		t.Errorf("Delete error = %v; want one mentioning the secret service", err)
	}
}

// The marker helpers must round-trip through ~/.kagi and drive keyringEverUsed.
func TestKeyringMarkerRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}

	if keyringEverUsed() {
		t.Fatal("marker should be absent in a fresh home")
	}

	markKeyringUsed()

	if !keyringEverUsed() {
		t.Fatal("marker should be present after markKeyringUsed")
	}

	path := filepath.Join(home, ".kagi", "keyring-active")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("expected marker at %s: %v", path, err)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0600 {
			t.Errorf("marker mode = %o; want 0600", perm)
		}
	}
}

// selectTokenStore reads the marker that markKeyringUsed writes, so an
// unreachable service on a host that has used the keyring resolves to the
// error-surfacing store — the end-to-end sticky path.
func TestStickyMarkerDrivesStoreSelection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}

	// Before any keyring use, an unreachable service falls back to plaintext.
	if got := typeName(selectTokenStore(false, keyringEverUsed())); got != "*auth.fileStore" {
		t.Fatalf("fresh host: got %s; want *auth.fileStore", got)
	}

	// Simulate a first successful keyring use.
	markKeyringUsed()

	// Now an unreachable service must refuse to downgrade to plaintext.
	if got := typeName(selectTokenStore(false, keyringEverUsed())); got != "*auth.unavailableKeyringStore" {
		t.Fatalf("sticky host: got %s; want *auth.unavailableKeyringStore", got)
	}
}

// typeName returns the concrete type of v, e.g. "*auth.keyringStore".
func typeName(v interface{}) string {
	return fmt.Sprintf("%T", v)
}
