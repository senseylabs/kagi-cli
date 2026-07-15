package kagi

import (
	"context"
	"encoding/json"
	"errors"
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

func TestListFolderChildren(t *testing.T) {
	children := FolderChildren{
		Path:    "/village",
		Folders: []Folder{{ID: "f1", Name: "Backend", Slug: "backend", Path: "/village/backend"}},
		Apps:    []App{{ID: "a1", Name: "Kaizen", Slug: "kaizen"}},
	}
	ts := newTestServer(t, "/kagi/folders/secrets/children/village", children)
	defer ts.Close()

	client := NewClient(ts.URL, "test-token")
	result, err := client.ListFolderChildren(context.Background(), LibrarySecrets, "/village")
	if err != nil {
		t.Fatalf("ListFolderChildren returned error: %v", err)
	}
	if len(result.Folders) != 1 || result.Folders[0].Slug != "backend" {
		t.Errorf("unexpected folders: %+v", result.Folders)
	}
	if len(result.Apps) != 1 || result.Apps[0].ID != "a1" {
		t.Errorf("unexpected apps: %+v", result.Apps)
	}
}

func TestListFolderChildren_Root(t *testing.T) {
	// An empty/"/" path browses the library root: the wildcard suffix is empty,
	// so the URL ends at .../children with no trailing segment.
	children := FolderChildren{Path: "/", Folders: []Folder{{ID: "f1", Slug: "village"}}}
	ts := newTestServer(t, "/kagi/folders/secrets/children", children)
	defer ts.Close()

	client := NewClient(ts.URL, "test-token")
	result, err := client.ListFolderChildren(context.Background(), LibrarySecrets, "/")
	if err != nil {
		t.Fatalf("ListFolderChildren returned error: %v", err)
	}
	if len(result.Folders) != 1 || result.Folders[0].Slug != "village" {
		t.Errorf("unexpected folders: %+v", result.Folders)
	}
}

func TestListApps(t *testing.T) {
	children := FolderChildren{
		Path: "/village",
		Apps: []App{
			{ID: "a1", Name: "App One", Slug: "app-one"},
			{ID: "a2", Name: "App Two", Slug: "app-two"},
		},
	}
	ts := newTestServer(t, "/kagi/folders/secrets/children/village", children)
	defer ts.Close()

	client := NewClient(ts.URL, "test-token")
	result, err := client.ListApps(context.Background(), "/village")
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

func TestResolveApp(t *testing.T) {
	// ResolveApp browses the PARENT folder of the app and matches the final path
	// segment (the app slug) to return the stable app ID.
	children := FolderChildren{
		Path: "/village",
		Apps: []App{
			{ID: "app-kaizen", Name: "Kaizen", Slug: "kaizen"},
			{ID: "app-korur", Name: "Korur", Slug: "korur"},
		},
	}
	ts := newTestServer(t, "/kagi/folders/secrets/children/village", children)
	defer ts.Close()

	client := NewClient(ts.URL, "test-token")
	appID, err := client.ResolveApp(context.Background(), "/village/kaizen")
	if err != nil {
		t.Fatalf("ResolveApp returned error: %v", err)
	}
	if appID != "app-kaizen" {
		t.Errorf("unexpected app ID: got %q, want %q", appID, "app-kaizen")
	}
}

func TestResolveApp_NotFound(t *testing.T) {
	// A reachable parent folder with no matching app slug yields ErrAppNotFound,
	// distinguishing "app does not exist" from a transport/authorization failure.
	children := FolderChildren{Path: "/village", Apps: []App{{ID: "app-korur", Slug: "korur"}}}
	ts := newTestServer(t, "/kagi/folders/secrets/children/village", children)
	defer ts.Close()

	client := NewClient(ts.URL, "test-token")
	_, err := client.ResolveApp(context.Background(), "/village/kaizen")
	if err == nil {
		t.Fatal("expected ErrAppNotFound, got nil")
	}
	if !errors.Is(err, ErrAppNotFound) {
		t.Errorf("expected ErrAppNotFound, got %v", err)
	}
}

func TestResolveApp_EmptyPath(t *testing.T) {
	// A bare "/" does not address an app — it must error without a request.
	reached := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	client := NewClient(ts.URL, "test-token")
	if _, err := client.ResolveApp(context.Background(), "/"); err == nil {
		t.Fatal("expected error for empty path, got nil")
	}
	if reached {
		t.Error("server should not have been reached for an empty path")
	}
}

func TestListCertificateFolderChildren(t *testing.T) {
	// The certificates children listing carries child folders only; its Apps
	// slice is always empty (certificate leaves come from the /items endpoint).
	children := FolderChildren{
		Path:    "/websites",
		Folders: []Folder{{ID: "f1", Name: "Korur", Slug: "korur", Path: "/websites/korur"}},
	}
	ts := newTestServer(t, "/kagi/folders/certificates/children/websites", children)
	defer ts.Close()

	client := NewClient(ts.URL, "test-token")
	result, err := client.ListCertificateFolderChildren(context.Background(), "/websites")
	if err != nil {
		t.Fatalf("ListCertificateFolderChildren returned error: %v", err)
	}
	if len(result.Folders) != 1 || result.Folders[0].Slug != "korur" {
		t.Errorf("unexpected folders: %+v", result.Folders)
	}
}

func TestListCertificatesInFolder(t *testing.T) {
	certs := []CertificateFolderItem{
		{ID: "c1", Name: "sensey-io-cloudflare-cert", Slug: "sensey-io-cloudflare-cert", SANs: "*.sensey.io,sensey.io"},
		{ID: "c2", Name: "kagi-pw-cloudflare-cert", Slug: "kagi-pw-cloudflare-cert"},
	}
	ts := newTestServer(t, "/kagi/folders/certificates/items/sensey", certs)
	defer ts.Close()

	client := NewClient(ts.URL, "test-token")
	result, err := client.ListCertificatesInFolder(context.Background(), "/sensey")
	if err != nil {
		t.Fatalf("ListCertificatesInFolder returned error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 certificates, got %d", len(result))
	}
	if result[0].ID != "c1" || result[0].Slug != "sensey-io-cloudflare-cert" {
		t.Errorf("unexpected first certificate: %+v", result[0])
	}
}

func TestListCertificatesInFolder_Root(t *testing.T) {
	// An empty/"/" path lists the certificates root: the wildcard suffix is
	// empty, so the URL ends at .../items with no trailing segment.
	ts := newTestServer(t, "/kagi/folders/certificates/items", []CertificateFolderItem{})
	defer ts.Close()

	client := NewClient(ts.URL, "test-token")
	result, err := client.ListCertificatesInFolder(context.Background(), "/")
	if err != nil {
		t.Fatalf("ListCertificatesInFolder returned error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected no certificates at root, got %d", len(result))
	}
}

func TestResolveCertificate(t *testing.T) {
	resolved := CertificateResolve{CertificateID: "cert-sensey", Name: "sensey-io-cloudflare-cert"}
	ts := newTestServer(t, "/kagi/folders/certificates/resolve/sensey/sensey-io-cloudflare-cert", resolved)
	defer ts.Close()

	client := NewClient(ts.URL, "test-token")
	result, err := client.ResolveCertificate(context.Background(), "/sensey/sensey-io-cloudflare-cert")
	if err != nil {
		t.Fatalf("ResolveCertificate returned error: %v", err)
	}
	if result.CertificateID != "cert-sensey" {
		t.Errorf("unexpected certificate id: got %q, want %q", result.CertificateID, "cert-sensey")
	}
	if result.Name != "sensey-io-cloudflare-cert" {
		t.Errorf("unexpected certificate name: %q", result.Name)
	}
}

func TestResolveCertificate_EmptyPath(t *testing.T) {
	// A bare "/" does not address a certificate — it must error without a request.
	reached := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	client := NewClient(ts.URL, "test-token")
	if _, err := client.ResolveCertificate(context.Background(), "/"); err == nil {
		t.Fatal("expected error for empty path, got nil")
	}
	if reached {
		t.Error("server should not have been reached for an empty path")
	}
}

func TestListEnvironments(t *testing.T) {
	envs := []Environment{
		{ID: "e1", Name: "Production", Slug: "production"},
		{ID: "e2", Name: "Staging", Slug: "staging"},
	}
	ts := newTestServer(t, "/kagi/apps/app-123/environments", envs)
	defer ts.Close()

	client := NewClient(ts.URL, "test-token")
	result, err := client.ListEnvironments(context.Background(), "app-123")
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
	ts := newTestServer(t, "/kagi/apps/app-1/environments/production/secrets/fetch", secretData)
	defer ts.Close()

	client := NewClient(ts.URL, "test-token")
	result, err := client.FetchSecrets(context.Background(), "app-1", "production")
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
		resp := map[string]interface{}{"data": []Environment{}, "message": "ok", "status": 200}
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
	if _, err := client.ListEnvironments(context.Background(), "app-1"); err != nil {
		t.Fatalf("ListEnvironments returned error: %v", err)
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
	if _, err := client.ListEnvironments(context.Background(), "app-1"); err != nil {
		t.Fatalf("ListEnvironments returned error: %v", err)
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
	_, err := client.ListEnvironments(context.Background(), "app-1")
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
	if _, err := client.ListEnvironments(context.Background(), "app-1"); err != nil {
		t.Fatalf("ListEnvironments returned error: %v", err)
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
	_, err := client.ListEnvironments(context.Background(), "app-1")
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
	_, err := client.ListEnvironments(context.Background(), "app-1")
	if err == nil {
		t.Fatal("expected error for closed server, got nil")
	}
}

func TestErrorHandling_CancelledContext(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{"data": []Environment{}, "message": "ok", "status": 200})
	}))
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	client := NewClient(ts.URL, "test-token")
	_, err := client.ListEnvironments(ctx, "app-1")
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}
