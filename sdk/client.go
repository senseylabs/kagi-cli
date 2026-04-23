package kagi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client is a read-only HTTP client for the Kagi secrets management API.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// NewClient creates a new Kagi SDK client.
//
// baseURL is the root URL of the Kagi API (e.g. "https://api.example.com").
// token is a Bearer token used for authentication.
func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: baseURL,
		token:   token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// ListProjects returns all projects accessible to the authenticated user.
func (c *Client) ListProjects(ctx context.Context) ([]Project, error) {
	var resp APIResponse[[]Project]
	if err := c.doGet(ctx, "/kagi/projects", &resp); err != nil {
		return nil, err
	}
	return resp.Data, nil
}

// ListApps returns all apps within the specified project.
func (c *Client) ListApps(ctx context.Context, projectSlug string) ([]App, error) {
	var resp APIResponse[[]App]
	if err := c.doGet(ctx, fmt.Sprintf("/kagi/projects/%s/apps", projectSlug), &resp); err != nil {
		return nil, err
	}
	return resp.Data, nil
}

// ListEnvironments returns all environments within the specified project.
func (c *Client) ListEnvironments(ctx context.Context, projectSlug string) ([]Environment, error) {
	var resp APIResponse[[]Environment]
	if err := c.doGet(ctx, fmt.Sprintf("/kagi/projects/%s/environments", projectSlug), &resp); err != nil {
		return nil, err
	}
	return resp.Data, nil
}

// FetchSecrets returns decrypted secrets as key-value pairs for an app's environment.
func (c *Client) FetchSecrets(ctx context.Context, appID, environmentID string) (map[string]string, error) {
	var resp APIResponse[SecretFetchResponse]
	if err := c.doGet(ctx, fmt.Sprintf("/kagi/apps/%s/environments/%s/secrets/fetch", appID, environmentID), &resp); err != nil {
		return nil, err
	}
	return resp.Data.Secrets, nil
}

// ListCertificates returns all certificates.
func (c *Client) ListCertificates(ctx context.Context) ([]CertificateListItem, error) {
	var resp APIResponse[[]CertificateListItem]
	if err := c.doGet(ctx, "/kagi/certificates", &resp); err != nil {
		return nil, err
	}
	return resp.Data, nil
}

// GetCertificateDetail returns detailed metadata for a certificate.
func (c *Client) GetCertificateDetail(ctx context.Context, certID string) (*CertificateDetail, error) {
	var resp APIResponse[CertificateDetail]
	if err := c.doGet(ctx, fmt.Sprintf("/kagi/certificates/%s", certID), &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

// RevealCertificate returns the decrypted certificate and private key.
func (c *Client) RevealCertificate(ctx context.Context, certID string) (*CertificateReveal, error) {
	var resp APIResponse[CertificateReveal]
	if err := c.doGet(ctx, fmt.Sprintf("/kagi/certificates/%s/reveal", certID), &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

// GetCertificateHistory returns audit history for a certificate.
func (c *Client) GetCertificateHistory(ctx context.Context, certID string) ([]CertificateHistory, error) {
	var resp APIResponse[[]CertificateHistory]
	if err := c.doGet(ctx, fmt.Sprintf("/kagi/certificates/%s/history", certID), &resp); err != nil {
		return nil, err
	}
	return resp.Data, nil
}

// doGet performs an authenticated GET request, reads the response body, and
// unmarshals the JSON into result. It returns an error for non-2xx status codes.
func (c *Client) doGet(ctx context.Context, path string, result any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("kagi: failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("kagi: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("kagi: failed to read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("kagi: API returned status %d: %s", resp.StatusCode, string(body))
	}

	if err := json.Unmarshal(body, result); err != nil {
		return fmt.Errorf("kagi: failed to parse response: %w", err)
	}

	return nil
}
