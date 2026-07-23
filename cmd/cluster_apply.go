package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/senseylabs/kagi-cli/internal/client"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// applySpec is the parsed shape of a `kagi cluster apply` file.
//
//	issuer:
//	  url: https://oidc.example/cluster   # optional — auto-detected via kubectl if omitted
//	  name: prod-cluster                  # display name; defaults to the issuer URL
//	  staticJwks: auto                    # auto (detect via kubectl) | <path to JWKS file> | omitted (public cluster)
//	bindings:
//	  - namespace: app
//	    serviceAccount: api
//	    scopes:
//	      - app: /village/kaizen
//	        env: prod
type applySpec struct {
	Issuer   applyIssuer    `yaml:"issuer"`
	Bindings []applyBinding `yaml:"bindings"`
}

type applyIssuer struct {
	URL string `yaml:"url"`
	// Name is the display name for the issuer. Optional; defaults to the URL.
	Name string `yaml:"name"`
	// StaticJwks controls the JWKS source: "auto" detects it via kubectl, any
	// other non-empty value is treated as a path to a JWKS file, and an empty
	// value (omitted or null) registers no static JWKS (public cluster).
	StaticJwks string `yaml:"staticJwks"`
}

type applyBinding struct {
	Namespace      string       `yaml:"namespace"`
	ServiceAccount string       `yaml:"serviceAccount"`
	Scopes         []applyScope `yaml:"scopes"`
}

type applyScope struct {
	App string `yaml:"app"`
	Env string `yaml:"env"`
}

// staticJwksAuto is the sentinel StaticJwks value that means "detect the JWKS
// from the cluster via kubectl" rather than read it from a file.
const staticJwksAuto = "auto"

// parseApplyFile parses and validates an apply file. It is pure (no network, no
// kubectl) so the parse+plan is unit-testable. Structural problems — a binding
// with no namespace/serviceAccount, a scope missing app/env — are rejected here.
func parseApplyFile(data []byte) (*applySpec, error) {
	var spec applySpec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("failed to parse apply file: %w", err)
	}

	for i, b := range spec.Bindings {
		if strings.TrimSpace(b.Namespace) == "" {
			return nil, fmt.Errorf("binding %d: namespace is required", i)
		}
		if strings.TrimSpace(b.ServiceAccount) == "" {
			return nil, fmt.Errorf("binding %d (%s): serviceAccount is required", i, b.Namespace)
		}
		if len(b.Scopes) == 0 {
			return nil, fmt.Errorf("binding %d (%s/%s): at least one scope is required", i, b.Namespace, b.ServiceAccount)
		}
		for j, s := range b.Scopes {
			if strings.TrimSpace(s.App) == "" || strings.TrimSpace(s.Env) == "" {
				return nil, fmt.Errorf("binding %d (%s/%s) scope %d: both app and env are required", i, b.Namespace, b.ServiceAccount, j)
			}
		}
	}
	return &spec, nil
}

