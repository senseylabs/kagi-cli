package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeRoundTripper lets a test drive the DeviceFlow HTTP client per-call,
// including synthesizing transport-level failures that httptest cannot easily
// produce.
type fakeRoundTripper struct {
	calls int
	fn    func(call int, req *http.Request) (*http.Response, error)
}

func (f *fakeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	f.calls++
	return f.fn(f.calls, req)
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func newFakeDeviceFlow(rt *fakeRoundTripper) *DeviceFlow {
	df := NewDeviceFlow("https://issuer.example", "kagi-cli", DefaultScope)
	df.client = &http.Client{Transport: rt}
	return df
}

// A single transport blip must not abort the login: PollForToken should keep
// trying within the expiry budget and eventually succeed.
func TestPollForTokenToleratesTransientErrors(t *testing.T) {
	rt := &fakeRoundTripper{
		fn: func(call int, req *http.Request) (*http.Response, error) {
			switch call {
			case 1, 2:
				return nil, fmt.Errorf("dial tcp: connection refused")
			default:
				return jsonResponse(http.StatusOK,
					`{"access_token":"at","refresh_token":"rt","token_type":"Bearer","expires_in":3600}`), nil
			}
		},
	}
	df := newFakeDeviceFlow(rt)

	resp, err := df.PollForToken("https://issuer.example/token", "devcode",
		time.Millisecond, time.Now().Add(10*time.Second))
	if err != nil {
		t.Fatalf("expected success after transient blips; got %v", err)
	}
	if resp.AccessToken != "at" {
		t.Errorf("unexpected access token %q", resp.AccessToken)
	}
	if rt.calls != 3 {
		t.Errorf("expected 3 attempts (2 transient + 1 success); got %d", rt.calls)
	}
}

// Persistent transport failure must give up after the bounded number of
// consecutive attempts rather than looping until expiry.
func TestPollForTokenGivesUpAfterMaxTransientErrors(t *testing.T) {
	rt := &fakeRoundTripper{
		fn: func(call int, req *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("network down")
		},
	}
	df := newFakeDeviceFlow(rt)

	_, err := df.PollForToken("https://issuer.example/token", "devcode",
		time.Millisecond, time.Now().Add(time.Hour))
	if err == nil {
		t.Fatal("expected an error after exhausting the transient budget")
	}
	if rt.calls != maxPollTransientErrors {
		t.Errorf("expected exactly %d attempts; got %d", maxPollTransientErrors, rt.calls)
	}
}

// authorization_pending must be treated as "keep polling", not an error.
func TestPollForTokenPendingThenSuccess(t *testing.T) {
	rt := &fakeRoundTripper{
		fn: func(call int, req *http.Request) (*http.Response, error) {
			if call < 2 {
				return jsonResponse(http.StatusBadRequest, `{"error":"authorization_pending"}`), nil
			}
			return jsonResponse(http.StatusOK, `{"access_token":"ok","expires_in":60}`), nil
		},
	}
	df := newFakeDeviceFlow(rt)

	resp, err := df.PollForToken("https://issuer.example/token", "devcode",
		time.Millisecond, time.Now().Add(10*time.Second))
	if err != nil {
		t.Fatalf("PollForToken: %v", err)
	}
	if resp.AccessToken != "ok" {
		t.Errorf("unexpected token %q", resp.AccessToken)
	}
}

// A cancelled context must stop polling promptly with the context error.
func TestPollForTokenContextCancellation(t *testing.T) {
	rt := &fakeRoundTripper{
		fn: func(call int, req *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusBadRequest, `{"error":"authorization_pending"}`), nil
		},
	}
	df := newFakeDeviceFlow(rt)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before the first sleep completes

	_, err := df.PollForTokenContext(ctx, "https://issuer.example/token", "devcode",
		time.Second, time.Now().Add(time.Hour))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled; got %v", err)
	}
	if rt.calls != 0 {
		t.Errorf("expected no HTTP calls after immediate cancellation; got %d", rt.calls)
	}
}

// nextSlowDownInterval must grow by 5s but clamp at the cap.
func TestNextSlowDownIntervalCap(t *testing.T) {
	cases := []struct {
		in, want time.Duration
	}{
		{5 * time.Second, 10 * time.Second},
		{20 * time.Second, 25 * time.Second},
		{maxPollInterval - 2*time.Second, maxPollInterval},
		{maxPollInterval, maxPollInterval},
		{maxPollInterval + time.Hour, maxPollInterval},
	}
	for _, c := range cases {
		if got := nextSlowDownInterval(c.in); got != c.want {
			t.Errorf("nextSlowDownInterval(%v) = %v; want %v", c.in, got, c.want)
		}
	}
}

