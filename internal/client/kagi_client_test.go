package client

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	kagi "github.com/senseylabs/kagi-sdk"
)

// newTestKagiClient builds a KagiClient wired to a test server, with an active
// org set so write/paged requests pass the requireOrgForJWT guard.
func newTestKagiClient(baseURL string) *KagiClient {
	return &KagiClient{
		baseURL:    baseURL,
		token:      "test-token",
		orgID:      "org-1",
		httpClient: http.DefaultClient,
		sdkClient:  kagi.NewOrgClient(baseURL, "test-token", "org-1"),
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
