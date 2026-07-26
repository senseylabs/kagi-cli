package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/senseylabs/kagi-cli/internal/client"
	"github.com/senseylabs/kagi-cli/internal/ui"
)

// nonTTYUI builds a UI over buffers (so it is non-interactive: Interactive()
// reports false and Pick uses the line-based fallback) with the given piped
// stdin. It returns the UI and the stderr buffer for assertions.
func nonTTYUI(stdin string) (*ui.UI, *bytes.Buffer) {
	errBuf := &bytes.Buffer{}
	u := ui.New(ui.Options{
		Out:   &bytes.Buffer{},
		Err:   errBuf,
		In:    strings.NewReader(stdin),
		Color: ui.ColorNever,
	})
	return u, errBuf
}

// TestSelectEnvironment_AutoSelectsSingle: one environment needs no prompt.
func TestSelectEnvironment_AutoSelectsSingle(t *testing.T) {
	u, _ := nonTTYUI("")
	envs := []client.Environment{{ID: "e1", Name: "Production", Slug: "prod"}}
	got, err := selectEnvironment(u, envs)
	if err != nil {
		t.Fatalf("selectEnvironment: %v", err)
	}
	if got != "prod" {
		t.Errorf("auto-select returned %q, want prod", got)
	}
}

// TestSelectEnvironment_PipedSelection: a piped line number selects that env via
// the non-interactive fallback picker.
func TestSelectEnvironment_PipedSelection(t *testing.T) {
	u, _ := nonTTYUI("2\n")
	envs := []client.Environment{
		{ID: "e1", Name: "Production", Slug: "prod"},
		{ID: "e2", Name: "Staging", Slug: "staging"},
	}
	got, err := selectEnvironment(u, envs)
	if err != nil {
		t.Fatalf("selectEnvironment: %v", err)
	}
	if got != "staging" {
		t.Errorf("piped selection returned %q, want staging", got)
	}
}

// TestSelectEnvironment_NonInteractiveEOFErrors covers the regression fix: with
// no selection available (exhausted/empty non-TTY stdin), setup must fail loudly
// instead of treating the EOF as a clean abort and exiting 0.
func TestSelectEnvironment_NonInteractiveEOFErrors(t *testing.T) {
	u, _ := nonTTYUI("") // immediate EOF
	envs := []client.Environment{
		{ID: "e1", Name: "Production", Slug: "prod"},
		{ID: "e2", Name: "Staging", Slug: "staging"},
	}
	_, err := selectEnvironment(u, envs)
	if err == nil {
		t.Fatal("expected an error on non-interactive EOF, got nil")
	}
	if errors.Is(err, errSetupAborted) {
		t.Errorf("non-interactive EOF must be a hard error, not a clean abort: %v", err)
	}
	if !strings.Contains(err.Error(), "--env") {
		t.Errorf("error should point at --env for non-interactive setup: %v", err)
	}
}

// TestWriteSetupConfig_OrgFromHomeNotStaleCWD covers FIX #8: `kagi setup` must
// pin the organization from the account-level (home) selection, not from the
// merged config that folds in the cwd kagi.yaml it is about to overwrite. A stale
// org left in the cwd kagi.yaml must NOT be re-pinned — otherwise, under strict
// tenancy, that stale org 403s every subsequent setup step with no way to clear
// it.
func TestWriteSetupConfig_OrgFromHomeNotStaleCWD(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	cwd := t.TempDir()
	t.Chdir(cwd)

	// Home records the true active organization.
	kagiDir := filepath.Join(home, ".kagi")
	if err := os.MkdirAll(kagiDir, 0700); err != nil {
		t.Fatalf("mkdir home config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(kagiDir, "config.yaml"),
		[]byte("organization: home-org\norganization-id: home-id\n"), 0600); err != nil {
		t.Fatalf("write home config: %v", err)
	}

	// A stale kagi.yaml in cwd pins a DIFFERENT org that must be ignored.
	if err := os.WriteFile(filepath.Join(cwd, "kagi.yaml"),
		[]byte("app-id: old-app\nenvironment: staging\norganization: stale-org\norganization-id: stale-id\n"), 0600); err != nil {
		t.Fatalf("write stale cwd config: %v", err)
	}

	if err := writeSetupConfig("/village/kaizen", "app-123", "production"); err != nil {
		t.Fatalf("writeSetupConfig: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(cwd, "kagi.yaml"))
	if err != nil {
		t.Fatalf("read written kagi.yaml: %v", err)
	}
	got := string(data)

	if !strings.Contains(got, "organization: home-org\n") {
		t.Errorf("kagi.yaml did not pin the home org; got:\n%s", got)
	}
	if !strings.Contains(got, "organization-id: home-id\n") {
		t.Errorf("kagi.yaml did not pin the home org-id; got:\n%s", got)
	}
	if strings.Contains(got, "stale-org") || strings.Contains(got, "stale-id") {
		t.Errorf("kagi.yaml re-pinned the stale cwd org; got:\n%s", got)
	}
	// The rest of the binding is written as expected.
	if !strings.Contains(got, "app-id: app-123\n") || !strings.Contains(got, "environment: production\n") {
		t.Errorf("kagi.yaml missing expected binding fields; got:\n%s", got)
	}
}
