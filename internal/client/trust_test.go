package client

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// These are contract tests: they assert the exact HTTP method and URL path each
// trust client method sends, and — for writes — the JSON body field names. They
// are the regression guard for the id-in-body class of bug, where a by-id
// operation was sent to the collection path with the id in the body (a 405 on
// the real backend). Every by-id method must put the id in the URL, not the body.

// capturedRequest records what the client sent to the test server.
type capturedRequest struct {
	method string
	path   string
	body   map[string]any
}

// newTrustTestServer returns an httptest server that records the incoming
// request into captured and replies with a well-formed APIResponse envelope
// wrapping responseData, so value-returning client methods parse successfully.
func newTrustTestServer(t *testing.T, captured *capturedRequest, responseData any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.method = r.Method
		captured.path = r.URL.Path

		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("failed to read request body: %v", err)
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &captured.body); err != nil {
				t.Errorf("request body is not valid JSON: %v", err)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":    responseData,
			"message": "ok",
			"status":  200,
		})
	}))
}

// newTrustTestClient builds a client pointed at the test server. It uses PAT auth
// (isPAT) so requests are network-free and self-contained: the org header is
// bound to the token server-side, so no X-Organization-ID setup is needed.
func newTrustTestClient(ts *httptest.Server) *KagiClient {
	return &KagiClient{
		baseURL:    ts.URL,
		token:      "test-token",
		httpClient: ts.Client(),
		isPAT:      true,
	}
}

func assertRequest(t *testing.T, got capturedRequest, wantMethod, wantPath string) {
	t.Helper()
	if got.method != wantMethod {
		t.Errorf("method: got %s, want %s", got.method, wantMethod)
	}
	if got.path != wantPath {
		t.Errorf("path: got %q, want %q", got.path, wantPath)
	}
}

func bodyKeys(body map[string]any) []string {
	keys := make([]string, 0, len(body))
	for k := range body {
		keys = append(keys, k)
	}
	return keys
}

// assertBodyKeys asserts the decoded body has exactly the wanted field names —
// no missing keys and, crucially, no extra ones (e.g. a stray id-in-body field).
func assertBodyKeys(t *testing.T, body map[string]any, want ...string) {
	t.Helper()
	if len(body) != len(want) {
		t.Errorf("body field names: got %v, want exactly %v", bodyKeys(body), want)
	}
	for _, k := range want {
		if _, ok := body[k]; !ok {
			t.Errorf("body missing field %q; got %v", k, bodyKeys(body))
		}
	}
}

func assertNoBodyField(t *testing.T, body map[string]any, field string) {
	t.Helper()
	if _, ok := body[field]; ok {
		t.Errorf("body must not contain field %q (id belongs in the URL path), got %v", field, bodyKeys(body))
	}
}

const (
	clusterIssuersBase   = "/kagi/organizations/trust/cluster-issuers"
	workloadBindingsBase = "/kagi/organizations/trust/workload-bindings"
	testID               = "id-123"
)

func TestListClusterIssuers_Request(t *testing.T) {
	var got capturedRequest
	ts := newTrustTestServer(t, &got, []map[string]any{{"id": testID}})
	defer ts.Close()

	if _, err := newTrustTestClient(ts).ListClusterIssuers(); err != nil {
		t.Fatalf("ListClusterIssuers: %v", err)
	}
	assertRequest(t, got, http.MethodGet, clusterIssuersBase)
}

func TestCreateClusterIssuer_Request(t *testing.T) {
	var got capturedRequest
	ts := newTrustTestServer(t, &got, map[string]any{"id": testID})
	defer ts.Close()

	_, err := newTrustTestClient(ts).CreateClusterIssuer("https://oidc.example/prod", "prod", `{"keys":[]}`)
	if err != nil {
		t.Fatalf("CreateClusterIssuer: %v", err)
	}
	assertRequest(t, got, http.MethodPost, clusterIssuersBase)
	assertBodyKeys(t, got.body, "issuerUrl", "displayName", "staticJwks")
}

