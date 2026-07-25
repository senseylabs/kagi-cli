package httpx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// fastOptions shrinks the per-attempt timeout and backoff so the suite runs in
// milliseconds instead of the production 15s/1-8s schedule.
func fastOptions(onRetry func(attempt, max int)) Options {
	return Options{
		PerAttemptTimeout: 100 * time.Millisecond,
		InitialBackoff:    10 * time.Millisecond,
		MaxBackoff:        20 * time.Millisecond,
		OnRetry:           onRetry,
	}
}

// TestGetWithRetry_TimeoutThenSuccess reproduces the reported bug: the first
// attempt hangs past the per-attempt timeout, the second succeeds.
func TestGetWithRetry_TimeoutThenSuccess(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			// Sleep well past the 100ms per-attempt timeout, but bail if the
			// client cancels so we don't leak the handler past server shutdown.
			select {
			case <-time.After(500 * time.Millisecond):
			case <-r.Context().Done():
			}
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var retries int32
	resp, err := GetWithRetry(ctx, srv.Client(), srv.URL, fastOptions(func(attempt, max int) {
		atomic.AddInt32(&retries, 1)
	}))
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Fatalf("expected body %q, got %q", "ok", string(body))
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected 2 server calls, got %d", got)
	}
	if got := atomic.LoadInt32(&retries); got != 1 {
		t.Fatalf("expected 1 retry notification, got %d", got)
	}
}

// TestGetWithRetry_503ThenSuccess covers retryable status codes: two 503s then
// a 200.
func TestGetWithRetry_503ThenSuccess(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, "cold boot")
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := GetWithRetry(ctx, srv.Client(), srv.URL, fastOptions(nil))
	if err != nil {
		t.Fatalf("expected success after retries, got error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("expected 3 server calls, got %d", got)
	}
}

// TestGetWithRetry_429ThenSuccess covers the newly-retryable 429: two 429s then
// a 200.
func TestGetWithRetry_429ThenSuccess(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, "slow down")
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := GetWithRetry(ctx, srv.Client(), srv.URL, fastOptions(nil))
	if err != nil {
		t.Fatalf("expected success after 429 retries, got error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("expected 3 server calls, got %d", got)
	}
}

// TestGetWithRetry_RetryAfterHonoured proves a Retry-After header on a 503 is
// used in place of the computed backoff: the fast backoff (10-20ms) would retry
// almost immediately, so a 1s Retry-After is observable as a ~1s gap before the
// second attempt fires.
func TestGetWithRetry_RetryAfterHonoured(t *testing.T) {
	var calls int32
	var firstAt time.Time
	var gap time.Duration
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			firstAt = time.Now()
			w.Header().Set("Retry-After", "1") // delta-seconds
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		gap = time.Since(firstAt)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := GetWithRetry(ctx, srv.Client(), srv.URL, fastOptions(nil))
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	// The gap must reflect the honored 1s header, not the 10-20ms backoff.
	if gap < 900*time.Millisecond {
		t.Fatalf("Retry-After not honored: second attempt fired after %s, want ~1s", gap)
	}
}

