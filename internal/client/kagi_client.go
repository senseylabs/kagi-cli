package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
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
type PasswordListItem = kagi.PasswordListItem
type PasswordResolve = kagi.PasswordResolve
type PasswordReveal = kagi.PasswordReveal
type PasswordHistory = kagi.PasswordHistory
type OnboardingState = kagi.OnboardingState
type OnboardingStatus = kagi.OnboardingStatus

// APIErrorResponse represents an error response from the API. It covers the
// human-readable half of the CustomResponse envelope; the machine-readable
// error.code is parsed separately (see errorEnvelope) because callers branch on
// the code, never on the message text.
type APIErrorResponse struct {
	Message string `json:"message"`
	Status  int    `json:"status"`
}

// errorEnvelope is the nested error object of the backend CustomResponse
// envelope ({success, message, data, pagination, error:{code,message}}). The
// code is the only stable identifier of WHY a request was refused — two
// unrelated refusals share HTTP 403 — so it is parsed here and carried on the
// returned error even when the message shown to the user is the friendly one.
type errorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// httpError is a non-2xx response rendered for a human while still carrying the
// machine-readable backend code.
//
// Error() is the friendly text the CLI has always printed, so no existing
// message changes. Unwrap exposes an *kagi.APIError, so errors.As and the
// kagi.IsNotOnboarded / kagi.IsDomainClaimedByOtherOrg classifiers see the wire
// code through it — without that, every caller would be back to matching
// substrings of the message.
type httpError struct {
	friendly string
	api      *kagi.APIError
}

func (e *httpError) Error() string { return e.friendly }
func (e *httpError) Unwrap() error { return e.api }

// KagiClient handles HTTP communication with the Village Kagi API.
// Read-only operations are delegated to the shared SDK client.
type KagiClient struct {
	baseURL    string
	issuerURL  string
	httpClient *http.Client
	token      string
	sdkClient  *kagi.Client

	// orgID is the active organization UUID, sent as X-Organization-ID on JWT
	// (human) write requests. Empty under PAT auth.
	orgID string
	// isPAT reports whether token came from KAGI_TOKEN (a Personal Access
	// Token). When true the org header is never sent — the org is bound to the
	// token server-side and a mismatched header returns 403.
	isPAT bool
}

// IsPAT reports whether this client authenticates with a Personal Access Token
// supplied via KAGI_TOKEN, rather than with the stored `kagi login` session.
func (c *KagiClient) IsPAT() bool { return c.isPAT }

// OrgID returns the active organization UUID configured for JWT requests.
func (c *KagiClient) OrgID() string { return c.orgID }

// NewKagiClientWithToken creates a client with an explicit JWT (used during the
// login flow to call read-only org endpoints before an org is selected). The
// token is treated as a JWT so the org header may be attached if orgID is set;
// the org-discovery call right after device login runs with no org known.
func NewKagiClientWithToken(baseURL, token string) *KagiClient {
	return &KagiClient{
		baseURL: baseURL,
		token:   token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		sdkClient: kagi.NewOrgClient(baseURL, token, "", false),
	}
}

// NewKagiClient creates a new client, resolving the auth token from KAGI_TOKEN
// or, failing that, from the stored `kagi login` session.
//
// KAGI_TOKEN takes precedence: it is the non-interactive credential for CI and
// other headless callers, where there is no session to fall back on. It also
// must win on a developer machine that happens to have both — a job that sets
// KAGI_TOKEN has stated which identity it wants, and silently acting as the
// logged-in human instead would be a confused deputy. An empty or
// whitespace-only value counts as unset (see auth.StaticToken), so an
// unpopulated CI secret falls through to the session rather than
// authenticating with an empty bearer token and failing as an opaque 401.
func NewKagiClient(baseURL, issuerURL string) (*KagiClient, error) {
	if pat := auth.StaticToken(); pat != "" {
		return newPATClient(baseURL, pat), nil
	}
	return NewSessionClient(baseURL, issuerURL)
}

// newPATClient builds a client authenticated with a Personal Access Token. A
// PAT is org-bound server-side, so orgID stays empty and X-Organization-ID is
// never attached — sending a mismatched org would be rejected with 403 (the
// confused-deputy guard).
func newPATClient(baseURL, pat string) *KagiClient {
	return &KagiClient{
		baseURL: baseURL,
		token:   pat,
		isPAT:   true,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		sdkClient: kagi.NewOrgClient(baseURL, pat, "", true),
	}
}