func TestUpdateClusterIssuer_Request(t *testing.T) {
	var got capturedRequest
	ts := newTrustTestServer(t, &got, map[string]any{"id": testID})
	defer ts.Close()

	_, err := newTrustTestClient(ts).UpdateClusterIssuer(testID, "prod", `{"keys":[]}`, true)
	if err != nil {
		t.Fatalf("UpdateClusterIssuer: %v", err)
	}
	assertRequest(t, got, http.MethodPut, clusterIssuersBase+"/"+testID)
	assertBodyKeys(t, got.body, "displayName", "staticJwks", "enabled")
	assertNoBodyField(t, got.body, "id")
	assertNoBodyField(t, got.body, "clusterIssuerId")
}

func TestDeleteClusterIssuer_Request(t *testing.T) {
	var got capturedRequest
	ts := newTrustTestServer(t, &got, nil)
	defer ts.Close()

	if err := newTrustTestClient(ts).DeleteClusterIssuer(testID); err != nil {
		t.Fatalf("DeleteClusterIssuer: %v", err)
	}
	assertRequest(t, got, http.MethodDelete, clusterIssuersBase+"/"+testID)
	if len(got.body) != 0 {
		t.Errorf("DELETE must send no body, got %v", bodyKeys(got.body))
	}
}

func TestListWorkloadBindings_Request(t *testing.T) {
	var got capturedRequest
	ts := newTrustTestServer(t, &got, []map[string]any{{"id": testID}})
	defer ts.Close()

	if _, err := newTrustTestClient(ts).ListWorkloadBindings(); err != nil {
		t.Fatalf("ListWorkloadBindings: %v", err)
	}
	assertRequest(t, got, http.MethodGet, workloadBindingsBase)
}

func TestCreateWorkloadBinding_Request(t *testing.T) {
	var got capturedRequest
	ts := newTrustTestServer(t, &got, map[string]any{"id": testID})
	defer ts.Close()

	scopes := []BindingScope{{AppID: "app-1", EnvironmentSlug: "prod"}}
	_, err := newTrustTestClient(ts).CreateWorkloadBinding("issuer-1", "production", "kagi-operator", scopes)
	if err != nil {
		t.Fatalf("CreateWorkloadBinding: %v", err)
	}
	assertRequest(t, got, http.MethodPost, workloadBindingsBase)
	assertBodyKeys(t, got.body, "clusterIssuerId", "namespace", "serviceAccount", "scopes")
}

func TestUpdateWorkloadBinding_Request(t *testing.T) {
	var got capturedRequest
	ts := newTrustTestServer(t, &got, map[string]any{"id": testID})
	defer ts.Close()

	scopes := []BindingScope{{AppID: "app-1", EnvironmentSlug: "prod"}}
	_, err := newTrustTestClient(ts).UpdateWorkloadBinding(testID, "production", "kagi-operator", true, scopes)
	if err != nil {
		t.Fatalf("UpdateWorkloadBinding: %v", err)
	}
	assertRequest(t, got, http.MethodPut, workloadBindingsBase+"/"+testID)
	assertBodyKeys(t, got.body, "namespace", "serviceAccount", "enabled", "scopes")
	// The id is a path variable on the backend: it must never be in the body.
	assertNoBodyField(t, got.body, "workloadBindingId")
	assertNoBodyField(t, got.body, "id")
}

func TestDeleteWorkloadBinding_Request(t *testing.T) {
	var got capturedRequest
	ts := newTrustTestServer(t, &got, nil)
	defer ts.Close()

	if err := newTrustTestClient(ts).DeleteWorkloadBinding(testID); err != nil {
		t.Fatalf("DeleteWorkloadBinding: %v", err)
	}
	assertRequest(t, got, http.MethodDelete, workloadBindingsBase+"/"+testID)
	if len(got.body) != 0 {
		t.Errorf("DELETE must send no body, got %v", bodyKeys(got.body))
	}
}
