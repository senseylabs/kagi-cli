// Package httpx holds small HTTP helpers shared across the CLI. The retry
// helper here exists for one concrete failure: auth.kagi.pw is a single-replica
// Keycloak that cold-boots for over a minute after any restart, during which
// the Service has zero endpoints and connections hang. A single-shot GET with a
// fixed client timeout turns that transient window into a hard `kagi login`
// failure. GetWithRetry lets a transient restart cost a few seconds of waiting
// instead.
package httpx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Default retry tuning. These are the production values; tests inject shorter
// ones through Options so the suite runs in milliseconds rather than minutes.
const (
	// DefaultPerAttemptTimeout bounds a single HTTP attempt.
	DefaultPerAttemptTimeout = 15 * time.Second
	// DefaultInitialBackoff is the first inter-attempt sleep; it doubles each
	// retry up to DefaultMaxBackoff (1s, 2s, 4s, 8s, 8s, ...).
	DefaultInitialBackoff = 1 * time.Second
	// DefaultMaxBackoff caps the exponential backoff.
	DefaultMaxBackoff = 8 * time.Second
	// DefaultOverallBudget is only used to estimate the displayed attempt count
	// when the caller's context carries no deadline. The real overall budget is
	// always the context deadline the caller supplies.
	DefaultOverallBudget = 90 * time.Second
	// maxRetryAfter caps how long a server-supplied Retry-After header is honored.
	// A larger (or malicious) value is ignored in favour of the computed backoff
	// so a single response can't park the whole budget on one sleep.
	maxRetryAfter = 2 * time.Minute
)

// ErrRetryBudgetExhausted marks the terminal error GetWithRetry returns when the
// overall time budget (the caller's context deadline) runs out before a usable
// response arrives. Callers can errors.Is against it to distinguish a genuine
// "auth service unreachable" from a definitive answer like a 404 wrong-issuer.
var ErrRetryBudgetExhausted = errors.New("retry budget exhausted")

// Options tunes GetWithRetry. Zero-valued duration fields fall back to the
// package defaults, so a caller may set only the fields it cares about
// (typically just OnRetry).
type Options struct {
	// PerAttemptTimeout bounds a single HTTP attempt via context.WithTimeout.
	PerAttemptTimeout time.Duration
	// InitialBackoff is the first inter-attempt sleep; it doubles up to
	// MaxBackoff.
	InitialBackoff time.Duration
	// MaxBackoff caps the exponential backoff.
	MaxBackoff time.Duration
	// OnRetry, when set, is invoked before each retry (from the 2nd attempt
	// onward) with the 1-based attempt number about to run and an estimate of
	// the maximum attempts that fit the budget. It lets the caller surface
	// progress without coupling this package to stdio.
	OnRetry func(attempt, max int)
	// jitterSrc returns a value in [0,1) used to spread the backoff sleep across
	// clients (full jitter), killing the lock-step thundering herd that fixed
	// exponential backoff creates when many callers retry the same cold origin.
	// It is unexported so it stays an internal testing seam: production uses a
	// real random source, and tests inject a deterministic one. A nil source
	// falls back to the default.
	jitterSrc func() float64
}

// DefaultOptions returns the production retry tuning.
func DefaultOptions() Options {
	return Options{
		PerAttemptTimeout: DefaultPerAttemptTimeout,
		InitialBackoff:    DefaultInitialBackoff,
		MaxBackoff:        DefaultMaxBackoff,
		jitterSrc:         defaultJitterSource,
	}
}

// defaultJitterSource is the production jitter source, backed by the standard
// library's concurrency-safe global rand. It returns a value in [0,1).
func defaultJitterSource() float64 { return rand.Float64() }

