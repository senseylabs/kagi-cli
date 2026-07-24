package kube

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// withRunner swaps the package Runner for the duration of a test and restores it
// afterwards, so detection tests stay network-free and never touch a real
// kubectl.
func withRunner(t *testing.T, fake func(args ...string) ([]byte, error)) {
	t.Helper()
	orig := Runner
	Runner = fake
	t.Cleanup(func() { Runner = orig })
}

func TestDetectIssuerURL_Success(t *testing.T) {
	var gotArgs []string
	withRunner(t, func(args ...string) ([]byte, error) {
		gotArgs = args
		return []byte(`{"issuer":"https://oidc.example/cluster","jwks_uri":"x"}`), nil
	})

	url, err := DetectIssuerURL("prod")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if url != "https://oidc.example/cluster" {
		t.Fatalf("issuer = %q, want https://oidc.example/cluster", url)
	}

	// The context flag must be threaded through, and the raw path must be the
	// OpenID discovery document.
	want := []string{"--context", "prod", "get", "--raw", "/.well-known/openid-configuration"}
	if strings.Join(gotArgs, " ") != strings.Join(want, " ") {
		t.Fatalf("args = %v, want %v", gotArgs, want)
	}
}

func TestDetectIssuerURL_NoContextOmitsFlag(t *testing.T) {
	var gotArgs []string
	withRunner(t, func(args ...string) ([]byte, error) {
		gotArgs = args
		return []byte(`{"issuer":"https://oidc.example/cluster"}`), nil
	})

	if _, err := DetectIssuerURL(""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(strings.Join(gotArgs, " "), "--context") {
		t.Fatalf("did not expect a --context flag with an empty context: %v", gotArgs)
	}
	want := []string{"get", "--raw", "/.well-known/openid-configuration"}
	if strings.Join(gotArgs, " ") != strings.Join(want, " ") {
		t.Fatalf("args = %v, want %v", gotArgs, want)
	}
}

func TestDetectIssuerURL_MissingIssuerField(t *testing.T) {
	withRunner(t, func(args ...string) ([]byte, error) {
		return []byte(`{"jwks_uri":"x"}`), nil
	})

	_, err := DetectIssuerURL("")
	if err == nil {
		t.Fatal("expected an error for a config with no issuer field")
	}
	if !strings.Contains(err.Error(), "--issuer-url") {
		t.Fatalf("error should point the user at --issuer-url: %v", err)
	}
}

func TestDetectIssuerURL_InvalidJSON(t *testing.T) {
	withRunner(t, func(args ...string) ([]byte, error) {
		return []byte("not json"), nil
	})

	_, err := DetectIssuerURL("")
	if err == nil {
		t.Fatal("expected an error for non-JSON output")
	}
	if !strings.Contains(err.Error(), "--issuer-url") {
		t.Fatalf("error should point the user at --issuer-url: %v", err)
	}
}

func TestDetectIssuerURL_KubectlNotFound(t *testing.T) {
	withRunner(t, func(args ...string) ([]byte, error) {
		return nil, exec.ErrNotFound
	})

	_, err := DetectIssuerURL("")
	if err == nil {
		t.Fatal("expected an error when kubectl is absent")
	}
	if !strings.Contains(err.Error(), "kubectl was not found") {
		t.Fatalf("expected a kubectl-not-found message, got: %v", err)
	}
}

func TestDetectIssuerURL_RunnerError(t *testing.T) {
	withRunner(t, func(args ...string) ([]byte, error) {
		return nil, errors.New("connection refused")
	})

	_, err := DetectIssuerURL("")
	if err == nil {
		t.Fatal("expected an error when the runner fails")
	}
	if !strings.Contains(err.Error(), "--issuer-url") {
		t.Fatalf("error should point the user at --issuer-url: %v", err)
	}
}

func TestDetectJWKS_Success(t *testing.T) {
	var gotArgs []string
	raw := `{"keys":[{"kid":"abc","kty":"RSA"}]}`
	withRunner(t, func(args ...string) ([]byte, error) {
		gotArgs = args
		return []byte(raw), nil
	})

	jwks, err := DetectJWKS("staging")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if jwks != raw {
		t.Fatalf("jwks = %q, want the raw document %q", jwks, raw)
	}
	want := []string{"--context", "staging", "get", "--raw", "/openid/v1/jwks"}
	if strings.Join(gotArgs, " ") != strings.Join(want, " ") {
		t.Fatalf("args = %v, want %v", gotArgs, want)
	}
}

// TestDetectClusterType covers one row per heuristic entry in the spec's host-
// substring table, plus the unrecognized-host GENERIC fallback and a couple of
// edge inputs (bare host, empty). Detection is on the URL host only.
func TestDetectClusterType(t *testing.T) {
	tests := []struct {
		name      string
		issuerURL string
		want      ClusterType
	}{
		{name: "AKS azmk8s", issuerURL: "https://mycluster-dns-abc123.hcp.westeurope.azmk8s.io", want: ClusterTypeAKS},
		{name: "AKS oidc prod-aks", issuerURL: "https://oic.prod-aks.azure.com/tenant/guid/", want: ClusterTypeAKS},
		{name: "AKS region-prefixed prod-aks", issuerURL: "https://westeurope.oic.prod-aks.azure.com/x/y/", want: ClusterTypeAKS},
		{name: "EKS regional", issuerURL: "https://oidc.eks.eu-west-1.amazonaws.com/id/ABCDEF0123456789", want: ClusterTypeEKS},
		{name: "GKE container", issuerURL: "https://container.googleapis.com/v1/projects/p/locations/l/clusters/c", want: ClusterTypeGKE},
		{name: "GCS-only bucket issuer is GENERIC", issuerURL: "https://storage.googleapis.com/my-oidc-bucket", want: ClusterTypeGeneric},
		{name: "GKE gke host token", issuerURL: "https://oidc.prod.gke.example.com/cluster", want: ClusterTypeGKE},
		{name: "OpenShift host", issuerURL: "https://oauth-openshift.apps.rosa.example.com", want: ClusterTypeOpenShift},
		{name: "K3s host", issuerURL: "https://k3s.example.internal:6443", want: ClusterTypeK3s},
		{name: "Rancher host", issuerURL: "https://rancher.example.com/k8s/clusters/c-abc", want: ClusterTypeK3s},
		{name: "unknown host falls back to GENERIC", issuerURL: "https://oidc.example.com/cluster", want: ClusterTypeGeneric},
		{name: "in-cluster default svc is GENERIC", issuerURL: "https://kubernetes.default.svc", want: ClusterTypeGeneric},
		{name: "bare host without scheme still matches", issuerURL: "oidc.eks.us-east-1.amazonaws.com", want: ClusterTypeEKS},
		{name: "empty is GENERIC", issuerURL: "", want: ClusterTypeGeneric},
		{name: "case-insensitive host", issuerURL: "https://OIDC.EKS.EU-WEST-1.AMAZONAWS.COM/id/x", want: ClusterTypeEKS},
		{name: "EKS token without amazonaws suffix is GENERIC", issuerURL: "https://oidc.eks.internal.example.com/id/x", want: ClusterTypeGeneric},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectClusterType(tt.issuerURL); got != tt.want {
				t.Fatalf("DetectClusterType(%q) = %q, want %q", tt.issuerURL, got, tt.want)
			}
		})
	}
}

func TestDetectJWKS_InvalidJSON(t *testing.T) {
	withRunner(t, func(args ...string) ([]byte, error) {
		return []byte("Error from server (Forbidden)"), nil
	})

	_, err := DetectJWKS("")
	if err == nil {
		t.Fatal("expected an error for a non-JSON JWKS body")
	}
	if !strings.Contains(err.Error(), "--static-jwks-file") {
		t.Fatalf("error should point the user at --static-jwks-file: %v", err)
	}
}