// NewSessionClient creates a client from the stored `kagi login` session only,
// ignoring KAGI_TOKEN, refreshing the access token when it has expired.
//
// `kagi login` uses this rather than NewKagiClient to judge whether the stored
// session is reusable: with a PAT in the environment NewKagiClient answers
// about the PAT, and login would report "Already logged in" about a session it
// never actually checked.
func NewSessionClient(baseURL, issuerURL string) (*KagiClient, error) {
	c := &KagiClient{
		baseURL:   baseURL,
		issuerURL: issuerURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}

	// Auth resolves from the stored JWT session. The active org is sent via the
	// X-Organization-ID header, sourced from the persisted config (set via
	// `kagi org use`).
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
			// budget (matches the pre-change behavior); the interactive `kagi
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
	c.sdkClient = kagi.NewOrgClient(baseURL, creds.AccessToken, c.orgID, false)
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

// GetOnboardingState reports this caller's own onboarding situation. It is the
// one read an account that has not finished onboarding may perform, so it is
// how the CLI explains a login it has to refuse.
func (c *KagiClient) GetOnboardingState() (*OnboardingStatus, error) {
	return c.sdkClient.GetOnboardingState(context.Background())
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

// ListPasswordFolderChildren browses a PASSWORDS folder path and returns its
// child folders (password leaves come from ListPasswordsInFolder).
func (c *KagiClient) ListPasswordFolderChildren(path string) (*FolderChildren, error) {
	return c.sdkClient.ListPasswordFolderChildren(context.Background(), path)
}

// ListPasswordsInFolder returns the passwords held directly inside the password
// folder addressed by path, with masked values.
func (c *KagiClient) ListPasswordsInFolder(path string) ([]PasswordListItem, error) {
	return c.sdkClient.ListPasswordsInFolder(context.Background(), path)
}

// ResolvePassword resolves a password node path to its stable id and username.
func (c *KagiClient) ResolvePassword(path string) (*PasswordResolve, error) {
	return c.sdkClient.ResolvePassword(context.Background(), path)
}

// GetPasswordDetail returns a single password's masked metadata by id.
func (c *KagiClient) GetPasswordDetail(passwordID string) (*PasswordListItem, error) {
	return c.sdkClient.GetPasswordDetail(context.Background(), passwordID)
}

// RevealPassword returns the decrypted password value by id.
func (c *KagiClient) RevealPassword(passwordID string) (*PasswordReveal, error) {
	return c.sdkClient.RevealPassword(context.Background(), passwordID)
}

// GetPasswordHistory returns the change history for a password by id.
func (c *KagiClient) GetPasswordHistory(passwordID string) ([]PasswordHistory, error) {
	return c.sdkClient.GetPasswordHistory(context.Background(), passwordID)
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

// mapHTTPError turns a non-2xx status + body into a friendly error that still
// carries the backend wire code.
//
// Precedence: a code the CLI has specific wording for wins, because the generic
// envelope message for those refusals ("Your account has not finished
// onboarding.") reads as a wall rather than as a next step. Otherwise the
// envelope message wins, and failing that a per-status fallback is used. In
// every branch the parsed code is attached, so callers can classify the failure
// with kagi.IsNotOnboarded and friends regardless of which message was chosen.
func mapHTTPError(status int, body []byte) error {
	var env errorEnvelope
	// rule-10-no-op -- reason: a body that is not the envelope (a proxy error
	// page, an empty 502) is an expected shape here, not a failure. It simply
	// yields no code, and the message fallbacks below cover it; reporting a
	// parse error instead would replace the server's reason with our own.
	_ = json.Unmarshal(body, &env)

	var apiErr APIErrorResponse
	// rule-10-no-op -- reason: same body, same reasoning as above.
	_ = json.Unmarshal(body, &apiErr)

	wrap := func(friendly string) error {
		return &httpError{
			friendly: friendly,
			api: &kagi.APIError{
				Status:  status,
				Code:    env.Error.Code,
				Message: apiErr.Message,
			},
		}
	}

	switch env.Error.Code {
	case kagi.ErrCodeAccountNotOnboarded:
		return wrap("your Kagi account has not finished setting up yet. Finish setting up in the Kagi portal, then run 'kagi login' again")
	case kagi.ErrCodeDomainClaimedByOtherOrg:
		return wrap("your email domain belongs to an existing organization, so you cannot create your own. Request to join it in the Kagi portal instead")
	}

	if apiErr.Message != "" {
		return wrap(apiErr.Message)
	}

	switch status {
	case 401:
		return wrap("unauthorized. Run 'kagi login' to authenticate")
	case 403:
		return wrap("access denied. You may not have permission for this operation")
	case 404:
		return wrap("resource not found")
	case 500:
		return wrap("server error. Try again later")
	default:
		bodyStr := string(body)
		if len(bodyStr) > 200 {
			bodyStr = bodyStr[:200] + "..."
		}
		return wrap(fmt.Sprintf("request failed (%d): %s", status, bodyStr))
	}
}

// setAuthHeaders sets the Authorization + Content-Type headers and, for JWT
// (human) auth only, the X-Organization-ID header. PAT auth omits the org
// header — the org is bound to the token and a mismatch returns 403.
func (c *KagiClient) setAuthHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	if !c.isPAT && c.orgID != "" {
		req.Header.Set(kagi.HeaderOrganizationID, c.orgID)
	}
}

// requireOrgForJWT fails fast on JWT (human) write requests when no active
// organization has been selected, rather than letting the backend reject the
// request opaquely. PAT auth is exempt — the org is bound to the token, and a
// CI runner has no `kagi org use` selection to make.
func (c *KagiClient) requireOrgForJWT() error {
	if !c.isPAT && c.orgID == "" {
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

// maxPageSize is the largest page the backend will honor: PageableSanitizer
// clamps any requested size to DEFAULT_MAX_SIZE (200), so a larger ?size is
// silently reduced and truncates the result. List calls page in this size.
const maxPageSize = 200

// maxListPages bounds a pagination loop so an unexpectedly large (or
// ever-growing) collection cannot spin forever. Reaching it logs a warning and
// returns what was gathered so far.
const maxListPages = 100

// maxTokenFolders bounds the access-token folder walk for the same reason: a
// pathological folder tree cannot make the traversal run unbounded.
const maxTokenFolders = 500

// fetchAllPages walks Spring pages 0,1,2,... for a list endpoint, accumulating
// rows until a page returns fewer than maxPageSize items (the final page) or the
// page cap is hit. pathForPage builds the request path for a given zero-based
// page number and must request size=maxPageSize. resource names the collection
// for the cap-exceeded warning and for parse errors.
func fetchAllPages[T any](c *KagiClient, resource string, pathForPage func(page int) string) ([]T, error) {
	var all []T
	for page := 0; page < maxListPages; page++ {
		body, err := c.doRequest("GET", pathForPage(page))
		if err != nil {
			return nil, err
		}

		var resp kagi.APIResponse[[]T]
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("failed to parse %s list response: %w", resource, err)
		}

		all = append(all, resp.Data...)
		if len(resp.Data) < maxPageSize {
			return all, nil
		}
	}
	fmt.Fprintf(os.Stderr, "warning: %s list hit the %d-page cap; results may be truncated\n", resource, maxListPages)
	return all, nil
}

// ListSecrets returns all secrets for an app's environment with masked values.
// The backend clamps page size to 200 (PageableSanitizer.DEFAULT_MAX_SIZE), so
// a single oversized request would silently truncate; the secrets are instead
// paged in maxPageSize-row batches so list/get/delete see every key.
func (c *KagiClient) ListSecrets(appID, envSlug string) ([]SecretListItem, error) {
	base := secretsBasePath(appID, envSlug)
	return fetchAllPages[SecretListItem](c, "secrets", func(page int) string {
		return fmt.Sprintf("%s?page=%d&size=%d", base, page, maxPageSize)
	})
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

// certificateFolderItemsPath builds the folder-model certificates items URL for
// a folder path, escaping each path segment. A "/" or empty folderPath addresses
// the certificates root, where the wildcard suffix is empty and the URL ends at
// .../items with no trailing segment.
func certificateFolderItemsPath(folderPath string) string {
	base := "/kagi/folders/certificates/items"
	trimmed := strings.Trim(folderPath, "/")
	if trimmed == "" {
		return base
	}
	segments := strings.Split(trimmed, "/")
	for i, seg := range segments {
		segments[i] = url.PathEscape(seg)
	}
	return base + "/" + strings.Join(segments, "/")
}

// CreateCertificateInFolder creates a certificate inside the certificate folder
// addressed by folderPath. A "/" or empty folderPath targets the root. The body
// mirrors CreateCertificate; the response parses the same CertificateDetail.
func (c *KagiClient) CreateCertificateInFolder(folderPath, name, certContent, keyContent string) (*CertificateDetail, error) {
	payload := map[string]string{
		"name":               name,
		"certificateContent": certContent,
		"privateKeyContent":  keyContent,
	}

	body, err := c.doRequestWithBody("POST", certificateFolderItemsPath(folderPath), payload)
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

// ---------------------------------------------------------------------------
// Access token operations (read/revoke only — creation is deliberately absent)
// ---------------------------------------------------------------------------

// AccessToken is a Kagi personal access token as returned by the folder-addressed
// list endpoint. The token hash/plaintext is never exposed. FolderID is empty for
// a token not yet folder-addressed, and ExpiresAt is empty for a token that never
// expires. FolderPath is the human-readable path of the folder the token was
// listed from ("/" for the root/null-folder bucket); it is filled in by the
// folder-tree walk, not returned by the backend.
type AccessToken struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	CreatedByID string `json:"createdById"`
	TokenType   string `json:"tokenType"`
	FolderID    string `json:"folderId"`
	FolderPath  string `json:"folderPath"`
	ExpiresAt   string `json:"expiresAt"`
	CreatedAt   string `json:"createdAt"`
}

// accessTokenItemsPath builds the folder-addressed access-token items URL for a
// zero-based page. The route uses a terminal capturing wildcard, so the folder
// path is appended last; an empty/"/" folderPath addresses the root (which is
// also the null-folder bucket for tokens not yet folder-addressed). The backend
// clamps size to 200, so callers page in maxPageSize-row batches.
func accessTokenItemsPath(folderPath string, page int) string {
	base := "/kagi/folders/access-tokens/items"
	suffix := ""
	if trimmed := strings.Trim(folderPath, "/"); trimmed != "" {
		suffix = "/" + trimmed
	}
	return fmt.Sprintf("%s%s?page=%d&size=%d&sort=name", base, suffix, page, maxPageSize)
}

// ListAccessTokens returns ALL of the caller's access tokens across the entire
// access-token folder tree, each tagged with its FolderPath. The root
// /items listing only ever returns root/null-folder tokens, so this walks the
// folder tree (mirroring how passwords and certificates browse folders): for
// each folder it pages the tokens held directly inside it, then descends into
// that folder's children. Subfolder and null-folder tokens would otherwise be
// invisible — and therefore unrevokable — from the CLI.
func (c *KagiClient) ListAccessTokens() ([]AccessToken, error) {
	ctx := context.Background()
	var all []AccessToken

	// Breadth-first walk of the folder tree. "" is the root, which doubles as
	// the null-folder bucket for tokens not yet folder-addressed.
	queue := []string{""}
	for visited := 0; len(queue) > 0; visited++ {
		if visited >= maxTokenFolders {
			fmt.Fprintf(os.Stderr, "warning: access-token folder walk hit the %d-folder cap; some tokens may be missing\n", maxTokenFolders)
			break
		}

		folderPath := queue[0]
		queue = queue[1:]

		displayPath := folderPath
		if displayPath == "" {
			displayPath = "/"
		}

		items, err := fetchAllPages[AccessToken](c, "access tokens", func(page int) string {
			return accessTokenItemsPath(folderPath, page)
		})
		if err != nil {
			return nil, err
		}
		for i := range items {
			items[i].FolderPath = displayPath
		}
		all = append(all, items...)

		children, err := c.sdkClient.ListFolderChildren(ctx, kagi.LibraryAccessTokens, folderPath)
		if err != nil {
			return nil, err
		}
		for _, f := range children.Folders {
			queue = append(queue, joinTokenFolderPath(folderPath, f.Slug))
		}
	}

	return all, nil
}

// joinTokenFolderPath appends a child folder slug to a parent access-token
// folder path, yielding an absolute path with a single leading slash. The root
// ("" or "/") yields "/slug".
func joinTokenFolderPath(base, slug string) string {
	if trimmed := strings.Trim(base, "/"); trimmed != "" {
		return "/" + trimmed + "/" + slug
	}
	return "/" + slug
}

// RevokeAccessToken revokes (soft-deletes) an access token by its stable id.
// There is deliberately no create counterpart — tokens are read/delete only.
func (c *KagiClient) RevokeAccessToken(id string) error {
	_, err := c.doRequest("DELETE", "/kagi/folders/access-tokens/by-id/"+url.PathEscape(id))
	return err
}
