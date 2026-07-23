package cmd

import (
	"strings"
	"testing"

	"github.com/senseylabs/kagi-cli/internal/client"
)

func issuers(items ...client.ClusterIssuer) []client.ClusterIssuer {
	return items
}

// TestMatchClusterIssuer covers the client-side issuer resolution used by
// `cluster rm`, `workload bind`, and `apply`: match by exact id, by issuer URL
// (case-insensitively), by unambiguous id prefix, plus the ambiguous and
// not-found errors.
func TestMatchClusterIssuer(t *testing.T) {
	list := issuers(
		client.ClusterIssuer{ID: "id-aaa111", IssuerURL: "https://oidc.example/prod"},
		client.ClusterIssuer{ID: "id-bbb222", IssuerURL: "https://oidc.example/staging"},
	)

	tests := []struct {
		name    string
		ref     string
		wantID  string
		wantErr string
	}{
		{name: "exact id", ref: "id-aaa111", wantID: "id-aaa111"},
		{name: "exact url", ref: "https://oidc.example/staging", wantID: "id-bbb222"},
		{name: "url case-insensitive", ref: "HTTPS://OIDC.EXAMPLE/PROD", wantID: "id-aaa111"},
		{name: "unambiguous id prefix", ref: "id-aaa", wantID: "id-aaa111"},
		{name: "ambiguous prefix", ref: "id-", wantErr: "ambiguous"},
		{name: "not found", ref: "nope", wantErr: "not found"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := matchClusterIssuer(list, tt.ref)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.ID != tt.wantID {
				t.Fatalf("id = %q, want %q", got.ID, tt.wantID)
			}
		})
	}
}

func TestParseApplyFile_Valid(t *testing.T) {
	data := []byte(`
issuer:
  url: https://oidc.example/prod
  name: prod-cluster
  staticJwks: auto
bindings:
  - namespace: app
    serviceAccount: api
    scopes:
      - app: /village/kaizen
        env: prod
      - app: /village/sage
        env: prod
  - namespace: data
    serviceAccount: worker
    scopes:
      - app: /village/lancer
        env: staging
`)

	spec, err := parseApplyFile(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.Issuer.URL != "https://oidc.example/prod" {
		t.Fatalf("issuer url = %q", spec.Issuer.URL)
	}
	if spec.Issuer.Name != "prod-cluster" {
		t.Fatalf("issuer name = %q", spec.Issuer.Name)
	}
	if spec.Issuer.StaticJwks != staticJwksAuto {
		t.Fatalf("staticJwks = %q, want %q", spec.Issuer.StaticJwks, staticJwksAuto)
	}
	if len(spec.Bindings) != 2 {
		t.Fatalf("bindings = %d, want 2", len(spec.Bindings))
	}
	if len(spec.Bindings[0].Scopes) != 2 {
		t.Fatalf("binding 0 scopes = %d, want 2", len(spec.Bindings[0].Scopes))
	}
	if spec.Bindings[1].Scopes[0].App != "/village/lancer" || spec.Bindings[1].Scopes[0].Env != "staging" {
		t.Fatalf("binding 1 scope 0 = %+v", spec.Bindings[1].Scopes[0])
	}
}

func TestParseApplyFile_OmittedIssuerFieldsAllowed(t *testing.T) {
	// url and staticJwks may be omitted (auto-detect / public cluster). The parse
	// must accept it — resolution happens later.
	data := []byte(`
issuer:
  name: prod
bindings:
  - namespace: app
    serviceAccount: api
    scopes:
      - app: /village/kaizen
        env: prod
`)

	spec, err := parseApplyFile(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.Issuer.URL != "" || spec.Issuer.StaticJwks != "" {
		t.Fatalf("expected empty url/staticJwks, got %+v", spec.Issuer)
	}
}

func TestParseApplyFile_Invalid(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "missing namespace",
			yaml: `
bindings:
  - serviceAccount: api
    scopes:
      - app: /village/kaizen
        env: prod
`,
			wantErr: "namespace is required",
		},
		{
			name: "missing service account",
			yaml: `
bindings:
  - namespace: app
    scopes:
      - app: /village/kaizen
        env: prod
`,
			wantErr: "serviceAccount is required",
		},
		{
			name: "no scopes",
			yaml: `
bindings:
  - namespace: app
    serviceAccount: api
`,
			wantErr: "at least one scope is required",
		},
		{
			name: "scope missing env",
			yaml: `
bindings:
  - namespace: app
    serviceAccount: api
    scopes:
      - app: /village/kaizen
`,
			wantErr: "both app and env are required",
		},
		{
			name:    "malformed yaml",
			yaml:    "issuer: [unterminated",
			wantErr: "failed to parse apply file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseApplyFile([]byte(tt.yaml))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}