// GetWithRetry issues GET requests against url, retrying transient failures
// until it gets a definitive response or the caller's context deadline (the
// overall budget) is reached. Each attempt is independently bounded by
// opts.PerAttemptTimeout, kept separate from the overall budget so a slow cold
// boot times out an attempt without ending the whole effort.
//
// It retries only on conditions consistent with an origin that is briefly down:
// per-attempt timeouts, connection refused/reset, and HTTP 429/502/503/504. Any
// other response (200, 404, 401, 500, ...) is returned to the caller as-is; the
// caller decides how to treat the status. In particular a 404 on a well-known
// path (a wrong issuer) returns immediately rather than burning the budget.
//
// Between retries it sleeps a full-jittered exponential backoff (random in
// [0, backoff]) to avoid a lock-step thundering herd. When a retryable response
// carries a sane Retry-After header (delta-seconds or an HTTP-date), that value
// is honored in place of the computed backoff.
//
// The caller SHOULD supply a context with a deadline; that deadline is the
// overall budget. On exhaustion the returned error wraps ErrRetryBudgetExhausted
// and the last underlying cause. If instead the caller cancels the context, the
// returned error wraps context.Canceled (not ErrRetryBudgetExhausted) so a
// deliberate cancellation is not misreported as an unreachable auth service.
func GetWithRetry(ctx context.Context, client *http.Client, url string, opts ...Options) (*http.Response, error) {
	o := resolveOptions(opts)

	budget := budgetFrom(ctx)
	maxAttempts := estimateMaxAttempts(budget, o.PerAttemptTimeout, o.InitialBackoff, o.MaxBackoff)

	backoff := o.InitialBackoff
	var lastErr error

	for attempt := 1; ; attempt++ {
		if attempt > 1 && o.OnRetry != nil {
			display := maxAttempts
			if attempt > display {
				// Never render an attempt number above the estimate (can happen
				// when attempts fail fast rather than consuming their full
				// per-attempt window).
				display = attempt
			}
			o.OnRetry(attempt, display)
		}

		resp, err := doAttempt(ctx, client, url, o.PerAttemptTimeout)
		// A server-supplied Retry-After overrides the computed backoff when the
		// response is retryable and the value is sane; -1 means "use backoff".
		wait := time.Duration(-1)
		switch {
		case err != nil:
			// The overall budget (or a caller cancellation) is terminal — never
			// retry past it, even though the per-attempt timeout is itself a
			// retryable condition.
			if ctx.Err() != nil {
				return nil, terminalContextErr(ctx, url, orDefault(lastErr, err))
			}
			if !IsRetryable(err) {
				return nil, err
			}
			lastErr = err
		case isRetryableStatus(resp.StatusCode):
			if d, ok := retryAfterDelay(resp.Header, time.Now()); ok {
				wait = d
			}
			drainAndClose(resp.Body)
			lastErr = fmt.Errorf("%s returned HTTP %d", url, resp.StatusCode)
		default:
			// Success or a definitive status. Hand the response back untouched.
			return resp, nil
		}

		// A retryable failure. Sleep the (jittered) backoff or the honored
		// Retry-After, unless the budget runs out first.
		if wait < 0 {
			wait = jitterBackoff(backoff, o.jitterSrc)
		}
		if sleepErr := sleepWithContext(ctx, wait); sleepErr != nil {
			return nil, terminalContextErr(ctx, url, orDefault(lastErr, sleepErr))
		}
		backoff = nextBackoff(backoff, o.MaxBackoff)
	}
}

// IsRetryable reports whether err represents a transient network condition worth
// retrying: a timeout (per-attempt or otherwise) or a connection-level failure
// (refused/reset). It deliberately does NOT treat context.Canceled or a
// permanent DNS failure as retryable. This is the single source of truth reused
// by the CLI's request paths so timeout classification lives in one place.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	// Per-attempt timeout, or any wrapped deadline.
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if os.IsTimeout(err) {
		return true
	}
	// Connection-level failures: refused (origin not accepting yet) or reset
	// mid-flight while the pod cycles.
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	// net.Error timeouts that os.IsTimeout may not recognise (e.g. dial timeout).
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return false
}

// resolveOptions returns production defaults merged with the first caller-
// supplied Options (if any). Zero-valued durations fall back to defaults.
func resolveOptions(opts []Options) Options {
	o := DefaultOptions()
	if len(opts) == 0 {
		return o
	}
	in := opts[0]
	if in.PerAttemptTimeout > 0 {
		o.PerAttemptTimeout = in.PerAttemptTimeout
	}
	if in.InitialBackoff > 0 {
		o.InitialBackoff = in.InitialBackoff
	}
	if in.MaxBackoff > 0 {
		o.MaxBackoff = in.MaxBackoff
	}
	if in.jitterSrc != nil {
		o.jitterSrc = in.jitterSrc
	}
	if o.jitterSrc == nil {
		o.jitterSrc = defaultJitterSource
	}
	o.OnRetry = in.OnRetry
	return o
}

