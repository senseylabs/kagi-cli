package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	kagi "github.com/senseylabs/kagi-sdk"

	"github.com/senseylabs/kagi-cli/internal/auth"
	"github.com/senseylabs/kagi-cli/internal/config"
	"github.com/senseylabs/kagi-cli/internal/httpx"
)

// Re-export SDK types so existing CLI code doesn't break.
type Organization = kagi.Organization
type App = kagi.App
type Folder = kagi.Folder
type FolderChildren = kagi.FolderChildren
type Environment = kagi.Environment
type SecretFetchResponse = kagi.SecretFetchResponse
type CertificateListItem = kagi.CertificateListItem
type CertificateDetail = kagi.CertificateDetail
type CertificateReveal = kagi.CertificateReveal
type CertificateHistory = kagi.CertificateHistory
type CertificateFolderItem = kagi.CertificateFolderItem
type CertificateResolve = kagi.CertificateResolve

// APIErrorResponse represents an error response from the API.
type APIErrorResponse struct {
	Message string `json:"message"`
	Status  int    `json:"status"`
}

// KagiClient handles HTTP communication with the Village Kagi API.
// Read-only operations are delegated to the shared SDK client.
type KagiClient struct {
	baseURL    string
	issuerURL  string
	httpClient *http.Client
	token      string
	sdkClient  *kagi.Client

	// orgID is the active organization UUID, sent as X-Organization-ID on write
	// requests. Empty until an organization has been selected.
	orgID string
}

// OrgID returns the active organization UUID configured for JWT requests.
func (c *KagiClient) OrgID() string { return c.orgID }

// NewKagiClientWithToken creates a client with an explicit JWT (used during the
// login flow to call read-only org endpoints before an org is selected). Pass an
// empty orgID for the org-discovery call right after device login; the org
// header is attached only once an org is known.
func NewKagiClientWithToken(baseURL, token string) *KagiClient {
	return &KagiClient{
		baseURL: baseURL,
		token:   token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		sdkClient: kagi.NewOrgClient(baseURL, token, ""),
	}
}

