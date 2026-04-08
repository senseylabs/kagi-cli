package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	kagi "github.com/senseylabs/kagi-sdk"

	"github.com/senseylabs/kagi-cli/internal/auth"
)

// Re-export SDK types so existing CLI code doesn't break.
type Project = kagi.Project
type App = kagi.App
type Environment = kagi.Environment
type SecretFetchResponse = kagi.SecretFetchResponse

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
}

// NewKagiClientWithToken creates a client with an explicit token (used during login before org is resolved).
func NewKagiClientWithToken(baseURL, token string) *KagiClient {
	return &KagiClient{
		baseURL: baseURL,
		token:   token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		sdkClient: kagi.NewClient(baseURL, token),
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

	// Check KAGI_TOKEN env var first (PAT for CI)
	if pat := os.Getenv("KAGI_TOKEN"); pat != "" {
		c.token = pat
		c.sdkClient = kagi.NewClient(baseURL, pat)
		return c, nil
	}

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

	// Refresh if expired
	if time.Now().After(creds.ExpiresAt) {
		refreshIssuer := creds.IssuerURL
		if refreshIssuer == "" {
			refreshIssuer = issuerURL
		}
		deviceFlow := auth.NewDeviceFlow(refreshIssuer, "kagi-cli", "openid")
		endpoints, err := deviceFlow.DiscoverEndpoints()
		if err != nil {
			return nil, fmt.Errorf("session expired. Run 'kagi login' to re-authenticate")
		}

		newToken, err := deviceFlow.RefreshToken(endpoints.TokenEndpoint, creds.RefreshToken)
		if err != nil {
			return nil, fmt.Errorf("session expired. Run 'kagi login' to re-authenticate")
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

	c.token = creds.AccessToken
	c.sdkClient = kagi.NewClient(baseURL, creds.AccessToken)
	return c, nil
}

// ---------------------------------------------------------------------------
// Read-only operations — delegated to the SDK client
// ---------------------------------------------------------------------------

// ListProjects returns all projects.
func (c *KagiClient) ListProjects() ([]Project, error) {
	return c.sdkClient.ListProjects(context.Background())
}

// ListApps returns all apps for a project.
func (c *KagiClient) ListApps(projectSlug string) ([]App, error) {
	return c.sdkClient.ListApps(context.Background(), projectSlug)
}

// ListEnvironments returns all environments for a project.
func (c *KagiClient) ListEnvironments(projectSlug string) ([]Environment, error) {
	return c.sdkClient.ListEnvironments(context.Background(), projectSlug)
}

// FetchSecrets returns decrypted secrets as key-value pairs for an app's environment.
func (c *KagiClient) FetchSecrets(appID, envID string) (map[string]string, error) {
	return c.sdkClient.FetchSecrets(context.Background(), appID, envID)
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

func (c *KagiClient) doRequest(method, path string) ([]byte, error) {
	url := c.baseURL + path
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if os.IsTimeout(err) || strings.Contains(err.Error(), "deadline exceeded") || strings.Contains(err.Error(), "connection refused") {
			return nil, fmt.Errorf("could not connect to %s. Check your network or if the API is running", c.baseURL)
		}
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response from %s: %w", url, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr APIErrorResponse
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Message != "" {
			return nil, fmt.Errorf("%s", apiErr.Message)
		}

		switch resp.StatusCode {
		case 401:
			return nil, fmt.Errorf("unauthorized. Run 'kagi login' to authenticate")
		case 403:
			return nil, fmt.Errorf("access denied. You may not have permission for this operation")
		case 404:
			return nil, fmt.Errorf("resource not found")
		case 500:
			return nil, fmt.Errorf("server error. Try again later")
		default:
			bodyStr := string(body)
			if len(bodyStr) > 200 {
				bodyStr = bodyStr[:200] + "..."
			}
			return nil, fmt.Errorf("request failed (%d): %s", resp.StatusCode, bodyStr)
		}
	}

	return body, nil
}

// doRequestWithBody sends an HTTP request with a JSON body and returns the response bytes.
func (c *KagiClient) doRequestWithBody(method, path string, payload interface{}) ([]byte, error) {
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

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if os.IsTimeout(err) || strings.Contains(err.Error(), "deadline exceeded") || strings.Contains(err.Error(), "connection refused") {
			return nil, fmt.Errorf("could not connect to %s. Check your network or if the API is running", c.baseURL)
		}
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response from %s: %w", url, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr APIErrorResponse
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Message != "" {
			return nil, fmt.Errorf("%s", apiErr.Message)
		}

		switch resp.StatusCode {
		case 401:
			return nil, fmt.Errorf("unauthorized. Run 'kagi login' to authenticate")
		case 403:
			return nil, fmt.Errorf("access denied. You may not have permission for this operation")
		case 404:
			return nil, fmt.Errorf("resource not found")
		case 500:
			return nil, fmt.Errorf("server error. Try again later")
		default:
			bodyStr := string(body)
			if len(bodyStr) > 200 {
				bodyStr = bodyStr[:200] + "..."
			}
			return nil, fmt.Errorf("request failed (%d): %s", resp.StatusCode, bodyStr)
		}
	}

	return body, nil
}

// CreateProject creates a new Kagi project.
func (c *KagiClient) CreateProject(name, description string) (*Project, error) {
	payload := map[string]string{
		"name":        name,
		"description": description,
	}

	body, err := c.doRequestWithBody("POST", "/kagi/projects", payload)
	if err != nil {
		return nil, err
	}

	var resp kagi.APIResponse[Project]
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse create project response: %w", err)
	}

	return &resp.Data, nil
}

// DeleteProject deletes a Kagi project by ID.
func (c *KagiClient) DeleteProject(projectID string) error {
	_, err := c.doRequest("DELETE", fmt.Sprintf("/kagi/projects/%s", projectID))
	return err
}

// CreateEnvironment creates a new environment within a project.
func (c *KagiClient) CreateEnvironment(projectSlug, name, slug string) (*Environment, error) {
	payload := map[string]string{
		"name": name,
		"slug": slug,
	}

	body, err := c.doRequestWithBody("POST", fmt.Sprintf("/kagi/projects/%s/environments", projectSlug), payload)
	if err != nil {
		return nil, err
	}

	var resp kagi.APIResponse[Environment]
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse create environment response: %w", err)
	}

	return &resp.Data, nil
}