// doAttempt issues one GET bounded by perAttempt. The per-attempt context must
// outlive this function so the caller can stream resp.Body, so we do not defer
// its cancel here; instead the returned body cancels the context on Close,
// guaranteeing the timer is released.
func doAttempt(ctx context.Context, client *http.Client, url string, perAttempt time.Duration) (*http.Response, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, perAttempt)

	req, err := http.NewRequestWithContext(attemptCtx, http.MethodGet, url, nil)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to build request for %s: %w", url, err)
	}

	resp, err := client.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}

	resp.Body = &cancelBody{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

// cancelBody ties a per-attempt context's cancel func to the response body's
// lifetime: closing the body releases the context timer.
type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

// drainAndClose discards a bounded amount of a retryable response body so the
// underlying connection can be reused, then closes it. Drain/close errors are
// intentionally ignored: this body is being thrown away before a retry, so a
// failure here only forgoes connection reuse and has nothing to surface.
func drainAndClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 64*1024))
	_ = body.Close()
}

// sleepWithContext sleeps for d or returns early with ctx.Err() if the context
// is done first (budget exhausted or caller cancelled).
func sleepWithContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isRetryableStatus(code int) bool {
	return code == http.StatusTooManyRequests ||
		code == http.StatusBadGateway ||
		code == http.StatusServiceUnavailable ||
		code == http.StatusGatewayTimeout
}

func nextBackoff(current, max time.Duration) time.Duration {
	next := current * 2
	if next > max {
		return max
	}
	return next
}

// jitterBackoff applies full jitter to the deterministic backoff: it returns a
// random duration in [0, base], using src for the random fraction. Spreading
// the sleep this way prevents many clients that failed together from retrying
// in lock-step. A nil src or non-positive base degrades to base.
func jitterBackoff(base time.Duration, src func() float64) time.Duration {
	if base <= 0 {
		return 0
	}
	if src == nil {
		return base
	}
	frac := src()
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	return time.Duration(frac * float64(base))
}

// retryAfterDelay parses a Retry-After header (RFC 9110): either delta-seconds
// or an HTTP-date. It reports the delay and true only when the value is present,
// well-formed, non-negative and within maxRetryAfter; otherwise the caller falls
// back to the computed backoff. now is passed in so HTTP-date math is testable.
func retryAfterDelay(h http.Header, now time.Time) (time.Duration, bool) {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil {
		d := time.Duration(secs) * time.Second
		if d < 0 || d > maxRetryAfter {
			return 0, false
		}
		return d, true
	}
	if t, err := http.ParseTime(v); err == nil {
		d := t.Sub(now)
		if d < 0 {
			d = 0
		}
		if d > maxRetryAfter {
			return 0, false
		}
		return d, true
	}
	return 0, false
}

// budgetFrom returns the remaining overall budget implied by the context
// deadline, or DefaultOverallBudget when the context carries no deadline (used
// only to estimate the displayed attempt count).
func budgetFrom(ctx context.Context) time.Duration {
	deadline, ok := ctx.Deadline()
	if !ok {
		return DefaultOverallBudget
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return 0
	}
	return remaining
}

// estimateMaxAttempts computes how many attempts fit the budget assuming the
// worst case where each attempt consumes its full per-attempt timeout. It drives
// the "(n/max)" progress display; the real loop is bounded by the context, not
// this estimate.
func estimateMaxAttempts(budget, perAttempt, initialBackoff, maxBackoff time.Duration) int {
	if budget <= 0 || perAttempt <= 0 {
		return 1
	}
	attempts := 0
	var elapsed time.Duration
	backoff := initialBackoff
	for {
		elapsed += perAttempt
		attempts++
		if elapsed >= budget {
			return attempts
		}
		elapsed += backoff
		if elapsed >= budget {
			return attempts
		}
		backoff = nextBackoff(backoff, maxBackoff)
	}
}

func orDefault(primary, fallback error) error {
	if primary != nil {
		return primary
	}
	return fallback
}

func wrapBudgetExhausted(url string, cause error) error {
	if cause == nil {
		return fmt.Errorf("%w while contacting %s", ErrRetryBudgetExhausted, url)
	}
	return fmt.Errorf("%w while contacting %s: %w", ErrRetryBudgetExhausted, url, cause)
}

// terminalContextErr classifies a done parent context. A deliberate caller
// cancellation is reported distinctly (wrapping context.Canceled) so it is not
// confused with a genuine "auth service unreachable"; a deadline that fired is
// the overall budget running out and wraps ErrRetryBudgetExhausted as before.
func terminalContextErr(ctx context.Context, url string, cause error) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return fmt.Errorf("request to %s canceled: %w", url, context.Canceled)
	}
	return wrapBudgetExhausted(url, cause)
}