// NewKagiClient creates a new client, resolving the auth token from env var or credential store.
func NewKagiClient(baseURL, issuerURL string) (*KagiClient, error) {
	c := &KagiClient{
		baseURL:   baseURL,
		issuerURL: issuerURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}

	// Auth resolves solely from the stored JWT session. The active org is sent
	// via the X-Organization-ID header, sourced from the persisted config (set
	// via `kagi org use`).
	cfg := config.Load()
	c.orgID = cfg.OrganizationID

	// Load JWT from credential store
	store := auth.NewTokenStore()
	creds, err := store.Load()
	if err != nil {
		return nil, fmt.Errorf("not authenticated. Run 'kagi login' to authenticate")
	}

	// Use stored issuer URL if not explicitly provided
	if issuerURL == "" && creds.IssuerURL != "" {
		issuerURL = creds.IssuerURL
	}

	c.issuerURL = issuerURL

	// Refresh if expired.
	//
	// This whole load-refresh-store sequence is guarded by a cross-process
	// advisory lock. When several `kagi` processes start at once (e.g. one per
	// app in a dev start script) with an expired token, they would otherwise all
	// refresh and store concurrently: on macOS that races the keychain write
	// (concurrent `security add-generic-password` -> errSecDuplicateItem, exit
	// status 45), and it presents the shared refresh token N times, which breaks
	// under refresh-token rotation. With the lock, exactly one process refreshes;
	// the rest re-load the freshly stored credentials and skip the network call.
	if time.Now().After(creds.ExpiresAt) {
		lock, err := auth.AcquireRefreshLock()
		if err != nil {
			return nil, err
		}
		defer lock.Release()

		// Double-checked expiry: another process may have refreshed while we
		// waited on the lock, so re-load to act on the latest stored credentials.
		// The pre-lock Load already succeeded, so a failure here means the store
		// went bad underneath us — fail rather than fall back to the stale creds
		// and present an already-rotated refresh token, which reuse detection can
		// treat as a breach and revoke the whole token family.
		fresh, loadErr := store.Load()
		if loadErr != nil {
			return nil, fmt.Errorf("could not read stored credentials: %w", loadErr)
		}
		creds = fresh

		if time.Now().After(creds.ExpiresAt) {
			refreshIssuer := creds.IssuerURL
			if refreshIssuer == "" {
				refreshIssuer = issuerURL
			}
			deviceFlow := auth.NewDeviceFlow(refreshIssuer, "cli", auth.DefaultScope)
			// Bound discovery on this routine refresh path to a single-shot-ish
			// budget (matches the pre-change behaviour); the interactive `kagi
			// login` path is where the longer cold-start retry budget applies.
			discoverCtx, discoverCancel := context.WithTimeout(context.Background(), 15*time.Second)
			endpoints, err := deviceFlow.DiscoverEndpoints(discoverCtx)
			discoverCancel()
			if err != nil {
				return nil, classifyRefreshError(err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			newToken, err := deviceFlow.RefreshToken(ctx, endpoints.TokenEndpoint, creds.RefreshToken)
			cancel()
			if err != nil {
				return nil, classifyRefreshError(err)
			}

			creds.AccessToken = newToken.AccessToken
			if newToken.RefreshToken != "" {
				creds.RefreshToken = newToken.RefreshToken
			}
			creds.ExpiresAt = time.Now().Add(time.Duration(newToken.ExpiresIn) * time.Second)

			if err := store.Save(creds); err != nil {
				return nil, fmt.Errorf("failed to save refreshed credentials: %w", err)
			}
		}

		// Release before client construction; the deferred Release above is an
		// idempotent no-op backstop that also covers the early-return paths.
		lock.Release()
	}

	c.token = creds.AccessToken
	c.sdkClient = kagi.NewOrgClient(baseURL, creds.AccessToken, c.orgID)
	return c, nil
}

// classifyRefreshError turns a discovery/refresh failure on the routine
// token-refresh path into an actionable message. A definite authentication
// failure — invalid_grant / an HTTP 4xx from the token endpoint — means the
// session is truly gone and the user must re-login. Anything else (a transient
// 5xx, a transport blip, or an OIDC discovery failure) is worth retrying and
// must not be mislabeled as an expired session.
func classifyRefreshError(err error) error {
	var tee *auth.TokenEndpointError
	if errors.As(err, &tee) && !tee.Transient() {
		return fmt.Errorf("session expired. Run 'kagi login' to re-authenticate")
	}
	return fmt.Errorf("temporary problem reaching the login server, try again: %w", err)
}

// ---------------------------------------------------------------------------
// Read-only operations — delegated to the SDK client
// ---------------------------------------------------------------------------

// ListOrganizations returns the organizations the authenticated user belongs to.
func (c *KagiClient) ListOrganizations() ([]Organization, error) {
	return c.sdkClient.ListOrganizations(context.Background())
}

// ListFolderChildren browses a SECRETS folder path and returns its child
// folders and the apps directly under it.
func (c *KagiClient) ListFolderChildren(path string) (*FolderChildren, error) {
	return c.sdkClient.ListFolderChildren(context.Background(), kagi.LibrarySecrets, path)
}

// ListApps returns the apps directly under a SECRETS folder path.
func (c *KagiClient) ListApps(folderPath string) ([]App, error) {
	return c.sdkClient.ListApps(context.Background(), folderPath)
}

// ResolveApp resolves a human-entered SECRETS folder/app path to the app's
// stable internal ID — the durable machine binding captured once at setup.
func (c *KagiClient) ResolveApp(folderPath string) (string, error) {
	return c.sdkClient.ResolveApp(context.Background(), folderPath)
}

// ListEnvironments returns all environments for an app, addressed by its
// stable app ID.
func (c *KagiClient) ListEnvironments(appID string) ([]Environment, error) {
	return c.sdkClient.ListEnvironments(context.Background(), appID)
}

// FetchSecrets returns decrypted secrets as key-value pairs for an app's
// environment, addressed by the stable app ID and the environment slug.
func (c *KagiClient) FetchSecrets(appID, envSlug string) (map[string]string, error) {
	return c.sdkClient.FetchSecrets(context.Background(), appID, envSlug)
}

// ListCertificates returns all certificates.
func (c *KagiClient) ListCertificates() ([]CertificateListItem, error) {
	return c.sdkClient.ListCertificates(context.Background())
}

// ListCertificateFolderChildren browses a CERTIFICATES folder path and returns
// its child folders (certificate leaves come from ListCertificatesInFolder).
func (c *KagiClient) ListCertificateFolderChildren(path string) (*FolderChildren, error) {
	return c.sdkClient.ListCertificateFolderChildren(context.Background(), path)
}

// ListCertificatesInFolder returns the certificates held directly inside the
// certificate folder addressed by path.
func (c *KagiClient) ListCertificatesInFolder(path string) ([]CertificateFolderItem, error) {
	return c.sdkClient.ListCertificatesInFolder(context.Background(), path)
}

// ResolveCertificate resolves a certificate node path to its stable id and name.
func (c *KagiClient) ResolveCertificate(path string) (*CertificateResolve, error) {
	return c.sdkClient.ResolveCertificate(context.Background(), path)
}

// GetCertificateDetail returns detailed metadata for a certificate.
func (c *KagiClient) GetCertificateDetail(certID string) (*CertificateDetail, error) {
	return c.sdkClient.GetCertificateDetail(context.Background(), certID)
}

// RevealCertificate returns the decrypted certificate and private key.
func (c *KagiClient) RevealCertificate(certID string) (*CertificateReveal, error) {
	return c.sdkClient.RevealCertificate(context.Background(), certID)
}

// GetCertificateHistory returns audit history for a certificate.
func (c *KagiClient) GetCertificateHistory(certID string) ([]CertificateHistory, error) {
	return c.sdkClient.GetCertificateHistory(context.Background(), certID)
}

// ---------------------------------------------------------------------------
// CLI-specific types (not in the SDK)
// ---------------------------------------------------------------------------

// SecretListItem represents a secret in a list view with masked value.
type SecretListItem struct {
	ID          string `json:"id"`
	KeyName     string `json:"keyName"`
	MaskedValue string `json:"maskedValue"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

// SecretRevealResponse represents a revealed (decrypted) secret.
type SecretRevealResponse struct {
	ID      string `json:"id"`
	KeyName string `json:"keyName"`
	Value   string `json:"value"`
}

// ---------------------------------------------------------------------------
// Write operations — stay in the CLI client (not in the read-only SDK)
// ---------------------------------------------------------------------------

// doRequest sends a bodyless HTTP request and returns the response bytes.
func (c *KagiClient) doRequest(method, path string) ([]byte, error) {
	return c.do(method, path, nil)
}

// doRequestWithBody sends an HTTP request with a JSON body and returns the
// response bytes.
func (c *KagiClient) doRequestWithBody(method, path string, payload interface{}) ([]byte, error) {
	return c.do(method, path, payload)
}

// do is the shared request core behind doRequest and doRequestWithBody. It
// enforces requireOrgForJWT, marshals an optional JSON payload, attaches the
// auth headers, and maps transport and non-2xx failures to friendly errors.
func (c *KagiClient) do(method, path string, payload interface{}) ([]byte, error) {
	if err := c.requireOrgForJWT(); err != nil {
		return nil, err
	}

	url := c.baseURL + path

	var bodyReader io.Reader
	if payload != nil {
		jsonBytes, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(jsonBytes)
	}

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	c.setAuthHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if httpx.IsRetryable(err) {
			return nil, fmt.Errorf("could not connect to %s. Check your network or if the API is running: %w", c.baseURL, err)
		}
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response from %s: %w", url, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, mapHTTPError(resp.StatusCode, body)
	}

	return body, nil
}

// mapHTTPError turns a non-2xx status + body into a friendly error. The backend
// envelope message wins when present; otherwise a per-status fallback is used.
func mapHTTPError(status int, body []byte) error {
	var apiErr APIErrorResponse
	if json.Unmarshal(body, &apiErr) == nil && apiErr.Message != "" {
		return fmt.Errorf("%s", apiErr.Message)
	}

	switch status {
	case 401:
		return fmt.Errorf("unauthorized. Run 'kagi login' to authenticate")
	case 403:
		return fmt.Errorf("access denied. You may not have permission for this operation")
	case 404:
		return fmt.Errorf("resource not found")
	case 500:
		return fmt.Errorf("server error. Try again later")
	default:
		bodyStr := string(body)
		if len(bodyStr) > 200 {
			bodyStr = bodyStr[:200] + "..."
		}
		return fmt.Errorf("request failed (%d): %s", status, bodyStr)
	}
}

// setAuthHeaders sets the Authorization + Content-Type headers and, once an
// organization has been selected, the X-Organization-ID header.
func (c *KagiClient) setAuthHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	if c.orgID != "" {
		req.Header.Set(kagi.HeaderOrganizationID, c.orgID)
	}
}

// requireOrgForJWT fails fast on write requests when no active organization has
// been selected, rather than letting the backend reject the request opaquely.
func (c *KagiClient) requireOrgForJWT() error {
	if c.orgID == "" {
		return fmt.Errorf("no organization selected. Run 'kagi org use <slug>' (see 'kagi org list')")
	}
	return nil
}

// secretsBasePath builds the folder-model secrets base URL for an app's
// environment: /kagi/apps/{appId}/environments/{envSlug}/secrets.
func secretsBasePath(appID, envSlug string) string {
	return fmt.Sprintf("/kagi/apps/%s/environments/%s/secrets", appID, envSlug)
}

// SetSecrets performs a bulk upsert of secrets for an app's environment,
// addressed by the stable app ID and the environment slug.
func (c *KagiClient) SetSecrets(appID, envSlug string, secrets map[string]string) error {
	type secretEntry struct {
		KeyName string `json:"keyName"`
		Value   string `json:"value"`
	}

	entries := make([]secretEntry, 0, len(secrets))
	for k, v := range secrets {
		entries = append(entries, secretEntry{KeyName: k, Value: v})
	}

	payload := map[string]interface{}{
		"secrets": entries,
	}

	_, err := c.doRequestWithBody("POST", secretsBasePath(appID, envSlug)+"/bulk", payload)
	return err
}

// GetSecret reveals (decrypts) a single secret by ID within an app's
// environment.
func (c *KagiClient) GetSecret(appID, envSlug, secretID string) (*SecretRevealResponse, error) {
	body, err := c.doRequest("GET", fmt.Sprintf("%s/%s/reveal", secretsBasePath(appID, envSlug), secretID))
	if err != nil {
		return nil, err
	}

	var resp kagi.APIResponse[SecretRevealResponse]
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse reveal secret response: %w", err)
	}

	return &resp.Data, nil
}

// DeleteSecret deletes a secret by ID within an app's environment.
func (c *KagiClient) DeleteSecret(appID, envSlug, secretID string) error {
	_, err := c.doRequest("DELETE", fmt.Sprintf("%s/%s", secretsBasePath(appID, envSlug), secretID))
	return err
}

// ListSecrets returns all secrets for an app's environment with masked values.
// An explicit large page size is sent because the backend paginates with a
// default of 20; without it, list/get/delete would only ever see the first
// page and miss keys beyond it.
func (c *KagiClient) ListSecrets(appID, envSlug string) ([]SecretListItem, error) {
	body, err := c.doRequest("GET", secretsBasePath(appID, envSlug)+"?size=1000")
	if err != nil {
		return nil, err
	}

	var resp kagi.APIResponse[[]SecretListItem]
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse secrets list response: %w", err)
	}

	return resp.Data, nil
}

// ---------------------------------------------------------------------------
// Certificate write operations
// ---------------------------------------------------------------------------

// CreateCertificate creates a new certificate.
func (c *KagiClient) CreateCertificate(name, certContent, keyContent string) (*CertificateDetail, error) {
	payload := map[string]string{
		"name":               name,
		"certificateContent": certContent,
		"privateKeyContent":  keyContent,
	}

	body, err := c.doRequestWithBody("POST", "/kagi/certificates", payload)
	if err != nil {
		return nil, err
	}

	var resp kagi.APIResponse[CertificateDetail]
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse create certificate response: %w", err)
	}

	return &resp.Data, nil
}

// UpdateCertificate updates an existing certificate's content.
func (c *KagiClient) UpdateCertificate(certID, certContent, keyContent string) (*CertificateDetail, error) {
	payload := map[string]string{
		"certificateContent": certContent,
		"privateKeyContent":  keyContent,
	}

	body, err := c.doRequestWithBody("PUT", fmt.Sprintf("/kagi/certificates/%s", certID), payload)
	if err != nil {
		return nil, err
	}

	var resp kagi.APIResponse[CertificateDetail]
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse update certificate response: %w", err)
	}

	return &resp.Data, nil
}

// DeleteCertificate deletes a certificate by ID.
func (c *KagiClient) DeleteCertificate(certID string) error {
	_, err := c.doRequest("DELETE", fmt.Sprintf("/kagi/certificates/%s", certID))
	return err
}
