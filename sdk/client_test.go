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
		{ID: "p1", Name: "Project One", Description: "First project"},
		{ID: "p2", Name: "Project Two", Description: "Second project"},
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
