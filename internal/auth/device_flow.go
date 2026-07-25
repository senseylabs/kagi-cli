package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/senseylabs/kagi-cli/internal/httpx"
)

// DefaultScope is the OAuth scope requested by the Kagi CLI.
// offline_access asks Keycloak for a refresh token bound to the realm's
// offline-session lifetime rather than the user's SSO session.
const DefaultScope = "openid offline_access"

// deviceRequestTimeout bounds each single-shot device-flow POST (device
// authorization and each token poll). It replaces the former http.Client.Timeout
// (removed so discovery's per-attempt context can own its own timeout) so these
// requests keep a bound rather than hanging indefinitely.
const deviceRequestTimeout = 15 * time.Second

// maxPollInterval caps how large the poll interval may grow. RFC 8628 tells the
// client to lengthen the interval on every slow_down response, but a server that
// keeps returning slow_down would otherwise push the interval up without bound
// and eventually poll only once per expiry window. Capping keeps polling
// responsive while still backing off.
const maxPollInterval = 30 * time.Second

// nextSlowDownInterval grows the poll interval by 5 seconds (RFC 8628) in
// response to a slow_down, clamped to maxPollInterval so it can never grow
// without bound.
func nextSlowDownInterval(interval time.Duration) time.Duration {
	interval += 5 * time.Second
	if interval > maxPollInterval {
		interval = maxPollInterval
	}
	return interval
}

// maxPollTransientErrors bounds how many consecutive transient network failures
// PollForToken tolerates before giving up. The expiry deadline already bounds
// total time; this stops a hard-down endpoint from silently burning the whole
// budget one failed request at a time.
const maxPollTransientErrors = 10

// TokenEndpointError is a concrete, typed error returned by RefreshToken (and
// available to future callers) that carries enough structure to tell a real
// authentication failure (invalid_grant / HTTP 4xx → the user must re-login)
// apart from a transient one (5xx or a transport failure → worth retrying).
type TokenEndpointError struct {
	// Status is the HTTP status code, or 0 for a transport-level failure that
	// never produced a response.
	Status int
	// Code is the OAuth2 error code (e.g. "invalid_grant"), or a synthetic code
	// like "transport" when the request never reached the server.
	Code string
	// Description is the human-readable error_description or raw detail.
	Description string
	// err is the underlying cause, exposed via Unwrap for errors.Is/As.
	err error
}

func (e *TokenEndpointError) Error() string {
	switch {
	case e.Code != "" && e.Description != "":
		return fmt.Sprintf("token endpoint error (status %d): %s - %s", e.Status, e.Code, e.Description)
	case e.Code != "":
		return fmt.Sprintf("token endpoint error (status %d): %s", e.Status, e.Code)
	default:
		return fmt.Sprintf("token endpoint error (status %d)", e.Status)
	}
}

// Unwrap exposes the underlying transport error, if any.
func (e *TokenEndpointError) Unwrap() error { return e.err }

// Transient reports whether the failure looks worth retrying: a transport
// failure (Status 0, or a body-read failure that never yielded a usable
// response — both carry Code "transport") or any server-side 5xx. A 4xx —
// most importantly invalid_grant / HTTP 400 — is a real auth failure and is
// not transient. Note the "transport" check: a connection reset mid-body can
// arrive after a non-5xx status line, and that is still a transport blip worth
// retrying, not a reason to force the user to re-login.
func (e *TokenEndpointError) Transient() bool {
	return e.Status == 0 || e.Status >= 500 || e.Code == "transport"
}

// OIDCEndpoints holds the discovered OpenID Connect endpoints.
type OIDCEndpoints struct {
	DeviceAuthorizationEndpoint string `json:"device_authorization_endpoint"`
	TokenEndpoint               string `json:"token_endpoint"`
	RevocationEndpoint          string `json:"revocation_endpoint"`
}

// DeviceAuthResponse represents the device authorization response.
type DeviceAuthResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// TokenResponse represents the token endpoint response.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}

// TokenErrorResponse represents an error from the token endpoint.
type TokenErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// DeviceFlow handles the Keycloak Device Authorization Grant (RFC 8628).
type DeviceFlow struct {
	issuerURL string
	clientID  string
	scope     string
	client    *http.Client
}

// NewDeviceFlow creates a new DeviceFlow instance.
//
// The http.Client carries no Timeout: discovery's per-attempt timeout is owned
// by the context inside httpx.GetWithRetry, and the single-shot POSTs bound
// themselves via deviceRequestTimeout. Transport is left nil on purpose so
// http.DefaultTransport keeps honoring ProxyFromEnvironment and HTTP/2 — both
// were verified innocent while diagnosing the cold-start failure.
func NewDeviceFlow(issuerURL, clientID, scope string) *DeviceFlow {
	return &DeviceFlow{
		issuerURL: issuerURL,
		clientID:  clientID,
		scope:     scope,
		client:    &http.Client{},
	}
}