// TestRetryAfterDelay unit-tests the header parser across delta-seconds,
// HTTP-date, and out-of-range/garbage inputs.
func TestRetryAfterDelay(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		header string
		want   time.Duration
		ok     bool
	}{
		{"absent", "", 0, false},
		{"delta zero", "0", 0, true},
		{"delta seconds", "5", 5 * time.Second, true},
		{"delta negative", "-1", 0, false},
		{"delta too large", "10000", 0, false},
		{"http date future", now.Add(30 * time.Second).UTC().Format(http.TimeFormat), 30 * time.Second, true},
		{"http date past", now.Add(-30 * time.Second).UTC().Format(http.TimeFormat), 0, true},
		{"garbage", "soon", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			if tc.header != "" {
				h.Set("Retry-After", tc.header)
			}
			got, ok := retryAfterDelay(h, now)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Fatalf("delay = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestJitterBackoff pins full jitter to its bounds using a deterministic
// injected source: for any fraction in [0,1] the sleep must land in [0, base].
func TestJitterBackoff(t *testing.T) {
	base := 800 * time.Millisecond
	for _, frac := range []float64{0, 0.001, 0.25, 0.5, 0.9999, 1} {
		got := jitterBackoff(base, func() float64 { return frac })
		if got < 0 || got > base {
			t.Fatalf("frac %v: jitter %s out of [0,%s]", frac, got, base)
		}
	}
	// Exact endpoints.
	if got := jitterBackoff(base, func() float64 { return 0 }); got != 0 {
		t.Fatalf("frac 0 should sleep 0, got %s", got)
	}
	if got := jitterBackoff(base, func() float64 { return 1 }); got != base {
		t.Fatalf("frac 1 should sleep base, got %s", got)
	}
	// Out-of-range fractions are clamped, never producing a negative or > base sleep.
	if got := jitterBackoff(base, func() float64 { return -5 }); got != 0 {
		t.Fatalf("negative frac should clamp to 0, got %s", got)
	}
	if got := jitterBackoff(base, func() float64 { return 5 }); got != base {
		t.Fatalf("frac > 1 should clamp to base, got %s", got)
	}
}

// TestGetWithRetry_JitterSourceInjected drives GetWithRetry with a deterministic
// jitter source (always 0 → retry immediately) to prove the injected seam is
// actually consulted on the backoff path and that a 503-then-200 still succeeds.
func TestGetWithRetry_JitterSourceInjected(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	opts := fastOptions(nil)
	// InitialBackoff of 10s would blow the test budget if actually slept; the
	// zero-fraction jitter must collapse it to ~0.
	opts.InitialBackoff = 10 * time.Second
	opts.MaxBackoff = 10 * time.Second
	opts.jitterSrc = func() float64 { return 0 }

	start := time.Now()
	resp, err := GetWithRetry(ctx, srv.Client(), srv.URL, opts)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	defer resp.Body.Close()
	if elapsed := time.Since(start); elapsed > 1*time.Second {
		t.Fatalf("zero-jitter backoff should retry near-immediately, took %s", elapsed)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected 2 server calls, got %d", got)
	}
}

// TestGetWithRetry_CallerCancellation proves a caller-canceled context is
// reported as context.Canceled — distinct from ErrRetryBudgetExhausted — so a
// deliberate Ctrl-C is not misreported as an unreachable auth service.
func TestGetWithRetry_CallerCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // never respond
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel shortly after the first attempt begins.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	defer cancel()

	_, err := GetWithRetry(ctx, srv.Client(), srv.URL, fastOptions(nil))
	if err == nil {
		t.Fatalf("expected an error on cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
	if errors.Is(err, ErrRetryBudgetExhausted) {
		t.Fatalf("cancellation must not be reported as budget exhaustion: %v", err)
	}
}

// TestGetWithRetry_404NoRetry proves a definitive 4xx (a wrong issuer) returns
// immediately without burning the budget.
func TestGetWithRetry_404NoRetry(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	resp, err := GetWithRetry(ctx, srv.Client(), srv.URL, fastOptions(func(attempt, max int) {
		t.Fatalf("must not retry a 404 (attempt %d)", attempt)
	}))
	if err != nil {
		t.Fatalf("expected the 404 response, got error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected exactly 1 server call, got %d", got)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("404 should return fast, took %s", elapsed)
	}
}

// TestGetWithRetry_BudgetExhausted covers a server that never responds: the
// helper gives up at the overall budget with a wrapped, sentinel-tagged error.
func TestGetWithRetry_BudgetExhausted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // never respond until the client gives up
	}))
	defer srv.Close()

	budget := 300 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	start := time.Now()
	resp, err := GetWithRetry(ctx, srv.Client(), srv.URL, fastOptions(nil))
	elapsed := time.Since(start)

	if resp != nil {
		resp.Body.Close()
		t.Fatalf("expected nil response on exhaustion")
	}
	if err == nil {
		t.Fatalf("expected an error on exhaustion")
	}
	if !errors.Is(err, ErrRetryBudgetExhausted) {
		t.Fatalf("expected ErrRetryBudgetExhausted, got: %v", err)
	}
	// Should give up around the budget, not hang far past it.
	if elapsed > budget+1*time.Second {
		t.Fatalf("gave up too late: %s (budget %s)", elapsed, budget)
	}
}

// TestGetWithRetry_BudgetOverrideHonoured confirms a shorter overall budget is
// respected: a longer budget would keep retrying well past this point.
func TestGetWithRetry_BudgetOverrideHonoured(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	shortBudget := 150 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), shortBudget)
	defer cancel()

	start := time.Now()
	_, err := GetWithRetry(ctx, srv.Client(), srv.URL, fastOptions(nil))
	elapsed := time.Since(start)

	if !errors.Is(err, ErrRetryBudgetExhausted) {
		t.Fatalf("expected ErrRetryBudgetExhausted, got: %v", err)
	}
	if elapsed < shortBudget {
		t.Fatalf("gave up before the budget: %s < %s", elapsed, shortBudget)
	}
	if elapsed > shortBudget+500*time.Millisecond {
		t.Fatalf("short budget not honored: %s (budget %s)", elapsed, shortBudget)
	}
}

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"deadline exceeded", context.DeadlineExceeded, true},
		{"canceled", context.Canceled, false},
		{"plain error", errors.New("boom"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsRetryable(tc.err); got != tc.want {
				t.Fatalf("IsRetryable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestEstimateMaxAttempts pins the production display estimate to 5 (the value
// the "(n/5)" retry line shows on a cold start).
func TestEstimateMaxAttempts(t *testing.T) {
	got := estimateMaxAttempts(90*time.Second, 15*time.Second, 1*time.Second, 8*time.Second)
	if got != 5 {
		t.Fatalf("expected 5 estimated attempts for the 90s budget, got %d", got)
	}
}
