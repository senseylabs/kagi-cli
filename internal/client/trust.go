package client

import (
	"encoding/json"
	"fmt"
	"net/url"

	kagi "github.com/senseylabs/kagi-sdk"
)

// ---------------------------------------------------------------------------
// Workload identity trust — cluster issuers and workload bindings
// ---------------------------------------------------------------------------
//
// These back the `kagi cluster` and `kagi workload` commands. A cluster issuer
// registers a Kubernetes cluster's OIDC issuer (and, for private clusters, a
// static JWKS) with Kagi; a workload binding grants a specific
// (namespace, serviceAccount) on that cluster access to a set of app
// environments (scopes). Both live under /kagi/organizations/trust and are
// gated to org ADMIN/OWNER. Writes go through doRequestWithBody, which enforces
// requireOrgForJWT and attaches the Bearer token plus X-Organization-ID.

// ClusterIssuer is a registered Kubernetes cluster OIDC issuer. IssuerURL is the
// idempotency key: the backend has no find-by-url route, so registration lists
// and matches on it.
type ClusterIssuer struct {
	ID          string `json:"id"`
	IssuerURL   string `json:"issuerUrl"`
	DisplayName string `json:"displayName"`
	StaticJwks  string `json:"staticJwks"`
	Enabled     bool   `json:"enabled"`
	// Type is the cluster platform (AKS/EKS/GKE/OPENSHIFT/K3S/GENERIC).
	// Descriptive metadata only — always present on responses from a migrated
	// backend (the column is NOT NULL DEFAULT 'GENERIC').
	Type      string `json:"type"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

// BindingScope grants a workload binding access to one app environment. AppID is
// the stable machine app binding; EnvironmentSlug is the environment within it.
// The scope's own ID is server-assigned and omitted on writes.
type BindingScope struct {
	ID              string `json:"id,omitempty"`
	AppID           string `json:"appId"`
	EnvironmentSlug string `json:"environmentSlug"`
}

// WorkloadBinding maps a cluster service account to a set of app-environment
// scopes. Its uniqueness key is the (ClusterIssuerID, Namespace, ServiceAccount)
// triple — the backend has no find-by-triple route, so reconciliation lists and
// matches on it.
type WorkloadBinding struct {
	ID              string         `json:"id"`
	ClusterIssuerID string         `json:"clusterIssuerId"`
	Namespace       string         `json:"namespace"`
	ServiceAccount  string         `json:"serviceAccount"`
	Enabled         bool           `json:"enabled"`
	Scopes          []BindingScope `json:"scopes"`
	CreatedAt       string         `json:"createdAt"`
	UpdatedAt       string         `json:"updatedAt"`
}

const (
	clusterIssuersPath   = "/kagi/organizations/trust/cluster-issuers"
	workloadBindingsPath = "/kagi/organizations/trust/workload-bindings"
)

// ListClusterIssuers returns every cluster issuer registered for the active
// organization.
func (c *KagiClient) ListClusterIssuers() ([]ClusterIssuer, error) {
	body, err := c.doRequest("GET", clusterIssuersPath)
	if err != nil {
		return nil, err
	}

	var resp kagi.APIResponse[[]ClusterIssuer]
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse cluster issuers response: %w", err)
	}
	return resp.Data, nil
}

// CreateClusterIssuer registers a cluster OIDC issuer. staticJwks is optional —
// pass an empty string for a public cluster whose JWKS Kagi can fetch itself;
// pass the raw JWKS JSON for a private cluster. clusterType is optional too — an
// empty string omits the field entirely, letting the backend default it to
// GENERIC (the field is not required on create).
func (c *KagiClient) CreateClusterIssuer(issuerURL, displayName, staticJwks, clusterType string) (*ClusterIssuer, error) {
	type createRequest struct {
		IssuerURL   string `json:"issuerUrl"`
		DisplayName string `json:"displayName"`
		StaticJwks  string `json:"staticJwks,omitempty"`
		Type        string `json:"type,omitempty"`
	}

	payload := createRequest{
		IssuerURL:   issuerURL,
		DisplayName: displayName,
		StaticJwks:  staticJwks,
		Type:        clusterType,
	}

	body, err := c.doRequestWithBody("POST", clusterIssuersPath, payload)
	if err != nil {
		return nil, err
	}

	var resp kagi.APIResponse[ClusterIssuer]
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse create cluster issuer response: %w", err)
	}
	return &resp.Data, nil
}

// UpdateClusterIssuer replaces a cluster issuer's display name, pinned JWKS,
// enabled flag, and platform type. Full update: every field is applied.
// staticJwks is optional — an empty string clears any pinned JWKS and reverts
// the issuer to OIDC discovery. clusterType is required by the backend on update
// and is therefore always sent (no omitempty); callers pass the issuer's current
// type through when they are not changing it. The issuer URL is immutable and is
// therefore not part of the update.
func (c *KagiClient) UpdateClusterIssuer(clusterIssuerID, displayName, staticJwks string, enabled bool, clusterType string) (*ClusterIssuer, error) {
	type updateRequest struct {
		DisplayName string `json:"displayName"`
		StaticJwks  string `json:"staticJwks,omitempty"`
		Enabled     bool   `json:"enabled"`
		Type        string `json:"type"`
	}

	payload := updateRequest{
		DisplayName: displayName,
		StaticJwks:  staticJwks,
		Enabled:     enabled,
		Type:        clusterType,
	}

	body, err := c.doRequestWithBody("PUT", clusterIssuersPath+"/"+url.PathEscape(clusterIssuerID), payload)
	if err != nil {
		return nil, err
	}

	var resp kagi.APIResponse[ClusterIssuer]
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse update cluster issuer response: %w", err)
	}
	return &resp.Data, nil
}

// DeleteClusterIssuer removes a cluster issuer by ID. The backend returns 400 if
// any workload binding still references it.
func (c *KagiClient) DeleteClusterIssuer(clusterIssuerID string) error {
	_, err := c.doRequest("DELETE", clusterIssuersPath+"/"+url.PathEscape(clusterIssuerID))
	return err
}

// ListWorkloadBindings returns every workload binding for the active
// organization.
func (c *KagiClient) ListWorkloadBindings() ([]WorkloadBinding, error) {
	body, err := c.doRequest("GET", workloadBindingsPath)
	if err != nil {
		return nil, err
	}

	var resp kagi.APIResponse[[]WorkloadBinding]
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse workload bindings response: %w", err)
	}
	return resp.Data, nil
}

// CreateWorkloadBinding grants a cluster service account access to the given
// scopes. Each scope's app must belong to the active organization, else the
// backend returns 403.
func (c *KagiClient) CreateWorkloadBinding(clusterIssuerID, namespace, serviceAccount string, scopes []BindingScope) (*WorkloadBinding, error) {
	type createRequest struct {
		ClusterIssuerID string         `json:"clusterIssuerId"`
		Namespace       string         `json:"namespace"`
		ServiceAccount  string         `json:"serviceAccount"`
		Scopes          []BindingScope `json:"scopes"`
	}

	payload := createRequest{
		ClusterIssuerID: clusterIssuerID,
		Namespace:       namespace,
		ServiceAccount:  serviceAccount,
		Scopes:          scopes,
	}

	body, err := c.doRequestWithBody("POST", workloadBindingsPath, payload)
	if err != nil {
		return nil, err
	}

	var resp kagi.APIResponse[WorkloadBinding]
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse create workload binding response: %w", err)
	}
	return &resp.Data, nil
}

// UpdateWorkloadBinding replaces a binding's namespace, service account, enabled
// flag, and scopes. Scopes are replaced wholesale — the passed set becomes the
// binding's complete scope list.
func (c *KagiClient) UpdateWorkloadBinding(workloadBindingID, namespace, serviceAccount string, enabled bool, scopes []BindingScope) (*WorkloadBinding, error) {
	type updateRequest struct {
		Namespace      string         `json:"namespace"`
		ServiceAccount string         `json:"serviceAccount"`
		Enabled        bool           `json:"enabled"`
		Scopes         []BindingScope `json:"scopes"`
	}

	payload := updateRequest{
		Namespace:      namespace,
		ServiceAccount: serviceAccount,
		Enabled:        enabled,
		Scopes:         scopes,
	}

	body, err := c.doRequestWithBody("PUT", workloadBindingsPath+"/"+url.PathEscape(workloadBindingID), payload)
	if err != nil {
		return nil, err
	}

	var resp kagi.APIResponse[WorkloadBinding]
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse update workload binding response: %w", err)
	}
	return &resp.Data, nil
}

// DeleteWorkloadBinding removes a workload binding by ID.
func (c *KagiClient) DeleteWorkloadBinding(workloadBindingID string) error {
	_, err := c.doRequest("DELETE", workloadBindingsPath+"/"+url.PathEscape(workloadBindingID))
	return err
}
