package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
