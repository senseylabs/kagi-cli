package kagi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// helper to create a test server that responds with a JSON APIResponse.
func newTestServer(t *testing.T, wantPath string, data interface{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != wantPath {
			t.Errorf("unexpected path: got %s, want %s", r.URL.Path, wantPath)
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method: got %s, want GET", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("unexpected Authorization header: got %q", got)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		resp := map[string]interface{}{
			"data":    data,
			"message": "success",
			"status":  200,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
}

func TestListProjects(t *testing.T) {
	projects := []Project{
		{ID: "p1", Name: "Project One", Slug: "project-one", Description: "First project"},
		{ID: "p2", Name: "Project Two", Slug: "project-two", Description: "Second project"},
	}
	ts := newTestServer(t, "/kagi/projects", projects)
	defer ts.Close()

	client := NewClient(ts.URL, "test-token")
	result, err := client.ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects returned error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(result))
	}
	if result[0].ID != "p1" || result[0].Name != "Project One" {
		t.Errorf("unexpected first project: %+v", result[0])
	}
	if result[1].ID != "p2" || result[1].Name != "Project Two" {
		t.Errorf("unexpected second project: %+v", result[1])
	}
}

func TestListApps(t *testing.T) {
	apps := []App{
		{ID: "a1", Name: "App One", Slug: "app-one", Description: "First app"},
		{ID: "a2", Name: "App Two", Slug: "app-two", Description: "Second app"},
	}
	ts := newTestServer(t, "/kagi/projects/proj-123/apps", apps)
	defer ts.Close()

	client := NewClient(ts.URL, "test-token")
	result, err := client.ListApps(context.Background(), "proj-123")
	if err != nil {
		t.Fatalf("ListApps returned error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 apps, got %d", len(result))
	}
	if result[0].Slug != "app-one" {
		t.Errorf("unexpected first app slug: %s", result[0].Slug)
	}
}

func TestListEnvironments(t *testing.T) {
	envs := []Environment{
		{ID: "e1", Name: "Production", Slug: "production"},
		{ID: "e2", Name: "Staging", Slug: "staging"},
	}
	ts := newTestServer(t, "/kagi/projects/proj-123/environments", envs)
	defer ts.Close()

	client := NewClient(ts.URL, "test-token")
	result, err := client.ListEnvironments(context.Background(), "proj-123")
	if err != nil {
		t.Fatalf("ListEnvironments returned error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 environments, got %d", len(result))
	}
	if result[0].Name != "Production" {
		t.Errorf("unexpected first environment: %+v", result[0])
	}
}

func TestFetchSecrets(t *testing.T) {
	secretData := SecretFetchResponse{
		Secrets: map[string]string{
			"DB_HOST":     "localhost",
			"DB_PASSWORD": "secret123",
		},
	}
	ts := newTestServer(t, "/kagi/apps/app-1/environments/env-1/secrets/fetch", secretData)
	defer ts.Close()

	client := NewClient(ts.URL, "test-token")
	result, err := client.FetchSecrets(context.Background(), "app-1", "env-1")
	if err != nil {
		t.Fatalf("FetchSecrets returned error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 secrets, got %d", len(result))
	}
	if result["DB_HOST"] != "localhost" {
		t.Errorf("unexpected DB_HOST: %s", result["DB_HOST"])
	}
	if result["DB_PASSWORD"] != "secret123" {
		t.Errorf("unexpected DB_PASSWORD: %s", result["DB_PASSWORD"])
	}
}

func TestListOrganizations(t *testing.T) {
	orgs := []Organization{
		{ID: "o1", Name: "Sensey", Slug: "sensey"},
		{ID: "o2", Name: "Acme", Slug: "acme"},
	}
	ts := newTestServer(t, "/kagi/organizations", orgs)
	defer ts.Close()

	client := NewClient(ts.URL, "test-token")
	result, err := client.ListOrganizations(context.Background())
	if err != nil {
		t.Fatalf("ListOrganizations returned error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 organizations, got %d", len(result))
	}
	if result[0].Slug != "sensey" || result[0].ID != "o1" {
		t.Errorf("unexpected first organization: %+v", result[0])
	}
	if result[1].Slug != "acme" {
		t.Errorf("unexpected second organization: %+v", result[1])
	}
}

// orgHeaderServer captures the X-Organization-ID header value the client sent.
func orgHeaderServer(t *testing.T, gotHeader *string, present *bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values, ok := r.Header[http.CanonicalHeaderKey(HeaderOrganizationID)]
		*present = ok
		if ok {
			*gotHeader = values[0]
		}
		resp := map[string]interface{}{"data": []Project{}, "message": "ok", "status": 200}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
}

