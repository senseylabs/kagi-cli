// Package kube auto-detects a Kubernetes cluster's OIDC issuer URL and JWKS by
// shelling out to kubectl. It carries no client-go dependency: everything is
// read through `kubectl get --raw`, which reuses the user's existing kubeconfig,
// contexts, and auth. The kubectl invocation is injectable (see Runner) so tests
// stay network-free and independent of a real cluster.
package kube

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
)

// ClusterType is the platform a cluster issuer runs on. It mirrors the backend's
// ClusterIssuerType enum and is descriptive metadata only — it never gates or
// alters which JWTs are trusted.
type ClusterType string

const (
	ClusterTypeAKS       ClusterType = "AKS"
	ClusterTypeEKS       ClusterType = "EKS"
	ClusterTypeGKE       ClusterType = "GKE"
	ClusterTypeOpenShift ClusterType = "OPENSHIFT"
	ClusterTypeK3s       ClusterType = "K3S"
	ClusterTypeGeneric   ClusterType = "GENERIC"
)

// DetectClusterType infers the platform from the issuer URL's host. Detection is
// host-substring based and best-effort: an unrecognized host (or an unparseable
// URL) returns ClusterTypeGeneric, never an error, so it never blocks
// registration. Matching is case-insensitive and checked in the order below,
// first match wins. Only the issuer URL host drives detection — kubeconfig
// context names are free-form aliases and carry no reliable platform signal.
func DetectClusterType(issuerURL string) ClusterType {
	host := issuerURLHost(issuerURL)
	switch {
	case strings.Contains(host, ".azmk8s.io"), strings.Contains(host, "prod-aks.azure.com"):
		return ClusterTypeAKS
	case strings.Contains(host, ".eks.") && strings.HasSuffix(host, ".amazonaws.com"):
		return ClusterTypeEKS
	case strings.Contains(host, "container.googleapis.com"),
		strings.Contains(host, "storage.googleapis.com"),
		strings.Contains(host, ".gke."):
		return ClusterTypeGKE
	case strings.Contains(host, "openshift"):
		return ClusterTypeOpenShift
	case strings.Contains(host, "k3s"), strings.Contains(host, "rancher"):
		return ClusterTypeK3s
	default:
		return ClusterTypeGeneric
	}
}

// issuerURLHost extracts the lower-cased host from an issuer URL. If the URL does
// not parse into a host (e.g. a bare host string with no scheme), it falls back
// to matching against the whole lower-cased input so detection still works on
// loosely-formed issuer values.
func issuerURLHost(issuerURL string) string {
	trimmed := strings.TrimSpace(issuerURL)
	if parsed, err := url.Parse(trimmed); err == nil && parsed.Host != "" {
		return strings.ToLower(parsed.Host)
	}
	return strings.ToLower(trimmed)
}

// Runner executes kubectl with the given arguments and returns its stdout. It is
// a package var so tests can substitute a fake — the real implementation shells
// out to the kubectl binary on PATH. A nil/absent binary surfaces as an
// exec.ErrNotFound, which callers translate into an actionable message.
var Runner = func(args ...string) ([]byte, error) {
	return exec.Command("kubectl", args...).Output()
}

// openIDConfiguration is the subset of the cluster's
// /.well-known/openid-configuration document we need: the issuer identifier.
type openIDConfiguration struct {
	Issuer string `json:"issuer"`
}

// DetectIssuerURL reads the cluster's OIDC issuer URL from its
// /.well-known/openid-configuration document via kubectl. contextName selects a
// kubeconfig context; pass "" to use the current context. On any failure it
// returns an actionable error telling the user to pass --issuer-url instead.
func DetectIssuerURL(contextName string) (string, error) {
	out, err := runRaw(contextName, "/.well-known/openid-configuration")
	if err != nil {
		return "", err
	}

	var cfg openIDConfiguration
	if err := json.Unmarshal(out, &cfg); err != nil {
		return "", fmt.Errorf("could not parse the cluster's OpenID configuration: %w. Pass --issuer-url to set the issuer explicitly", err)
	}
	if cfg.Issuer == "" {
		return "", fmt.Errorf("the cluster's OpenID configuration has no issuer field. Pass --issuer-url to set the issuer explicitly")
	}
	return cfg.Issuer, nil
}

// DetectJWKS reads the cluster's raw JWKS document from /openid/v1/jwks via
// kubectl and returns it verbatim (the backend stores the JSON as-is). Use this
// for private clusters whose JWKS endpoint Kagi cannot reach directly. On any
// failure it returns an actionable error telling the user to pass a static JWKS
// file instead.
func DetectJWKS(contextName string) (string, error) {
	out, err := runRaw(contextName, "/openid/v1/jwks")
	if err != nil {
		return "", err
	}

	// Validate it is well-formed JSON so we never store a kubectl error banner or
	// a truncated body as if it were a JWKS. The raw bytes are returned unchanged.
	if !json.Valid(out) {
		return "", fmt.Errorf("the cluster's JWKS endpoint did not return valid JSON. Pass --static-jwks-file to set the JWKS explicitly")
	}
	return string(out), nil
}

// runRaw invokes `kubectl [--context ctx] get --raw <rawPath>` through Runner and
// maps failures to actionable errors. A missing kubectl binary is called out
// specifically so the user knows to install it or pass the value explicitly.
func runRaw(contextName, rawPath string) ([]byte, error) {
	args := make([]string, 0, 5)
	if contextName != "" {
		args = append(args, "--context", contextName)
	}
	args = append(args, "get", "--raw", rawPath)

	out, err := Runner(args...)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("kubectl was not found on your PATH. Install kubectl and configure cluster access, or pass --issuer-url (and --static-jwks-file) explicitly")
		}
		// exec.ExitError carries kubectl's stderr, which explains the real cause
		// (no cluster access, RBAC denied, path not served). Surface it verbatim.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return nil, fmt.Errorf("kubectl get --raw %s failed: %s. Pass --issuer-url (and --static-jwks-file) explicitly to skip auto-detection", rawPath, string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("kubectl get --raw %s failed: %w. Pass --issuer-url (and --static-jwks-file) explicitly to skip auto-detection", rawPath, err)
	}
	return out, nil
}
