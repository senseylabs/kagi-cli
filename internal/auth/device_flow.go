package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// OIDCEndpoints holds the discovered OpenID Connect endpoints.
type OIDCEndpoints struct {
	DeviceAuthorizationEndpoint string `json:"device_authorization_endpoint"`
	TokenEndpoint               string `json:"token_endpoint"`
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
func NewDeviceFlow(issuerURL, clientID, scope string) *DeviceFlow {
	return &DeviceFlow{
		issuerURL: issuerURL,
		clientID:  clientID,
		scope:     scope,
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// DiscoverEndpoints fetches the OIDC well-known configuration.
func (d *DeviceFlow) DiscoverEndpoints() (*OIDCEndpoints, error) {
	wellKnownURL := d.issuerURL + "/.well-known/openid-configuration"

	resp, err := d.client.Get(wellKnownURL)
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

	resp, err := d.client.PostForm(deviceAuthEndpoint, data)
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

		resp, err := d.client.PostForm(tokenEndpoint, data)
		if err != nil {
			return nil, fmt.Errorf("token request failed: %w", err)
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
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
func (d *DeviceFlow) RefreshToken(tokenEndpoint, refreshToken string) (*TokenResponse, error) {
	data := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {d.clientID},
		"refresh_token": {refreshToken},
	}

	resp, err := d.client.Post(tokenEndpoint, "application/x-www-form-urlencoded", strings.NewReader(data.Encode()))
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
