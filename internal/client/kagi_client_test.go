package client

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	kagi "github.com/senseylabs/kagi-sdk"
	"github.com/zalando/go-keyring"

	"github.com/senseylabs/kagi-cli/internal/auth"
)

// newTestKagiClient builds a KagiClient wired to a test server, with an active
// org set so write/paged requests pass the requireOrgForJWT guard.
func newTestKagiClient(baseURL string) *KagiClient {
	return &KagiClient{
		baseURL:    baseURL,
		token:      "test-token",
		orgID:      "org-1",
		httpClient: http.DefaultClient,
		sdkClient:  kagi.NewOrgClient(baseURL, "test-token", "org-1", false),
	}
}

// pagedItems slices allData by the ?page query for a maxPageSize-batched
// endpoint, asserting the size param and recording served page numbers.
func writePage(t *testing.T, w http.ResponseWriter, r *http.Request, allData []map[string]string, servedPages *[]int) {
	t.Helper()
	if got := r.URL.Query().Get("size"); got != "200" {
		t.Errorf("unexpected size param: got %q, want 200", got)
	}
	page := 0
	fmt.Sscanf(r.URL.Query().Get("page"), "%d", &page)
	if servedPages != nil {
		*servedPages = append(*servedPages, page)
	}
	start := page * maxPageSize
	end := start + maxPageSize
	if start > len(allData) {
		start = len(allData)
	}
	if end > len(allData) {
		end = len(allData)
	}
	resp := map[string]interface{}{"data": allData[start:end], "message": "ok", "status": 200}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func TestListSecrets_PaginatesUntilShortPage(t *testing.T) {
	total := maxPageSize + 7
	all := make([]map[string]string, total)
	for i := range all {
		all[i] = map[string]string{"id": fmt.Sprintf("s%d", i), "keyName": fmt.Sprintf("KEY_%d", i)}
	}
	var served []int
	wantPath := "/kagi/apps/app-1/environments/production/secrets"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != wantPath {
			t.Errorf("unexpected path: got %s, want %s", r.URL.Path, wantPath)
			http.NotFound(w, r)
			return
		}
		writePage(t, w, r, all, &served)
	}))
	defer ts.Close()

	c := newTestKagiClient(ts.URL)
	result, err := c.ListSecrets("app-1", "production")
	if err != nil {
		t.Fatalf("ListSecrets returned error: %v", err)
	}
	if len(result) != total {
		t.Fatalf("expected %d secrets, got %d", total, len(result))
	}
	if len(served) != 2 || served[0] != 0 || served[1] != 1 {
		t.Errorf("expected pages [0 1] served, got %v", served)
	}
}

// TestListAccessTokens_WalksFolderTree verifies the traversal visits the root
// (null-folder bucket) plus every subfolder, pages each folder's items, tags
// each token with its folder path, and returns tokens invisible to a root-only
// listing.
func TestListAccessTokens_WalksFolderTree(t *testing.T) {
	// Folder tree: root -> {ci, prod}; ci -> {nested}. Items keyed by folder path.
	items := map[string][]map[string]string{
		"/kagi/folders/access-tokens/items":           {{"id": "t-root", "name": "root-tok"}},
		"/kagi/folders/access-tokens/items/ci":        {{"id": "t-ci", "name": "ci-tok"}},
		"/kagi/folders/access-tokens/items/ci/nested": {{"id": "t-nested", "name": "nested-tok"}},
		"/kagi/folders/access-tokens/items/prod":      {{"id": "t-prod", "name": "prod-tok"}},
	}
	children := map[string]kagi.FolderChildren{
		"/kagi/folders/access-tokens/children": {
			Folders: []kagi.Folder{{ID: "f-ci", Slug: "ci"}, {ID: "f-prod", Slug: "prod"}},
		},
		"/kagi/folders/access-tokens/children/ci": {
			Folders: []kagi.Folder{{ID: "f-nested", Slug: "nested"}},
		},
		"/kagi/folders/access-tokens/children/ci/nested": {},
		"/kagi/folders/access-tokens/children/prod":      {},
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if data, ok := items[r.URL.Path]; ok {
			writePage(t, w, r, data, nil)
			return
		}
		if ch, ok := children[r.URL.Path]; ok {
			resp := map[string]interface{}{"data": ch, "message": "ok", "status": 200}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}
		t.Errorf("unexpected path: %s", r.URL.Path)
		http.NotFound(w, r)
	}))
	defer ts.Close()

	c := newTestKagiClient(ts.URL)
	tokens, err := c.ListAccessTokens()
	if err != nil {
		t.Fatalf("ListAccessTokens returned error: %v", err)
	}
	if len(tokens) != 4 {
		t.Fatalf("expected 4 tokens across the tree, got %d: %+v", len(tokens), tokens)
	}

	gotPath := map[string]string{}
	for _, tok := range tokens {
		gotPath[tok.ID] = tok.FolderPath
	}
	want := map[string]string{
		"t-root":   "/",
		"t-ci":     "/ci",
		"t-nested": "/ci/nested",
		"t-prod":   "/prod",
	}
	for id, wantFP := range want {
		if gotPath[id] != wantFP {
			t.Errorf("token %s: got folder path %q, want %q", id, gotPath[id], wantFP)
		}
	}

	ids := make([]string, 0, len(gotPath))
	for id := range gotPath {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if fmt.Sprint(ids) != "[t-ci t-nested t-prod t-root]" {
		t.Errorf("unexpected token id set: %v", ids)
	}
}

