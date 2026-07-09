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
		t.Fatalf("short budget not honoured: %s (budget %s)", elapsed, shortBudget)
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
