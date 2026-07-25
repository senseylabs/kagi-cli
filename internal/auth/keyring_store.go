package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
// first-run with errors.Is regardless of which backend is active. cmd/root.go
// detects first-run via errors.Is(err, ErrNoCredentials); the message also keeps
// the literal "no credentials found" text for human readability.
var ErrNoCredentials = errors.New("no credentials found")

// keyringProbeTimeout bounds the availability probe of the OS secret service.
// go-keyring on Linux can block for seconds dialing a broken/absent D-Bus
// Secret Service before erroring, which would stall every command on a headless
// box; capping the probe keeps the fallback path fast rather than a hang.
const keyringProbeTimeout = 3 * time.Second

// keyringStore stores credentials in the OS secret service via go-keyring
// (macOS Keychain, Linux Secret Service / D-Bus, Windows Credential Manager).
type keyringStore struct{}

// NewTokenStore returns the best available credential store for this host.
//
// The backend choice is STICKY, so a transient secret-service outage cannot
// silently downgrade a keyring host to plaintext — which would either strand the
// real credentials in the keyring or write a divergent plaintext copy carrying a
// rotated refresh token. The decision is:
//
//   - Secret service reachable now → keyring store. Its first successful use
//     drops a sentinel marker (see markKeyringUsed) recording that this host has
//     used the keyring.
//   - Secret service unreachable now, but the marker says it has been used
//     before → a store whose every operation fails loudly with "could not reach
//     the OS secret service", rather than falling back to plaintext.
//   - Secret service unreachable and never used here → plaintext file store,
//     with a one-time warning and file mode 0600.
//
// The signature is intentionally non-fallible; the error surfaces from Load/Save
// instead. keyringAvailable is evaluated first so that, when it detects a live
// credential, the marker it writes is visible to the keyringEverUsed check.
func NewTokenStore() TokenStore {
	return selectTokenStore(keyringAvailable(), keyringEverUsed())
}

// selectTokenStore applies the sticky-backend decision described on
// NewTokenStore. It is split out (pure, no I/O beyond the plaintext warning) so
// the decision table can be unit-tested without a real secret service.
func selectTokenStore(available, everUsed bool) TokenStore {
	switch {
	case available:
		return &keyringStore{}
	case everUsed:
		// The keyring has held this host's credentials before but the secret
		// service is unreachable now. Refuse to fall back to plaintext.
		return &unavailableKeyringStore{}
	default:
		warnPlaintextFallback()
		return newFileStore()
	}
}

// errKeyringUnavailable is surfaced from every operation of
// unavailableKeyringStore. Its message keeps the literal "could not reach the OS
// secret service" text so the failure is self-explanatory on the command line.
var errKeyringUnavailable = errors.New(
	"could not reach the OS secret service, which has held this host's credentials before; " +
		"refusing to fall back to plaintext storage; retry once the secret service is back")

// unavailableKeyringStore is returned when the keyring backend has been used
// before (the sticky marker is present) but the OS secret service is currently
// unreachable. Every operation fails with errKeyringUnavailable rather than
// silently downgrading to the plaintext file, so a transient outage never
// strands credentials in the keyring nor writes a divergent plaintext copy with
// a rotated refresh token.
type unavailableKeyringStore struct{}

func (unavailableKeyringStore) Save(Credentials) error { return errKeyringUnavailable }
func (unavailableKeyringStore) Load() (Credentials, error) {
	return Credentials{}, errKeyringUnavailable
}
func (unavailableKeyringStore) Delete() error { return errKeyringUnavailable }

// keyringMarkerPath returns the path of the sticky sentinel file under ~/.kagi,
// or an error if the home directory cannot be resolved.
func keyringMarkerPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".kagi", "keyring-active"), nil
}

// markKeyringUsed records that the OS secret service has successfully held this
// host's credentials, making the backend choice sticky across invocations. It is
// best-effort: a failure to write the marker only costs stickiness on the next
// run, never a stored credential, so errors are intentionally swallowed.
func markKeyringUsed() {
	path, err := keyringMarkerPath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return
	}
	_ = os.WriteFile(path, []byte("1\n"), 0600)
}

// keyringEverUsed reports whether the sticky marker exists, i.e. whether the
// keyring backend has been used successfully on this host before.
func keyringEverUsed() bool {
	path, err := keyringMarkerPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
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
		if r.err == nil {
			// A live credential is present: this host has used the keyring. Drop
			// the sticky marker now so an existing credential (e.g. from before
			// this marker existed) makes the choice sticky even if the service
			// later fails, before any Save/Load has had a chance to write it.
			markKeyringUsed()
			return true
		}
		// ErrNotFound means the service works but holds no secret yet (available).
		// Anything else (ErrUnsupportedPlatform, a D-Bus dial failure, etc.) means
		// the secret service is not usable here.
		return errors.Is(r.err, keyring.ErrNotFound)
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
	data, err := json.Marshal(creds) //nolint:gosec // serializing credentials to store in the OS secret service is this store's purpose
	if err != nil {
		return fmt.Errorf("failed to serialize credentials: %w", err)
	}
	if err := keyring.Set(keychainService, keychainUser, string(data)); err != nil {
		return fmt.Errorf("failed to store credentials in keychain: %w", err)
	}
	// First successful use makes the keyring choice sticky for later invocations.
	markKeyringUsed()
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
	// A credential was read back: record the keyring as used so the choice stays
	// sticky even if the service is later unreachable.
	markKeyringUsed()
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
