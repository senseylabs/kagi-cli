package cmd

import (
	"strings"
	"testing"

	"github.com/senseylabs/kagi-cli/internal/client"
	"github.com/senseylabs/kagi-cli/internal/kube"
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

// TestParseClusterType covers case-insensitive acceptance of the six known
// platforms and rejection of anything else with an actionable message.
func TestParseClusterType(t *testing.T) {
	valid := map[string]kube.ClusterType{
		"AKS":       kube.ClusterTypeAKS,
		"eks":       kube.ClusterTypeEKS,
		"Gke":       kube.ClusterTypeGKE,
		"openshift": kube.ClusterTypeOpenShift,
		"  k3s  ":   kube.ClusterTypeK3s,
		"GENERIC":   kube.ClusterTypeGeneric,
	}
	for raw, want := range valid {
		got, err := parseClusterType(raw)
		if err != nil {
			t.Fatalf("parseClusterType(%q) unexpected error: %v", raw, err)
		}
		if got != want {
			t.Fatalf("parseClusterType(%q) = %q, want %q", raw, got, want)
		}
	}

	for _, raw := range []string{"DOCKER_DESKTOP", "kind", "", "aksx"} {
		_, err := parseClusterType(raw)
		if err == nil {
			t.Fatalf("parseClusterType(%q) expected an error", raw)
		}
		if !strings.Contains(err.Error(), "Valid values") {
			t.Fatalf("parseClusterType(%q) error should list valid values: %v", raw, err)
		}
	}
}

// TestDecideIssuerAction covers the reconcile verdict including the new type
// field: a type-only difference must force an update, and a full match (type
// included) must report unchanged.
func TestDecideIssuerAction(t *testing.T) {
	existing := &client.ClusterIssuer{
		DisplayName: "prod",
		StaticJwks:  "",
		Enabled:     true,
		Type:        "EKS",
	}

	tests := []struct {
		name     string
		existing *client.ClusterIssuer
		dName    string
		dJwks    string
		dEnabled bool
		dType    string
		want     issuerAction
	}{
		{name: "nil existing -> create", existing: nil, dName: "prod", dEnabled: true, dType: "EKS", want: issuerActionCreate},
		{name: "all match -> unchanged", existing: existing, dName: "prod", dJwks: "", dEnabled: true, dType: "EKS", want: issuerActionUnchanged},
		{name: "type differs -> update", existing: existing, dName: "prod", dJwks: "", dEnabled: true, dType: "AKS", want: issuerActionUpdate},
		{name: "name differs -> update", existing: existing, dName: "prod-2", dJwks: "", dEnabled: true, dType: "EKS", want: issuerActionUpdate},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decideIssuerAction(tt.existing, tt.dName, tt.dJwks, tt.dEnabled, tt.dType)
			if got != tt.want {
				t.Fatalf("decideIssuerAction = %q, want %q", got, tt.want)
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

func TestParseApplyFile_TypeKey(t *testing.T) {
	data := []byte(`
issuer:
  url: https://oidc.eks.eu-west-1.amazonaws.com/id/x
  name: prod
  type: EKS
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
	if spec.Issuer.Type != "EKS" {
		t.Fatalf("issuer type = %q, want EKS", spec.Issuer.Type)
	}
}

func TestParseApplyFile_TypeKeyOmittedIsEmpty(t *testing.T) {
	data := []byte(`
issuer:
  url: https://oidc.example/prod
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
	if spec.Issuer.Type != "" {
		t.Fatalf("issuer type = %q, want empty", spec.Issuer.Type)
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
