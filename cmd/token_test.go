package cmd

import (
	"strings"
	"testing"

	"github.com/senseylabs/kagi-cli/internal/client"
)

// TestMatchAccessToken covers the id-prefix resolution `token revoke` uses so a
// truncated id printed by `token list` is still usable: exact id, unambiguous
// prefix, ambiguous prefix (which must name the candidates), and not-found.
func TestMatchAccessToken(t *testing.T) {
	tokens := []client.AccessToken{
		{ID: "tok-aaa111", Name: "ci"},
		{ID: "tok-bbb222", Name: "deploy"},
	}

	tests := []struct {
		name       string
		ref        string
		wantID     string
		wantErr    string
		wantErrHas []string
	}{
		{name: "exact id", ref: "tok-aaa111", wantID: "tok-aaa111"},
		{name: "unambiguous prefix", ref: "tok-aaa", wantID: "tok-aaa111"},
		{name: "other unambiguous prefix", ref: "tok-b", wantID: "tok-bbb222"},
		{name: "ambiguous prefix lists candidates", ref: "tok-", wantErr: "ambiguous", wantErrHas: []string{"tok-aaa111", "tok-bbb222"}},
		{name: "not found", ref: "nope", wantErr: "not found"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := matchAccessToken(tokens, tt.ref)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tt.wantErr)
				}
				for _, want := range tt.wantErrHas {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("ambiguity error %q does not name candidate %q", err.Error(), want)
					}
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