func TestAccessTokenItemsPath(t *testing.T) {
	cases := []struct {
		folderPath string
		page       int
		want       string
	}{
		{"", 0, "/kagi/folders/access-tokens/items?page=0&size=200&sort=name"},
		{"/", 1, "/kagi/folders/access-tokens/items?page=1&size=200&sort=name"},
		{"/ci/nested", 2, "/kagi/folders/access-tokens/items/ci/nested?page=2&size=200&sort=name"},
	}
	for _, tc := range cases {
		if got := accessTokenItemsPath(tc.folderPath, tc.page); got != tc.want {
			t.Errorf("accessTokenItemsPath(%q, %d) = %q, want %q", tc.folderPath, tc.page, got, tc.want)
		}
	}
}

// --- KAGI_TOKEN (PAT) vs stored-session auth ---------------------------------

// authProbeServer records the Authorization and X-Organization-ID headers of the
// first request it receives and answers with an empty data envelope, so both the
// SDK read path and the CLI write path can be probed without real credentials.
func authProbeServer(t *testing.T, gotAuth *string, gotOrg *string, orgPresent *bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*gotAuth = r.Header.Get("Authorization")
		values, ok := r.Header[http.CanonicalHeaderKey(kagi.HeaderOrganizationID)]
		*orgPresent = ok
		if ok {
			*gotOrg = values[0]
		}
		resp := map[string]interface{}{"data": []map[string]string{}, "message": "ok", "status": 200}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

// seedSession writes a hermetic home with a stored, unexpired login session and
// a kagi.yaml pinning an active organization, mirroring `kagi login` followed by
// `kagi org use`. It returns the org UUID it pinned.
func seedSession(t *testing.T) string {
	t.Helper()
	keyring.MockInit()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	const orgID = "11111111-2222-3333-4444-555555555555"
	dir := t.TempDir()
	cfg := "organization: sensey\norganization-id: " + orgID + "\n"
	if err := os.WriteFile(filepath.Join(dir, "kagi.yaml"), []byte(cfg), 0600); err != nil {
		t.Fatalf("write kagi.yaml: %v", err)
	}
	t.Chdir(dir)

	store := auth.NewTokenStore()
	if err := store.Save(auth.Credentials{
		AccessToken:  "stored-session-jwt",
		RefreshToken: "stored-refresh",
		ExpiresAt:    time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("seed credentials: %v", err)
	}
	return orgID
}

// With KAGI_TOKEN set, the CLI authenticates with it rather than with the stored
// session, and — because a PAT is org-bound server-side — never sends
// X-Organization-ID, even though an active org is pinned in kagi.yaml. Sending a
// mismatched org header is rejected with 403 by the API's tenancy filter, which
// is exactly how CI would break.
func TestNewKagiClient_StaticTokenSelectsPATAuth(t *testing.T) {
	seedSession(t)
	t.Setenv("KAGI_TOKEN", "vv_ci_token")

	var gotAuth, gotOrg string
	var orgPresent bool
	ts := authProbeServer(t, &gotAuth, &gotOrg, &orgPresent)
	defer ts.Close()

	c, err := NewKagiClient(ts.URL, "")
	if err != nil {
		t.Fatalf("NewKagiClient: %v", err)
	}
	if !c.IsPAT() {
		t.Fatal("expected a PAT client when KAGI_TOKEN is set")
	}
	if c.OrgID() != "" {
		t.Errorf("PAT client should carry no orgID, got %q", c.OrgID())
	}

	// SDK read path.
	if _, err := c.ListEnvironments("app-1"); err != nil {
		t.Fatalf("ListEnvironments: %v", err)
	}
	if gotAuth != "Bearer vv_ci_token" {
		t.Errorf("SDK path Authorization = %q, want %q", gotAuth, "Bearer vv_ci_token")
	}
	if orgPresent {
		t.Errorf("SDK path must omit %s for a PAT, got %q", kagi.HeaderOrganizationID, gotOrg)
	}

	// CLI write/list path (doRequest -> setAuthHeaders + requireOrgForJWT).
	gotAuth, gotOrg, orgPresent = "", "", false
	if _, err := c.ListSecrets("app-1", "prod"); err != nil {
		t.Fatalf("ListSecrets: %v", err)
	}
	if gotAuth != "Bearer vv_ci_token" {
		t.Errorf("CLI path Authorization = %q, want %q", gotAuth, "Bearer vv_ci_token")
	}
	if orgPresent {
		t.Errorf("CLI path must omit %s for a PAT, got %q", kagi.HeaderOrganizationID, gotOrg)
	}
}

// With KAGI_TOKEN unset, nothing about the device-grant session path changes:
// the stored access token is used and the pinned org is sent as
// X-Organization-ID.
func TestNewKagiClient_NoStaticTokenUsesStoredSession(t *testing.T) {
	orgID := seedSession(t)
	t.Setenv("KAGI_TOKEN", "")

	var gotAuth, gotOrg string
	var orgPresent bool
	ts := authProbeServer(t, &gotAuth, &gotOrg, &orgPresent)
	defer ts.Close()

	c, err := NewKagiClient(ts.URL, "")
	if err != nil {
		t.Fatalf("NewKagiClient: %v", err)
	}
	if c.IsPAT() {
		t.Fatal("expected a session client when KAGI_TOKEN is unset")
	}
	if c.OrgID() != orgID {
		t.Errorf("OrgID = %q, want %q", c.OrgID(), orgID)
	}

	if _, err := c.ListSecrets("app-1", "prod"); err != nil {
		t.Fatalf("ListSecrets: %v", err)
	}
	if gotAuth != "Bearer stored-session-jwt" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer stored-session-jwt")
	}
	if !orgPresent {
		t.Fatalf("session auth must send %s", kagi.HeaderOrganizationID)
	}
	if gotOrg != orgID {
		t.Errorf("%s = %q, want %q", kagi.HeaderOrganizationID, gotOrg, orgID)
	}
}

// An empty or whitespace-only KAGI_TOKEN is indistinguishable from unset: an
// unpopulated CI secret must fall through to the stored session rather than
// authenticate with an empty bearer token and fail as an opaque 401.
func TestNewKagiClient_EmptyStaticTokenTreatedAsUnset(t *testing.T) {
	for _, value := range []string{"", "   ", "\n", "\t "} {
		orgID := seedSession(t)
		t.Setenv("KAGI_TOKEN", value)

		c, err := NewKagiClient("http://127.0.0.1:0", "")
		if err != nil {
			t.Fatalf("KAGI_TOKEN=%q: NewKagiClient: %v", value, err)
		}
		if c.IsPAT() {
			t.Errorf("KAGI_TOKEN=%q should not select PAT auth", value)
		}
		if c.token != "stored-session-jwt" {
			t.Errorf("KAGI_TOKEN=%q: token = %q, want the stored session token", value, c.token)
		}
		if c.OrgID() != orgID {
			t.Errorf("KAGI_TOKEN=%q: OrgID = %q, want %q", value, c.OrgID(), orgID)
		}
	}
}

// NewSessionClient ignores KAGI_TOKEN entirely. `kagi login` relies on this to
// judge whether the STORED session is reusable — asking NewKagiClient with a PAT
// in the environment would answer about the PAT and report "Already logged in"
// about a session it never checked.
func TestNewSessionClient_IgnoresStaticToken(t *testing.T) {
	orgID := seedSession(t)
	t.Setenv("KAGI_TOKEN", "vv_ci_token")

	c, err := NewSessionClient("http://127.0.0.1:0", "")
	if err != nil {
		t.Fatalf("NewSessionClient: %v", err)
	}
	if c.IsPAT() {
		t.Fatal("NewSessionClient must never select PAT auth")
	}
	if c.token != "stored-session-jwt" {
		t.Errorf("token = %q, want the stored session token", c.token)
	}
	if c.OrgID() != orgID {
		t.Errorf("OrgID = %q, want %q", c.OrgID(), orgID)
	}
}

// A PAT is exempt from the "no organization selected" fail-fast on the CLI write
// path: its org is bound to the token, so there is nothing to select.
func TestRequireOrgForJWT_PATExempt(t *testing.T) {
	pat := &KagiClient{isPAT: true}
	if err := pat.requireOrgForJWT(); err != nil {
		t.Errorf("PAT client should be exempt from the org requirement, got: %v", err)
	}

	session := &KagiClient{}
	if err := session.requireOrgForJWT(); err == nil {
		t.Error("session client with no org selected should fail fast")
	}
}
