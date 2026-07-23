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
