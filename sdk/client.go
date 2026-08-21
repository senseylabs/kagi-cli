package kagi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
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
	// isPAT reports whether token is a Personal Access Token. When true the org
	// header is never sent — the backend resolves the org from the token itself
	// and rejects a mismatched header (403).
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
//
// A JWT client is org-aware: an org-scoped request with no org selected fails
// fast with ErrNoOrganizationSelected rather than being sent without a header.
// A PAT client never needs a selected org, so it is exempt from that check.
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

// ListFolderChildren browses a folder by path and returns its child folders
// and, for the SECRETS library, the apps directly under it. The path is the
// human-readable folder path (e.g. "/fepatex/backend"); an empty or "/" path
// browses the library root. It hits
// GET /kagi/folders/{library}/children/{*path}.
func (c *Client) ListFolderChildren(ctx context.Context, library KagiLibrary, path string) (*FolderChildren, error) {
	var resp APIResponse[FolderChildren]
	if err := c.doGet(ctx, folderChildrenPath(library, path), &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

// ErrAppNotFound is returned by ResolveApp when the parent folder is reachable
// but contains no app with the requested slug. It is distinct from a transport
// or authorization error on the browse itself, letting callers tell "app does
// not exist" apart from "no access to the folder".
var ErrAppNotFound = errors.New("app not found")

// ResolveApp resolves a human-entered SECRETS folder path to an app's stable
// internal ID. The final path segment is the app slug; the preceding segments
// identify the folder the app lives in. The returned app ID is the durable
// machine binding and should be captured once at setup, then used to address
// secrets thereafter (it is stable across app renames and folder moves).
func (c *Client) ResolveApp(ctx context.Context, folderPath string) (string, error) {
	trimmed := strings.Trim(folderPath, "/")
	if trimmed == "" {
		return "", fmt.Errorf("kagi: folder path %q does not address an app", folderPath)
	}

	segments := strings.Split(trimmed, "/")
	appSlug := segments[len(segments)-1]
	parentPath := "/" + strings.Join(segments[:len(segments)-1], "/")

	children, err := c.ListFolderChildren(ctx, LibrarySecrets, parentPath)
	if err != nil {
		return "", err
	}

	for _, app := range children.Apps {
		if app.Slug == appSlug {
			return app.ID, nil
		}
	}

	return "", fmt.Errorf("kagi: no app with slug %q under folder %q: %w", appSlug, parentPath, ErrAppNotFound)
}

// ListApps returns the apps directly under a SECRETS folder path.
func (c *Client) ListApps(ctx context.Context, folderPath string) ([]App, error) {
	children, err := c.ListFolderChildren(ctx, LibrarySecrets, folderPath)
	if err != nil {
		return nil, err
	}
	return children.Apps, nil
}

// ListEnvironments returns all environments for an app, addressed by its stable
// app ID. It hits GET /kagi/apps/{appId}/environments.
func (c *Client) ListEnvironments(ctx context.Context, appID string) ([]Environment, error) {
	var resp APIResponse[[]Environment]
	if err := c.doGet(ctx, fmt.Sprintf("/kagi/apps/%s/environments", appID), &resp); err != nil {
		return nil, err
	}
	return resp.Data, nil
}

// FetchSecrets returns decrypted secrets as key-value pairs for an app's
// environment, addressed by the stable app ID and the environment slug. It hits
// GET /kagi/apps/{appId}/environments/{environmentSlug}/secrets/fetch (the
// plaintext machine-fetch variant).
func (c *Client) FetchSecrets(ctx context.Context, appID, environmentSlug string) (map[string]string, error) {
	var resp APIResponse[SecretFetchResponse]
	if err := c.doGet(ctx, fmt.Sprintf("/kagi/apps/%s/environments/%s/secrets/fetch", appID, environmentSlug), &resp); err != nil {
		return nil, err
	}
	return resp.Data.Secrets, nil
}

// ListCertificates returns all certificates. The backend clamps page size to
// PageableSanitizer.DEFAULT_MAX_SIZE (200), so a single oversized request would
// silently truncate at 200 rows; results are instead collected by paging in
// maxPageSize-row batches until a short page marks the end.
func (c *Client) ListCertificates(ctx context.Context) ([]CertificateListItem, error) {
	return fetchAllPages[CertificateListItem](ctx, c, "certificates", func(page int) string {
		return fmt.Sprintf("/kagi/certificates?page=%d&size=%d&sort=name", page, maxPageSize)
	})
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

// ListCertificateFolderChildren browses a CERTIFICATES folder path and returns
// its child folders. Unlike the SECRETS library the children listing carries no
// leaf items for certificates (its Apps slice is always empty); the certificate
// leaves under a folder are served by ListCertificatesInFolder. Mirrors the
// secrets browse, split across two endpoints because the backend serves cert
// leaves through a dedicated /items route.
func (c *Client) ListCertificateFolderChildren(ctx context.Context, path string) (*FolderChildren, error) {
	return c.ListFolderChildren(ctx, LibraryCertificates, path)
}

// ListCertificatesInFolder returns the certificates held directly inside the
// certificate folder addressed by path. It hits
// GET /kagi/folders/certificates/items/{*path}. An empty or "/" path lists the
// certificates at the certificates root. The backend clamps page size to 200,
// so the folder is paged in maxPageSize-row batches until a short page marks the
// end rather than trusting a single oversized request.
func (c *Client) ListCertificatesInFolder(ctx context.Context, path string) ([]CertificateFolderItem, error) {
	return fetchAllPages[CertificateFolderItem](ctx, c, "certificate folder items", func(page int) string {
		return certificateItemsPath(path, page)
	})
}

// ResolveCertificate resolves a human-entered certificate node path to the
// certificate's stable id and display name. The final path segment is the
// certificate slug; the preceding segments identify its containing folder. It
// hits GET /kagi/folders/certificates/resolve/{*path}. The returned id is the
// durable machine binding, valid across renames and folder moves — the
// certificate analog of the secrets ResolveApp step.
func (c *Client) ResolveCertificate(ctx context.Context, path string) (*CertificateResolve, error) {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil, fmt.Errorf("kagi: certificate path %q does not address a certificate", path)
	}

	var resp APIResponse[CertificateResolve]
	if err := c.doGet(ctx, certificateResolvePath(path), &resp); err != nil {
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

// ErrPasswordNotFound is returned by ResolvePassword when the parent folder is
// reachable but holds no password whose username matches the requested leaf. It
// is distinct from a transport or authorization error on the browse itself,
// mirroring ErrAppNotFound for the secrets resolve path.
var ErrPasswordNotFound = errors.New("password not found")

// ListPasswordFolderChildren browses a PASSWORDS folder path and returns its
// child folders. Like the certificates library the children listing carries no
// leaf items for passwords (its Apps slice is always empty); the password leaves
// under a folder are served by ListPasswordsInFolder. It hits
// GET /kagi/folders/passwords/children/{*path}.
func (c *Client) ListPasswordFolderChildren(ctx context.Context, path string) (*FolderChildren, error) {
	return c.ListFolderChildren(ctx, LibraryPasswords, path)
}

// ListPasswordsInFolder returns the passwords held directly inside the password
// folder addressed by path, with masked values. It hits
// GET /kagi/folders/passwords/items/{*path}. An empty or "/" path lists the
// passwords at the passwords root. The backend clamps page size to 200, so the
// folder is paged in maxPageSize-row batches until a short page marks the end.
// Results are sorted by username — the entity has no name.
func (c *Client) ListPasswordsInFolder(ctx context.Context, path string) ([]PasswordListItem, error) {
	return fetchAllPages[PasswordListItem](ctx, c, "password folder items", func(page int) string {
		return passwordItemsPath(path, page)
	})
}

// ResolvePassword resolves a human-entered password node path to the password's
// stable id and login username. The final path segment is matched
// case-insensitively against the login username of the passwords held directly
// in the preceding folder; the earlier segments identify that folder. Passwords
// have no dedicated resolve endpoint and no name/slug, so this browses the
// parent folder's leaves — the password analog of the secrets ResolveApp step.
// The returned id is the durable machine binding, valid across folder moves.
func (c *Client) ResolvePassword(ctx context.Context, path string) (*PasswordResolve, error) {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil, fmt.Errorf("kagi: password path %q does not address a password", path)
	}

	segments := strings.Split(trimmed, "/")
	leaf := segments[len(segments)-1]
	parentPath := "/" + strings.Join(segments[:len(segments)-1], "/")

	items, err := c.ListPasswordsInFolder(ctx, parentPath)
	if err != nil {
		return nil, err
	}

	for _, it := range items {
		if strings.EqualFold(it.Username, leaf) {
			return &PasswordResolve{PasswordID: it.ID, Username: it.Username}, nil
		}
	}

	return nil, fmt.Errorf("kagi: no password with username %q under folder %q: %w", leaf, parentPath, ErrPasswordNotFound)
}

// GetPasswordDetail returns a single password's masked metadata by id. Passwords
// are folder-independent, so this is addressed by id rather than by folder path.
// The backend serves the list DTO for this route, so the detail shares the
// PasswordListItem shape. It hits GET /kagi/folders/passwords/by-id/{id}.
func (c *Client) GetPasswordDetail(ctx context.Context, passwordID string) (*PasswordListItem, error) {
	var resp APIResponse[PasswordListItem]
	if err := c.doGet(ctx, fmt.Sprintf("/kagi/folders/passwords/by-id/%s", passwordID), &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

// RevealPassword returns the decrypted password value by id. It hits
// GET /kagi/folders/passwords/by-id/{id}/reveal.
func (c *Client) RevealPassword(ctx context.Context, passwordID string) (*PasswordReveal, error) {
	var resp APIResponse[PasswordReveal]
	if err := c.doGet(ctx, fmt.Sprintf("/kagi/folders/passwords/by-id/%s/reveal", passwordID), &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

// GetPasswordHistory returns the change history for a password by id. History
// metadata never carries the plaintext value. It hits
// GET /kagi/folders/passwords/by-id/{id}/history.
func (c *Client) GetPasswordHistory(ctx context.Context, passwordID string) ([]PasswordHistory, error) {
	var resp APIResponse[[]PasswordHistory]
	if err := c.doGet(ctx, fmt.Sprintf("/kagi/folders/passwords/by-id/%s/history", passwordID), &resp); err != nil {
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

// folderChildrenPath builds the folder-children browse URL. The Kagi route uses
// a terminal capturing wildcard (/kagi/folders/{library}/children/{*path}), so
// the path is appended last with a single leading slash; an empty/"/" path
// browses the root (no trailing segment).
func folderChildrenPath(library KagiLibrary, path string) string {
	return "/kagi/folders/" + string(library) + "/children" + normalizeFolderPath(path)
}

// certificateItemsPath builds the folder-addressed certificate-items URL for a
// zero-based page. Like folderChildrenPath the route uses a terminal capturing
// wildcard, so the path is appended last; an empty/"/" path lists the root. The
// backend clamps size to 200, so the caller pages in maxPageSize-row batches.
func certificateItemsPath(path string, page int) string {
	return fmt.Sprintf("/kagi/folders/certificates/items%s?page=%d&size=%d&sort=name",
		normalizeFolderPath(path), page, maxPageSize)
}

// certificateResolvePath builds the certificate node-path resolve URL. The route
// uses a terminal capturing wildcard, so the certificate node path (folder
// segments then the certificate slug) is appended last.
func certificateResolvePath(path string) string {
	return "/kagi/folders/certificates/resolve" + normalizeFolderPath(path)
}

// passwordItemsPath builds the folder-addressed password-items URL for a
// zero-based page. Like certificateItemsPath the route uses a terminal capturing
// wildcard, so the path is appended last; an empty/"/" path lists the root. The
// backend clamps size to 200, so the caller pages in maxPageSize-row batches.
// Sort is by username because the KagiPassword entity has no name property.
func passwordItemsPath(path string, page int) string {
	return fmt.Sprintf("/kagi/folders/passwords/items%s?page=%d&size=%d&sort=username",
		normalizeFolderPath(path), page, maxPageSize)
}

// normalizeFolderPath collapses a folder path to the canonical wildcard suffix:
// "" for the root, or "/seg1/seg2" otherwise. Folder slugs are [a-z0-9-] only,
// so no URL escaping is required.
func normalizeFolderPath(path string) string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return ""
	}
	return "/" + trimmed
}

// maxPageSize is the largest page the backend will honor: PageableSanitizer
// clamps any requested size to DEFAULT_MAX_SIZE (200), so a larger ?size is
// silently reduced and truncates the result. List calls page in this size.
const maxPageSize = 200

// maxListPages bounds the pagination loop so an unexpectedly large (or
// ever-growing) collection cannot spin forever. Reaching it logs a warning and
// returns what was gathered so far.
const maxListPages = 100

// fetchAllPages walks Spring pages 0,1,2,... for a list endpoint, accumulating
// rows until a page returns fewer than maxPageSize items (the final page) or the
// page cap is hit. pathForPage builds the request path for a given zero-based
// page number and must request size=maxPageSize. resource names the collection
// for the cap-exceeded warning.
func fetchAllPages[T any](ctx context.Context, c *Client, resource string, pathForPage func(page int) string) ([]T, error) {
	var all []T
	for page := 0; page < maxListPages; page++ {
		var resp APIResponse[[]T]
		if err := c.doGet(ctx, pathForPage(page), &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Data...)
		if len(resp.Data) < maxPageSize {
			return all, nil
		}
	}
	fmt.Fprintf(os.Stderr, "warning: kagi: %s list hit the %d-page cap; results may be truncated\n", resource, maxListPages)
	return all, nil
}

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
		return newAPIError(resp.StatusCode, body)
	}

	if err := json.Unmarshal(body, result); err != nil {
		return fmt.Errorf("kagi: failed to parse response: %w", err)
	}

	return nil
}