// DiscoverEndpoints fetches the OIDC well-known configuration, retrying through
// httpx.GetWithRetry so a transient Keycloak restart (the origin cold-booting
// with zero ready endpoints) costs a few seconds of waiting rather than a hard
// failure. The caller owns the overall time budget via ctx.
func (d *DeviceFlow) DiscoverEndpoints(ctx context.Context) (*OIDCEndpoints, error) {
	wellKnownURL := d.issuerURL + "/.well-known/openid-configuration"

	opts := httpx.DefaultOptions()
	opts.OnRetry = func(attempt, max int) {
		// Progress from the 2nd attempt onward only, so the happy path stays
		// silent. GetWithRetry calls this before each retry.
		fmt.Fprintf(os.Stderr, "Auth service not responding, retrying (%d/%d)...\n", attempt, max)
	}

	resp, err := httpx.GetWithRetry(ctx, d.client, wellKnownURL, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch OIDC configuration from %s: %w", wellKnownURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OIDC configuration endpoint returned %d", resp.StatusCode)
	}

	var endpoints OIDCEndpoints
	if err := json.NewDecoder(resp.Body).Decode(&endpoints); err != nil {
		return nil, fmt.Errorf("failed to parse OIDC configuration: %w", err)
	}

	if endpoints.DeviceAuthorizationEndpoint == "" {
		return nil, fmt.Errorf("device_authorization_endpoint not found in OIDC configuration")
	}
	if endpoints.TokenEndpoint == "" {
		return nil, fmt.Errorf("token_endpoint not found in OIDC configuration")
	}

	return &endpoints, nil
}

// RequestDeviceAuthorization initiates the device authorization flow.
func (d *DeviceFlow) RequestDeviceAuthorization(deviceAuthEndpoint string) (*DeviceAuthResponse, error) {
	data := url.Values{
		"client_id": {d.clientID},
		"scope":     {d.scope},
	}

	ctx, cancel := context.WithTimeout(context.Background(), deviceRequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, deviceAuthEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create device authorization request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("device authorization request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read device authorization response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device authorization failed with status %d: %s", resp.StatusCode, string(body))
	}

	var deviceResp DeviceAuthResponse
	if err := json.Unmarshal(body, &deviceResp); err != nil {
		return nil, fmt.Errorf("failed to parse device authorization response: %w", err)
	}

	return &deviceResp, nil
}

// PollForToken polls the token endpoint until the user completes authentication.
//
// It delegates to PollForTokenContext with a background context so the existing
// signature (used by cmd/login.go) stays stable; new callers that want
// cancellation should call PollForTokenContext directly.
func (d *DeviceFlow) PollForToken(tokenEndpoint, deviceCode string, interval time.Duration, expiresAt time.Time) (*TokenResponse, error) {
	return d.PollForTokenContext(context.Background(), tokenEndpoint, deviceCode, interval, expiresAt)
}

