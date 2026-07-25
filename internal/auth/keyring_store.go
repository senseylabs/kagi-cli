package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/zalando/go-keyring"
)

const (
	keychainService = "kagi-cli"
	keychainUser    = "default"
)

// ErrNoCredentials is the sentinel returned by a store's Load when no
// credentials have been saved yet. It wraps the underlying not-found errors of
// both backends (keyring.ErrNotFound and fs.ErrNotExist) so callers can detect
// first-run with errors.Is regardless of which backend is active. The returned
// error message keeps the literal substring "no credentials found" for the
// legacy substring check in cmd/root.go until that call migrates to errors.Is.
var ErrNoCredentials = errors.New("no credentials found")

// keyringProbeTimeout bounds the availability probe of the OS secret service.
// go-keyring on Linux can block for seconds dialling a broken/absent D-Bus
// Secret Service before erroring, which would stall every command on a headless
// box; capping the probe keeps the fallback path fast rather than a hang.
const keyringProbeTimeout = 3 * time.Second

// keyringStore stores credentials in the OS secret service via go-keyring
// (macOS Keychain, Linux Secret Service / D-Bus, Windows Credential Manager).
type keyringStore struct{}

// NewTokenStore returns the best available credential store for this host.
//
// It prefers the OS secret service (keyring). When no secret service is
// available — a headless Linux box with no D-Bus, or an unsupported platform —
// it falls back to a plaintext file store, prints a one-time warning, and
// hardens the file mode to 0600. The signature is intentionally non-fallible;
// a later wave introduces a fallible constructor.
func NewTokenStore() TokenStore {
	if keyringAvailable() {
		return &keyringStore{}
	}
	warnPlaintextFallback()
	return newFileStore()
}

// keyringAvailable reports whether the OS secret service can be reached. It
// performs a bounded Get probe: ErrNotFound means the service works but holds no
// secret yet (available); any infrastructure error — unsupported platform, a
// dead D-Bus session, or a probe that exceeds keyringProbeTimeout — means the
// service is unusable and the caller should fall back to file storage.
func keyringAvailable() bool {
	type result struct{ err error }
	ch := make(chan result, 1)
	go func() {
		_, err := keyring.Get(keychainService, keychainUser)
		ch <- result{err: err}
	}()

	select {
	case r := <-ch:
		// A working service returns either the secret (nil) or ErrNotFound.
		// Anything else (ErrUnsupportedPlatform, a D-Bus dial failure, etc.)
		// means the secret service is not usable here.
		return r.err == nil || errors.Is(r.err, keyring.ErrNotFound)
	case <-time.After(keyringProbeTimeout):
		// The probe wedged (typically a broken D-Bus Secret Service). Treat the
		// service as unavailable so we fall back rather than hang. The goroutine
		// is left to finish on its own; ch is buffered so it never leaks.
		return false
	}
}

var plaintextWarnOnce sync.Once

// warnPlaintextFallback prints a single stderr warning that credentials are
// being stored in plaintext because no OS secret service was reachable.
func warnPlaintextFallback() {
	plaintextWarnOnce.Do(func() {
		fmt.Fprintln(os.Stderr,
			"Warning: no OS secret service available; storing credentials in plaintext under ~/.kagi/credentials (mode 0600).")
	})
}

func (k *keyringStore) Save(creds Credentials) error {
	data, err := json.Marshal(creds)
	if err != nil {
		return fmt.Errorf("failed to serialize credentials: %w", err)
	}
	if err := keyring.Set(keychainService, keychainUser, string(data)); err != nil {
		return fmt.Errorf("failed to store credentials in keychain: %w", err)
	}
	return nil
}

func (k *keyringStore) Load() (Credentials, error) {
	data, err := keyring.Get(keychainService, keychainUser)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			// Keep the "no credentials found" substring and wrap both the
			// sentinel and the backend cause so errors.Is matches either.
			return Credentials{}, fmt.Errorf("no credentials found in keychain: %w",
				errors.Join(ErrNoCredentials, err))
		}
		return Credentials{}, fmt.Errorf("failed to read credentials from keychain: %w", err)
	}

	var creds Credentials
	if err := json.Unmarshal([]byte(data), &creds); err != nil {
		return Credentials{}, fmt.Errorf("failed to parse credentials from keychain: %w", err)
	}
	return creds, nil
}

func (k *keyringStore) Delete() error {
	if err := keyring.Delete(keychainService, keychainUser); err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("failed to delete credentials from keychain: %w", err)
	}
	return nil
}