func TestOrgHeader_SentForJWT(t *testing.T) {
	var gotHeader string
	var present bool
	ts := orgHeaderServer(t, &gotHeader, &present)
	defer ts.Close()

	client := NewOrgClient(ts.URL, "jwt-token", "org-uuid-123", false /* isPAT */)
	if _, err := client.ListProjects(context.Background()); err != nil {
		t.Fatalf("ListProjects returned error: %v", err)
	}

	if !present {
		t.Fatalf("expected %s header to be sent for JWT auth, but it was absent", HeaderOrganizationID)
	}
	if gotHeader != "org-uuid-123" {
		t.Errorf("unexpected %s header: got %q, want %q", HeaderOrganizationID, gotHeader, "org-uuid-123")
	}
}

func TestOrgHeader_NotSentForPAT(t *testing.T) {
	var gotHeader string
	var present bool
	ts := orgHeaderServer(t, &gotHeader, &present)
	defer ts.Close()

	// Even with an orgID supplied, a PAT client must NOT send the header — the
	// org is bound to the token and a mismatch would be rejected with 403.
	client := NewOrgClient(ts.URL, "vv_pat_token", "org-uuid-123", true /* isPAT */)
	if _, err := client.ListProjects(context.Background()); err != nil {
		t.Fatalf("ListProjects returned error: %v", err)
	}

	if present {
		t.Errorf("expected %s header to be absent for PAT auth, but got %q", HeaderOrganizationID, gotHeader)
	}
}

func TestOrgScopedRequest_FailsFastWhenNoOrgSelected(t *testing.T) {
	// An org-aware JWT client with no org selected must fail fast with an
	// actionable error, never reaching the server.
	reached := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	client := NewOrgClient(ts.URL, "jwt-token", "", false /* isPAT */)
	_, err := client.ListProjects(context.Background())
	if err == nil {
		t.Fatal("expected ErrNoOrganizationSelected, got nil")
	}
	if err != ErrNoOrganizationSelected {
		t.Errorf("unexpected error: got %v, want %v", err, ErrNoOrganizationSelected)
	}
	if reached {
		t.Error("server should not have been reached when no org is selected")
	}
}

func TestListOrganizations_AllowedWithoutOrgSelected(t *testing.T) {
	// The org-list endpoint is the one org-scoped path reachable before an org
	// is chosen — it is how the user discovers selectable orgs.
	orgs := []Organization{{ID: "o1", Name: "Sensey", Slug: "sensey"}}
	ts := newTestServer(t, "/kagi/organizations", orgs)
	defer ts.Close()

	client := NewOrgClient(ts.URL, "test-token", "", false /* isPAT */)
	result, err := client.ListOrganizations(context.Background())
	if err != nil {
		t.Fatalf("ListOrganizations returned error: %v", err)
	}
	if len(result) != 1 || result[0].Slug != "sensey" {
		t.Errorf("unexpected organizations: %+v", result)
	}
}

func TestPlainClient_NotOrgGated(t *testing.T) {
	// The bare NewClient is unopinionated: an empty orgID does not fail-fast and
	// no org header is sent (back-compat for existing callers / PAT-style usage).
	var present bool
	var gotHeader string
	ts := orgHeaderServer(t, &gotHeader, &present)
	defer ts.Close()

	client := NewClient(ts.URL, "test-token")
	if _, err := client.ListProjects(context.Background()); err != nil {
		t.Fatalf("ListProjects returned error: %v", err)
	}
	if present {
		t.Errorf("plain client should not send %s, got %q", HeaderOrganizationID, gotHeader)
	}
}

func TestErrorHandling_Non200(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message": "access denied", "status": 403}`))
	}))
	defer ts.Close()

	client := NewClient(ts.URL, "test-token")
	_, err := client.ListProjects(context.Background())
	if err == nil {
		t.Fatal("expected error for 403 response, got nil")
	}
	if got := err.Error(); got == "" {
		t.Error("error message should not be empty")
	}
}

func TestErrorHandling_NetworkError(t *testing.T) {
	// Use a server that's already closed to simulate network error.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	ts.Close()

	client := NewClient(ts.URL, "test-token")
	_, err := client.ListProjects(context.Background())
	if err == nil {
		t.Fatal("expected error for closed server, got nil")
	}
}

func TestErrorHandling_CancelledContext(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{"data": []Project{}, "message": "ok", "status": 200})
	}))
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	client := NewClient(ts.URL, "test-token")
	_, err := client.ListProjects(ctx)
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}