// DeleteEnvironment deletes an environment by ID within a project.
func (c *KagiClient) DeleteEnvironment(projectSlug, envID string) error {
	_, err := c.doRequest("DELETE", fmt.Sprintf("/kagi/projects/%s/environments/%s", projectSlug, envID))
	return err
}

// CreateApp creates a new app within a project.
func (c *KagiClient) CreateApp(projectSlug, name, description string) (*App, error) {
	payload := map[string]string{
		"name":        name,
		"description": description,
	}

	body, err := c.doRequestWithBody("POST", fmt.Sprintf("/kagi/projects/%s/apps", projectSlug), payload)
	if err != nil {
		return nil, err
	}

	var resp kagi.APIResponse[App]
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse create app response: %w", err)
	}

	return &resp.Data, nil
}

// DeleteApp deletes an app by ID within a project.
func (c *KagiClient) DeleteApp(projectSlug, appID string) error {
	_, err := c.doRequest("DELETE", fmt.Sprintf("/kagi/projects/%s/apps/%s", projectSlug, appID))
	return err
}

// SetSecrets performs a bulk upsert of secrets for an app in an environment.
func (c *KagiClient) SetSecrets(appID, envID string, secrets map[string]string) error {
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

	_, err := c.doRequestWithBody("POST", fmt.Sprintf("/kagi/apps/%s/environments/%s/secrets/bulk", appID, envID), payload)
	return err
}

// GetSecret reveals (decrypts) a single secret by ID.
func (c *KagiClient) GetSecret(appID, envID, secretID string) (*SecretRevealResponse, error) {
	body, err := c.doRequest("GET", fmt.Sprintf("/kagi/apps/%s/environments/%s/secrets/%s/reveal", appID, envID, secretID))
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
func (c *KagiClient) DeleteSecret(appID, envID, secretID string) error {
	_, err := c.doRequest("DELETE", fmt.Sprintf("/kagi/apps/%s/environments/%s/secrets/%s", appID, envID, secretID))
	return err
}

// ListSecrets returns all secrets for an app's environment with masked values.
func (c *KagiClient) ListSecrets(appID, envID string) ([]SecretListItem, error) {
	body, err := c.doRequest("GET", fmt.Sprintf("/kagi/apps/%s/environments/%s/secrets", appID, envID))
	if err != nil {
		return nil, err
	}

	var resp kagi.APIResponse[[]SecretListItem]
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse secrets list response: %w", err)
	}

	return resp.Data, nil
}