// Repeated slow_down responses must not grow the interval past the cap, however
// many arrive.
func TestPollForTokenSlowDownStaysCapped(t *testing.T) {
	interval := 5 * time.Second
	for i := 0; i < 100; i++ {
		interval = nextSlowDownInterval(interval)
	}
	if interval != maxPollInterval {
		t.Errorf("after many slow_downs interval = %v; want cap %v", interval, maxPollInterval)
	}
}

// RefreshToken: HTTP 400 invalid_grant is a real auth failure (not transient).
func TestRefreshTokenInvalidGrantIsPermanent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":"invalid_grant","error_description":"Token is not active"}`)
	}))
	defer srv.Close()

	df := NewDeviceFlow("https://issuer.example", "kagi-cli", DefaultScope)
	_, err := df.RefreshToken(context.Background(), srv.URL, "stale-refresh")
	if err == nil {
		t.Fatal("expected an error")
	}
	var te *TokenEndpointError
	if !errors.As(err, &te) {
		t.Fatalf("expected *TokenEndpointError; got %T: %v", err, err)
	}
	if te.Status != http.StatusBadRequest {
		t.Errorf("Status = %d; want 400", te.Status)
	}
	if te.Code != "invalid_grant" {
		t.Errorf("Code = %q; want invalid_grant", te.Code)
	}
	if te.Transient() {
		t.Error("invalid_grant / 400 must not be classified as transient")
	}
}

// RefreshToken: HTTP 503 is a transient server failure.
func TestRefreshTokenServerErrorIsTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, "upstream cold start")
	}))
	defer srv.Close()

	df := NewDeviceFlow("https://issuer.example", "kagi-cli", DefaultScope)
	_, err := df.RefreshToken(context.Background(), srv.URL, "refresh")
	var te *TokenEndpointError
	if !errors.As(err, &te) {
		t.Fatalf("expected *TokenEndpointError; got %T: %v", err, err)
	}
	if te.Status != http.StatusServiceUnavailable {
		t.Errorf("Status = %d; want 503", te.Status)
	}
	if !te.Transient() {
		t.Error("503 must be classified as transient")
	}
}

// RefreshToken: a transport failure (no response) is transient, Status 0, and
// keeps the underlying error in the chain.
func TestRefreshTokenTransportErrorIsTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening now → connection refused

	df := NewDeviceFlow("https://issuer.example", "kagi-cli", DefaultScope)
	_, err := df.RefreshToken(context.Background(), url, "refresh")
	var te *TokenEndpointError
	if !errors.As(err, &te) {
		t.Fatalf("expected *TokenEndpointError; got %T: %v", err, err)
	}
	if te.Status != 0 {
		t.Errorf("Status = %d; want 0 for a transport failure", te.Status)
	}
	if !te.Transient() {
		t.Error("a transport failure must be transient")
	}
	if te.Unwrap() == nil {
		t.Error("expected the transport cause to be preserved via Unwrap")
	}
}

// RefreshToken: a successful 200 yields the token and no error.
func TestRefreshTokenSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"access_token":"new","refresh_token":"rot","token_type":"Bearer","expires_in":300}`)
	}))
	defer srv.Close()

	df := NewDeviceFlow("https://issuer.example", "kagi-cli", DefaultScope)
	resp, err := df.RefreshToken(context.Background(), srv.URL, "refresh")
	if err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	if resp.AccessToken != "new" || resp.RefreshToken != "rot" {
		t.Errorf("unexpected token response %+v", resp)
	}
}

func TestTokenEndpointErrorTransient(t *testing.T) {
	cases := []struct {
		name string
		err  TokenEndpointError
		want bool
	}{
		{"transport status 0", TokenEndpointError{Status: 0, Code: "transport"}, true},
		{"server 503", TokenEndpointError{Status: 503, Code: "temporarily_unavailable"}, true},
		{"invalid_grant 400", TokenEndpointError{Status: 400, Code: "invalid_grant"}, false},
		{"forbidden 403", TokenEndpointError{Status: 403, Code: "access_denied"}, false},
		// Body-read failure after a non-5xx status line (e.g. a connection reset
		// mid-body on a 200) keeps the real status but carries Code "transport";
		// it must be treated as transient, not "session expired".
		{"body-read failure on 200", TokenEndpointError{Status: 200, Code: "transport"}, true},
		{"body-read failure on 400", TokenEndpointError{Status: 400, Code: "transport"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Transient(); got != tc.want {
				t.Errorf("Transient() = %v, want %v", got, tc.want)
			}
		})
	}
}