func runClusterApply(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	data, err := os.ReadFile(clusterApplyFile)
	if err != nil {
		return fmt.Errorf("failed to read apply file %s: %w", clusterApplyFile, err)
	}

	spec, err := parseApplyFile(data)
	if err != nil {
		return err
	}

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	// Resolve the issuer's identity (URL + display name). The URL is the
	// idempotency key, so it is resolved before we look for an existing issuer.
	issuerURL, name, err := resolveClusterIssuerIdentity(spec.Issuer.URL, spec.Issuer.Name, clusterApplyContext)
	if err != nil {
		return err
	}

	issuers, err := vc.ListClusterIssuers()
	if err != nil {
		return fmt.Errorf("failed to list cluster issuers: %w", err)
	}
	existingIssuer := matchIssuerByURL(issuers, issuerURL)

	// Map the file's staticJwks field to a JWKS source: "auto" detects it via
	// kubectl, any other non-empty value is a file path, empty means public.
	jwksFile := ""
	detectJwks := false
	switch strings.TrimSpace(spec.Issuer.StaticJwks) {
	case "":
		// public cluster — no static JWKS
	case staticJwksAuto:
		detectJwks = true
	default:
		jwksFile = spec.Issuer.StaticJwks
	}

	// Resolve the desired JWKS lazily: for a public cluster (no source) it is
	// unambiguously empty, so skip the kubectl shell / file read entirely rather
	// than resolving a value the reconcile would only discard.
	desiredJwks := ""
	if jwksFile != "" || detectJwks {
		desiredJwks, err = resolveIssuerJwks(jwksFile, detectJwks, clusterApplyContext)
		if err != nil {
			return err
		}
	}

	// The apply file carries no enabled flag, so an existing issuer keeps its
	// current enabled state; a newly created one is enabled by the backend default.
	desiredEnabled := true
	if existingIssuer != nil {
		desiredEnabled = existingIssuer.Enabled
	}

	issuer := existingIssuer
	switch decideIssuerAction(existingIssuer, name, desiredJwks, desiredEnabled) {
	case issuerActionCreate:
		issuer, err = vc.CreateClusterIssuer(issuerURL, name, desiredJwks)
		if err != nil {
			return fmt.Errorf("failed to register cluster issuer %q: %w", issuerURL, err)
		}
		fmt.Printf("issuer %q: created (%s)\n", issuer.DisplayName, issuer.IssuerURL)
	case issuerActionUpdate:
		issuer, err = vc.UpdateClusterIssuer(existingIssuer.ID, name, desiredJwks, desiredEnabled)
		if err != nil {
			return fmt.Errorf("failed to update cluster issuer %q: %w", name, err)
		}
		fmt.Printf("issuer %q: updated (%s)\n", issuer.DisplayName, issuer.IssuerURL)
	default: // issuerActionUnchanged
		fmt.Printf("issuer %q: unchanged (%s)\n", issuer.DisplayName, issuer.IssuerURL)
	}

	// Snapshot existing bindings once; reconcile each desired binding against it.
	existingBindings, err := vc.ListWorkloadBindings()
	if err != nil {
		return fmt.Errorf("failed to list workload bindings: %w", err)
	}

	// Track which existing bindings on THIS issuer are still desired, so --prune
	// can drop the rest.
	desiredKeys := make(map[string]bool, len(spec.Bindings))

	for _, b := range spec.Bindings {
		inputs := make([]scopeInput, len(b.Scopes))
		for i, s := range b.Scopes {
			inputs[i] = scopeInput{AppPath: s.App, Env: s.Env}
		}

		scopes, err := resolveScopeInputs(vc, inputs)
		if err != nil {
			return fmt.Errorf("binding %s/%s: %w", b.Namespace, b.ServiceAccount, err)
		}

		desiredKeys[bindingKey(b.Namespace, b.ServiceAccount)] = true
		existing, _ := findWorkloadBinding(existingBindings, issuer.ID, b.Namespace, b.ServiceAccount)

		switch decideBindingAction(existing, scopes) {
		case actionCreate:
			if _, err := vc.CreateWorkloadBinding(issuer.ID, b.Namespace, b.ServiceAccount, scopes); err != nil {
				return fmt.Errorf("failed to create workload binding %s/%s: %w", b.Namespace, b.ServiceAccount, err)
			}
			fmt.Printf("binding %s/%s: created (%d scope(s))\n", b.Namespace, b.ServiceAccount, len(scopes))
		case actionUnchanged:
			fmt.Printf("binding %s/%s: unchanged\n", b.Namespace, b.ServiceAccount)
		default: // actionUpdate
			if _, err := vc.UpdateWorkloadBinding(existing.ID, b.Namespace, b.ServiceAccount, true, scopes); err != nil {
				return fmt.Errorf("failed to update workload binding %s/%s: %w", b.Namespace, b.ServiceAccount, err)
			}
			fmt.Printf("binding %s/%s: updated (%d scope(s))\n", b.Namespace, b.ServiceAccount, len(scopes))
		}
	}

	if clusterApplyPrune {
		if err := pruneBindings(vc, issuer, existingBindings, desiredKeys); err != nil {
			return err
		}
	}

	return nil
}

// bindingKey is the (namespace, serviceAccount) identity used to match a desired
// binding against an existing one during prune (both are already scoped to one
// issuer).
func bindingKey(namespace, serviceAccount string) string {
	return namespace + "\x00" + serviceAccount
}

// pruneBindings deletes workload bindings on the given issuer that are absent
// from the desired set. Per the no-silent-failures rule, every drop is logged to
// stderr and a delete error aborts the prune rather than being swallowed.
func pruneBindings(vc *client.KagiClient, issuer *client.ClusterIssuer, existing []client.WorkloadBinding, desiredKeys map[string]bool) error {
	for i := range existing {
		b := existing[i]
		if b.ClusterIssuerID != issuer.ID {
			continue
		}
		if desiredKeys[bindingKey(b.Namespace, b.ServiceAccount)] {
			continue
		}
		fmt.Fprintf(os.Stderr, "prune: deleting workload binding %s/%s (id %s) on issuer %q — absent from apply file\n",
			b.Namespace, b.ServiceAccount, b.ID, issuer.DisplayName)
		if err := vc.DeleteWorkloadBinding(b.ID); err != nil {
			return fmt.Errorf("prune: failed to delete workload binding %s/%s: %w", b.Namespace, b.ServiceAccount, err)
		}
		fmt.Printf("binding %s/%s: pruned\n", b.Namespace, b.ServiceAccount)
	}
	return nil
}
