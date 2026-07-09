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
// http.DefaultTransport keeps honouring ProxyFromEnvironment and HTTP/2 — both
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
func (d *DeviceFlow) PollForToken(tokenEndpoint, deviceCode string, interval time.Duration, expiresAt time.Time) (*TokenResponse, error) {
	data := url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"client_id":   {d.clientID},
		"device_code": {deviceCode},
	}

	for {
		if time.Now().After(expiresAt) {
			return nil, fmt.Errorf("device authorization expired. Please try again")
		}

		time.Sleep(interval)

		reqCtx, cancel := context.WithTimeout(context.Background(), deviceRequestTimeout)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, tokenEndpoint, strings.NewReader(data.Encode()))
		if err != nil {
			cancel()
			return nil, fmt.Errorf("failed to create token request: %w", err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		resp, err := d.client.Do(req)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("token request failed: %w", err)
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()
		if err != nil {
			return nil, fmt.Errorf("failed to read token response: %w", err)
		}

		if resp.StatusCode == http.StatusOK {
			var tokenResp TokenResponse
			if err := json.Unmarshal(body, &tokenResp); err != nil {
				return nil, fmt.Errorf("failed to parse token response: %w", err)
			}
			return &tokenResp, nil
		}

		var errResp TokenErrorResponse
		if err := json.Unmarshal(body, &errResp); err != nil {
			return nil, fmt.Errorf("unexpected response from token endpoint: %s", string(body))
		}

		switch errResp.Error {
		case "authorization_pending":
			// Continue polling
			continue
		case "slow_down":
			// Increase interval by 5 seconds per RFC 8628
			interval += 5 * time.Second
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
		return nil, fmt.Errorf("refresh token request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read refresh response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh failed with status %d: %s", resp.StatusCode, string(body))
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
