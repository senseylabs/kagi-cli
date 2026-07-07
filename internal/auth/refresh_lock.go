package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

const (
	// refreshLockTimeout is the total time we wait for a peer process to release
	// the lock before giving up. A healthy holder releases within ~30s (the
	// bounded discover + refresh HTTP calls), so this leaves generous headroom
	// while still failing loudly rather than hanging forever behind a wedged
	// holder (SIGSTOP'd, laptop suspended mid-syscall, etc.).
	refreshLockTimeout = 60 * time.Second
	// refreshLockQuietWait is how long we wait silently before telling the user we
	// are blocked on another process, so a sub-second handoff produces no noise.
	refreshLockQuietWait = 2 * time.Second
	// refreshLockRetryDelay is the poll interval while waiting for the lock.
	refreshLockRetryDelay = 100 * time.Millisecond
)

// RefreshLock is a cross-process advisory lock that serializes the
// load-refresh-store credential critical section. Without it, several `kagi`
// invocations started at once (e.g. one per app in a dev start script) all
// notice the access token is expired and stampede the refresh path
// simultaneously. On macOS that means concurrent `security add-generic-password`
// writes to the same keychain item, and the loser exits with errSecDuplicateItem
// (OSStatus -25299, surfaced as "exit status 45"). It also means the shared
// refresh token gets presented N times, which breaks under refresh-token
// rotation. Holding this lock guarantees exactly one refresh and one store.
type RefreshLock struct {
	fl *flock.Flock
}

// refreshLockPath returns ~/.kagi/refresh.lock, the well-known lock file all
// concurrent invocations contend on. It sits next to the other ~/.kagi state.
func refreshLockPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to resolve home directory: %w", err)
	}
	dir := filepath.Join(home, ".kagi")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("failed to create kagi config directory: %w", err)
	}
	return filepath.Join(dir, "refresh.lock"), nil
}

// AcquireRefreshLock waits until it holds the exclusive refresh lock, up to
// refreshLockTimeout. The returned RefreshLock must be released with Release
// (typically via defer). The wait is bounded so a wedged holder can never hang
// every other kagi invocation indefinitely with no output.
func AcquireRefreshLock() (*RefreshLock, error) {
	path, err := refreshLockPath()
	if err != nil {
		return nil, err
	}
	fl := flock.New(path)

	// Try quietly first — the common case is a sub-second handoff while a peer
	// finishes a refresh, and we don't want to print anything for that. Note
	// TryLockContext returns context.DeadlineExceeded (not a nil error) when the
	// window elapses without the lock, so a deadline here just means "keep going";
	// only a non-deadline error is a real failure to acquire.
	quietCtx, quietCancel := context.WithTimeout(context.Background(), refreshLockQuietWait)
	ok, err := fl.TryLockContext(quietCtx, refreshLockRetryDelay)
	quietCancel()
	if ok {
		return &RefreshLock{fl: fl}, nil
	}
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return nil, fmt.Errorf("failed to acquire refresh lock at %s: %w", path, err)
	}

	// Still blocked after the quiet window: tell the user why, then wait out the
	// rest of the budget.
	fmt.Fprintln(os.Stderr, "Waiting for another kagi process to refresh credentials...")
	ctx, cancel := context.WithTimeout(context.Background(), refreshLockTimeout-refreshLockQuietWait)
	defer cancel()
	ok, err = fl.TryLockContext(ctx, refreshLockRetryDelay)
	if ok {
		return &RefreshLock{fl: fl}, nil
	}
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return nil, fmt.Errorf("failed to acquire refresh lock at %s: %w", path, err)
	}
	return nil, fmt.Errorf("timed out after %s waiting for another kagi process to refresh credentials. "+
		"If no other kagi command is running, remove %s and retry", refreshLockTimeout, path)
}

// Release unlocks the refresh lock. Safe to call once; further calls are no-ops.
func (l *RefreshLock) Release() {
	if l == nil || l.fl == nil {
		return
	}
	_ = l.fl.Unlock()
	l.fl = nil
}
