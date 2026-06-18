package kagi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// HeaderOrganizationID is the request header carrying the active organization
// UUID. It is sent only for JWT (human) auth — for PAT auth the org is bound to
// the token server-side and sending a mismatched header would be rejected (403).
const HeaderOrganizationID = "X-Organization-ID"

// Client is a read-only HTTP client for the Kagi secrets management API.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client

	// orgID is the active organization UUID, sent as X-Organization-ID on JWT
	// requests. Empty for PAT auth (the org is bound to the token).
	orgID string
	// isPAT reports whether token is a Personal Access Token. When true the
	// org header is never sent — the backend rejects a mismatched header (403).
	isPAT bool
	// orgAware is set by NewOrgClient for JWT auth. When true an empty orgID on
	// an org-scoped request fails fast (ErrNoOrganizationSelected) rather than
	// being sent without a header. The bare NewClient leaves this false so it
	// stays unopinionated for callers that manage org context themselves.
	orgAware bool
}

// NewClient creates a new Kagi SDK client.
//
// baseURL is the root URL of the Kagi API (e.g. "https://api.example.com").
// token is a Bearer token used for authentication.
//
// This constructor does not attach an organization header. Use NewOrgClient to
// send X-Organization-ID on JWT (human) requests.
func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: baseURL,
		token:   token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// NewOrgClient creates an organization-aware Kagi SDK client.
//
// orgID is the active organization UUID, sent as the X-Organization-ID header
// on every request when isPAT is false (JWT / human auth). When isPAT is true
// the token already carries its org server-side, so no header is sent — sending
// a mismatched one would be rejected with 403 (the confused-deputy guard).
func NewOrgClient(baseURL, token, orgID string, isPAT bool) *Client {
	c := NewClient(baseURL, token)
	c.orgID = orgID
	c.isPAT = isPAT
	// JWT clients are org-aware: an org-scoped request with no org selected
	// fails fast. PAT clients never need a selected org (token-bound).
	c.orgAware = !isPAT
	return c
}

// ListOrganizations returns the organizations the authenticated user belongs to.
// Intended for JWT (human) auth; PAT auth is scoped to a single token-bound org.
func (c *Client) ListOrganizations(ctx context.Context) ([]Organization, error) {
	var resp APIResponse[[]Organization]
	if err := c.doGet(ctx, "/kagi/organizations", &resp); err != nil {
		return nil, err
	}
	return resp.Data, nil
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

// orgListPath is the one org-scoped endpoint reachable before an org is
// selected — it is how a JWT user discovers which orgs they may select.
const orgListPath = "/kagi/organizations"

// ErrNoOrganizationSelected is returned for org-scoped JWT requests when no
// active organization has been configured.
var ErrNoOrganizationSelected = fmt.Errorf("no organization selected. Run 'kagi org use <slug>' (see 'kagi org list')")

// doGet performs an authenticated GET request, reads the response body, and
// unmarshals the JSON into result. It returns an error for non-2xx status codes.
func (c *Client) doGet(ctx context.Context, path string, result any) error {
	// JWT auth needs an active org for every org-scoped request. Fail fast with
	// an actionable error rather than letting the backend reject it opaquely.
	// The org-list endpoint is exempt — it is how the user discovers orgs.
	if c.orgAware && c.orgID == "" && path != orgListPath {
		return ErrNoOrganizationSelected
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("kagi: failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	// JWT (human) auth resolves the active org from this header. PAT auth must
	// NOT send it — the org is bound to the token and a mismatch returns 403.
	if !c.isPAT && c.orgID != "" {
		req.Header.Set(HeaderOrganizationID, c.orgID)
	}

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
