package cmd

import (
	"strings"
	"testing"

	"github.com/senseylabs/kagi-cli/internal/client"
)

func TestParseScopeFlag(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		wantApp     string
		wantEnv     string
		wantErr     bool
		errContains string
	}{
		{name: "simple", raw: "/village/kaizen:prod", wantApp: "/village/kaizen", wantEnv: "prod"},
		{name: "nested path", raw: "/clients/fepatex/portal:staging", wantApp: "/clients/fepatex/portal", wantEnv: "staging"},
		{name: "trims spaces", raw: " /village/sage : prod ", wantApp: "/village/sage", wantEnv: "prod"},
		{name: "no colon", raw: "/village/kaizen", wantErr: true, errContains: "expected <app-path>:<env>"},
		{name: "empty env", raw: "/village/kaizen:", wantErr: true, errContains: "expected <app-path>:<env>"},
		{name: "empty app", raw: ":prod", wantErr: true, errContains: "expected <app-path>:<env>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseScopeFlag(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got nil")
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("error %q does not contain %q", err.Error(), tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.AppPath != tt.wantApp || got.Env != tt.wantEnv {
				t.Fatalf("got {%q, %q}, want {%q, %q}", got.AppPath, got.Env, tt.wantApp, tt.wantEnv)
			}
		})
	}
}

func scopes(pairs ...[2]string) []client.BindingScope {
	out := make([]client.BindingScope, len(pairs))
	for i, p := range pairs {
		out[i] = client.BindingScope{AppID: p[0], EnvironmentSlug: p[1]}
	}
	return out
}

func TestFindWorkloadBinding(t *testing.T) {
	list := []client.WorkloadBinding{
		{ID: "wb-1", ClusterIssuerID: "iss-a", Namespace: "app", ServiceAccount: "api"},
		{ID: "wb-2", ClusterIssuerID: "iss-a", Namespace: "data", ServiceAccount: "worker"},
		{ID: "wb-3", ClusterIssuerID: "iss-b", Namespace: "app", ServiceAccount: "api"},
	}

	// Matches on the full (issuer, namespace, serviceAccount) triple — the same
	// (namespace, serviceAccount) under a different issuer must NOT match.
	got, found := findWorkloadBinding(list, "iss-a", "app", "api")
	if !found || got.ID != "wb-1" {
		t.Fatalf("expected wb-1, found=%v got=%+v", found, got)
	}

	got, found = findWorkloadBinding(list, "iss-b", "app", "api")
	if !found || got.ID != "wb-3" {
		t.Fatalf("expected wb-3, found=%v got=%+v", found, got)
	}

	if _, found := findWorkloadBinding(list, "iss-a", "app", "missing"); found {
		t.Fatal("expected no match for a missing service account")
	}
}

func TestScopesEqual(t *testing.T) {
	tests := []struct {
		name string
		a    []client.BindingScope
		b    []client.BindingScope
		want bool
	}{
		{
			name: "equal same order",
			a:    scopes([2]string{"app-1", "prod"}, [2]string{"app-2", "prod"}),
			b:    scopes([2]string{"app-1", "prod"}, [2]string{"app-2", "prod"}),
			want: true,
		},
		{
			name: "equal different order",
			a:    scopes([2]string{"app-1", "prod"}, [2]string{"app-2", "prod"}),
			b:    scopes([2]string{"app-2", "prod"}, [2]string{"app-1", "prod"}),
			want: true,
		},
		{
			name: "different env",
			a:    scopes([2]string{"app-1", "prod"}),
			b:    scopes([2]string{"app-1", "staging"}),
			want: false,
		},
		{
			name: "different length",
			a:    scopes([2]string{"app-1", "prod"}),
			b:    scopes([2]string{"app-1", "prod"}, [2]string{"app-2", "prod"}),
			want: false,
		},
		{
			name: "both empty",
			a:    nil,
			b:    nil,
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scopesEqual(tt.a, tt.b); got != tt.want {
				t.Fatalf("scopesEqual = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDecideBindingAction(t *testing.T) {
	desired := scopes([2]string{"app-1", "prod"}, [2]string{"app-2", "prod"})

	tests := []struct {
		name     string
		existing *client.WorkloadBinding
		want     bindingAction
	}{
		{
			name:     "no existing -> create",
			existing: nil,
			want:     actionCreate,
		},
		{
			name:     "same scopes enabled -> unchanged",
			existing: &client.WorkloadBinding{Enabled: true, Scopes: scopes([2]string{"app-2", "prod"}, [2]string{"app-1", "prod"})},
			want:     actionUnchanged,
		},
		{
			name:     "same scopes but disabled -> update",
			existing: &client.WorkloadBinding{Enabled: false, Scopes: desired},
			want:     actionUpdate,
		},
		{
			name:     "different scopes -> update",
			existing: &client.WorkloadBinding{Enabled: true, Scopes: scopes([2]string{"app-1", "staging"})},
			want:     actionUpdate,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := decideBindingAction(tt.existing, desired); got != tt.want {
				t.Fatalf("decideBindingAction = %q, want %q", got, tt.want)
			}
		})
	}
}