// PollForTokenContext polls the token endpoint until the user completes
// authentication, the device code expires, or ctx is canceled.
//
// Transient failures do not abort the flow: they are tolerated up to
// maxPollTransientErrors consecutive occurrences within the expiry budget, so a
// momentary blip while the user is authorizing doesn't kill the login. Three
// kinds count against that budget: a request that never got a response, a body
// that could not be read, and a response that arrived with a 5xx status. That
// last case matters because a proxy in front of Keycloak can return a bare HTML
// 502/503 mid-flow; treating it as fatal would abort an otherwise healthy login.
// This mirrors httpx.GetWithRetry (which retries 502/503/504) and RefreshToken's
// TokenEndpointError.Transient() (which retries any >=500). Only a <500 non-200
// OAuth response (authorization_pending / slow_down / access_denied /
// expired_token / a definitive 4xx) is acted on immediately. The poll interval
// is grown on slow_down per RFC 8628 but capped at maxPollInterval so a
// misbehaving server cannot push it up without bound.
func (d *DeviceFlow) PollForTokenContext(ctx context.Context, tokenEndpoint, deviceCode string, interval time.Duration, expiresAt time.Time) (*TokenResponse, error) {
	data := url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"client_id":   {d.clientID},
		"device_code": {deviceCode},
	}

	transientErrors := 0

	for {
		if time.Now().After(expiresAt) {
			return nil, fmt.Errorf("device authorization expired. Please try again")
		}

		// Sleep for the interval, but wake early if the caller cancels.
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}

		reqCtx, cancel := context.WithTimeout(ctx, deviceRequestTimeout)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, tokenEndpoint, strings.NewReader(data.Encode()))
		if err != nil {
			cancel()
			return nil, fmt.Errorf("failed to create token request: %w", err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		resp, err := d.client.Do(req)
		if err != nil {
			cancel()
			// Honor caller cancellation immediately; only tolerate genuine
			// transient failures.
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			transientErrors++
			if transientErrors >= maxPollTransientErrors {
				return nil, fmt.Errorf("token request failed after %d attempts: %w", transientErrors, err)
			}
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			transientErrors++
			if transientErrors >= maxPollTransientErrors {
				return nil, fmt.Errorf("failed to read token response after %d attempts: %w", transientErrors, err)
			}
			continue
		}

		if resp.StatusCode == http.StatusOK {
			// A definitive success reached us; parse the token and return.
			var tokenResp TokenResponse
			if err := json.Unmarshal(body, &tokenResp); err != nil {
				return nil, fmt.Errorf("failed to parse token response: %w", err)
			}
			return &tokenResp, nil
		}

		// A 5xx that arrived is a server-side blip (a proxy's bare HTML 502/503,
		// or a JSON 5xx), not an OAuth decision the user can act on. Count it
		// against the same transientErrors budget as a transport failure and keep
		// polling within the expiry deadline. Without this, an HTML 5xx body fails
		// json.Unmarshal below and hits "unexpected response", and a JSON 5xx falls
		// through to the fatal default — either one aborting a healthy login.
		if resp.StatusCode >= 500 {
			transientErrors++
			if transientErrors >= maxPollTransientErrors {
				return nil, fmt.Errorf("token endpoint returned server error %d after %d attempts: %s",
					resp.StatusCode, transientErrors, strings.TrimSpace(string(body)))
			}
			continue
		}

		// A definitive <500 non-200 OAuth response reached us; reset the transient
		// budget and interpret the error code.
		transientErrors = 0

		var errResp TokenErrorResponse
		if err := json.Unmarshal(body, &errResp); err != nil {
			return nil, fmt.Errorf("unexpected response from token endpoint: %s", string(body))
		}

		switch errResp.Error {
		case "authorization_pending":
			// Continue polling
			continue
		case "slow_down":
			// Increase the interval per RFC 8628, but never past the cap so a
			// repeated slow_down cannot grow it without bound.
			interval = nextSlowDownInterval(interval)
			continue
		case "expired_token":
			return nil, fmt.Errorf("device code expired. Please try again")
		case "access_denied":
			return nil, fmt.Errorf("access denied by user")
		default:
			return nil, fmt.Errorf("token error: %s - %s", errResp.Error, errResp.ErrorDescription)
		}
	}
}

// RefreshToken uses a refresh token to obtain a new access token.
// The caller controls the timeout via ctx.
func (d *DeviceFlow) RefreshToken(ctx context.Context, tokenEndpoint, refreshToken string) (*TokenResponse, error) {
	data := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {d.clientID},
		"refresh_token": {refreshToken},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := d.client.Do(req)
	if err != nil {
		// No response reached us: a transport-level failure (Status 0), which
		// Transient() classifies as retryable.
		return nil, &TokenEndpointError{
			Status:      0,
			Code:        "transport",
			Description: err.Error(),
			err:         err,
		}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &TokenEndpointError{
			Status:      resp.StatusCode,
			Code:        "transport",
			Description: fmt.Sprintf("failed to read refresh response: %v", err),
			err:         err,
		}
	}

	if resp.StatusCode != http.StatusOK {
		// Surface the status and the OAuth error code (if the body carried one)
		// so callers can tell invalid_grant / 4xx (re-login) apart from 5xx
		// (transient) via TokenEndpointError.Transient / errors.As.
		var errResp TokenErrorResponse
		_ = json.Unmarshal(body, &errResp)
		code := errResp.Error
		desc := errResp.ErrorDescription
		if code == "" {
			// No structured body; fall back to the raw payload for context.
			desc = strings.TrimSpace(string(body))
		}
		return nil, &TokenEndpointError{
			Status:      resp.StatusCode,
			Code:        code,
			Description: desc,
		}
	}

	var tokenResp TokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse refresh response: %w", err)
	}

	return &tokenResp, nil
}

// RevokeToken asks the IdP to revoke the given refresh token per RFC 7009.
// The caller controls the timeout via ctx; RFC 7009 allows the server to respond
// with either HTTP 200 or 204 on success.
func (d *DeviceFlow) RevokeToken(ctx context.Context, revocationEndpoint, refreshToken string) error {
	data := url.Values{
		"client_id":       {d.clientID},
		"token":           {refreshToken},
		"token_type_hint": {"refresh_token"},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, revocationEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return fmt.Errorf("failed to create revocation request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("revocation request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent {
		return nil
	}

	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("revocation failed with status %d: %s", resp.StatusCode, string(body))
}
