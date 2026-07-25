package config

import (
	"os"
	"path/filepath"
	"testing"
)

// hermeticHome points os.UserHomeDir() at a fresh temp dir for the duration of
// the test. On darwin os.UserHomeDir() reads $HOME; on other unixes it also
// consults $HOME first, so setting HOME is sufficient across the platforms we
// build for. We clear the XDG override that could otherwise redirect lookups.
func hermeticHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Guard against XDG_* leaking the developer's real config on non-darwin.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return home
}

// writeHomeConfig writes ~/.kagi/config.yaml with the given raw yaml body.
func writeHomeConfig(t *testing.T, home, body string) string {
	t.Helper()
	dir := filepath.Join(home, ".kagi")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir home config dir: %v", err)
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatalf("write home config: %v", err)
	}
	return path
}

// writeCWDConfig writes kagi.yaml into dir with the given raw yaml body.
func writeCWDConfig(t *testing.T, dir, body string) {
	t.Helper()
	path := filepath.Join(dir, "kagi.yaml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatalf("write cwd config: %v", err)
	}
}

func TestLoad_Precedence(t *testing.T) {
	tests := []struct {
		name       string
		homeBody   string // "" means no home config file
		cwdBody    string // "" means no cwd kagi.yaml
		wantAPIURL string
		wantIssuer string
		wantAppID  string
		wantFolder string
		wantEnv    string
		wantOrg    string
		wantOrgID  string
	}{
		{
			name:       "home only",
			homeBody:   "api-url: https://home.example\nissuer: https://home.iss\napp-id: home-app\nfolder-path: /home/path\nenvironment: staging\norganization: home-org\norganization-id: home-id\n",
			wantAPIURL: "https://home.example",
			wantIssuer: "https://home.iss",
			wantAppID:  "home-app",
			wantFolder: "/home/path",
			wantEnv:    "staging",
			wantOrg:    "home-org",
			wantOrgID:  "home-id",
		},
		{
			name:       "cwd only",
			cwdBody:    "api-url: https://cwd.example\napp-id: cwd-app\nenvironment: production\norganization: cwd-org\n",
			wantAPIURL: "https://cwd.example",
			wantAppID:  "cwd-app",
			wantEnv:    "production",
			wantOrg:    "cwd-org",
		},
		{
			name: "cwd overrides home field-by-field, home fills gaps",
			// Home supplies issuer, folder-path, org-id; cwd overrides the rest.
			homeBody:   "api-url: https://home.example\nissuer: https://home.iss\napp-id: home-app\nfolder-path: /home/path\nenvironment: staging\norganization: home-org\norganization-id: home-id\n",
			cwdBody:    "api-url: https://cwd.example\napp-id: cwd-app\nenvironment: production\norganization: cwd-org\n",
			wantAPIURL: "https://cwd.example", // overridden by cwd
			wantIssuer: "https://home.iss",    // gap filled by home
			wantAppID:  "cwd-app",             // overridden by cwd
			wantFolder: "/home/path",          // gap filled by home
			wantEnv:    "production",          // overridden by cwd
			wantOrg:    "cwd-org",             // overridden by cwd
			wantOrgID:  "home-id",             // gap filled by home (cwd empty)
		},
		{
			name:     "empty cwd values do not clobber home",
			homeBody: "api-url: https://home.example\napp-id: home-app\norganization: home-org\n",
			// cwd sets only environment; empty fields must NOT overwrite home.
			cwdBody:    "environment: production\n",
			wantAPIURL: "https://home.example",
			wantAppID:  "home-app",
			wantEnv:    "production",
			wantOrg:    "home-org",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := hermeticHome(t)
			cwd := t.TempDir()
			t.Chdir(cwd)

			if tc.homeBody != "" {
				writeHomeConfig(t, home, tc.homeBody)
			}
			if tc.cwdBody != "" {
				writeCWDConfig(t, cwd, tc.cwdBody)
			}

			got := Load()
			if got.APIURL != tc.wantAPIURL {
				t.Errorf("APIURL = %q, want %q", got.APIURL, tc.wantAPIURL)
			}
			if got.Issuer != tc.wantIssuer {
				t.Errorf("Issuer = %q, want %q", got.Issuer, tc.wantIssuer)
			}
			if got.AppID != tc.wantAppID {
				t.Errorf("AppID = %q, want %q", got.AppID, tc.wantAppID)
			}
			if got.FolderPath != tc.wantFolder {
				t.Errorf("FolderPath = %q, want %q", got.FolderPath, tc.wantFolder)
			}
			if got.Environment != tc.wantEnv {
				t.Errorf("Environment = %q, want %q", got.Environment, tc.wantEnv)
			}
			if got.Organization != tc.wantOrg {
				t.Errorf("Organization = %q, want %q", got.Organization, tc.wantOrg)
			}
			if got.OrganizationID != tc.wantOrgID {
				t.Errorf("OrganizationID = %q, want %q", got.OrganizationID, tc.wantOrgID)
			}
		})
	}
}

func TestLoad_NoConfigAtAll(t *testing.T) {
	hermeticHome(t)
	t.Chdir(t.TempDir())

	got := Load()
	if (got != Config{}) {
		t.Errorf("Load() with no config = %+v, want zero Config", got)
	}
}

func TestSaveOrganization_RoundTripPreservesKeys(t *testing.T) {
	home := hermeticHome(t)
	// cwd has no kagi.yaml so Load reflects the home file we round-trip through.
	t.Chdir(t.TempDir())

	// Seed a home config with unrelated keys that must survive the save.
	writeHomeConfig(t, home, "api-url: https://home.example\napp-id: home-app\nfolder-path: /home/path\nenvironment: staging\n")

	if err := SaveOrganization("acme", "org-uuid-123"); err != nil {
		t.Fatalf("SaveOrganization: %v", err)
	}

	got := Load()
	if got.Organization != "acme" {
		t.Errorf("Organization = %q, want %q", got.Organization, "acme")
	}
	if got.OrganizationID != "org-uuid-123" {
		t.Errorf("OrganizationID = %q, want %q", got.OrganizationID, "org-uuid-123")
	}
	// Other keys preserved.
	if got.APIURL != "https://home.example" {
		t.Errorf("APIURL = %q, want preserved", got.APIURL)
	}
	if got.AppID != "home-app" {
		t.Errorf("AppID = %q, want preserved", got.AppID)
	}
	if got.FolderPath != "/home/path" {
		t.Errorf("FolderPath = %q, want preserved", got.FolderPath)
	}
	if got.Environment != "staging" {
		t.Errorf("Environment = %q, want preserved", got.Environment)
	}
}

func TestSaveOrganization_FirstSaveNoExistingFile(t *testing.T) {
	hermeticHome(t)
	t.Chdir(t.TempDir())

	if err := SaveOrganization("acme", "org-uuid-123"); err != nil {
		t.Fatalf("SaveOrganization on fresh home: %v", err)
	}
	slug, id := HomeOrganization()
	if slug != "acme" || id != "org-uuid-123" {
		t.Errorf("HomeOrganization() = (%q, %q), want (acme, org-uuid-123)", slug, id)
	}
}

func TestClearOrganization(t *testing.T) {
	t.Run("removes org keys but preserves others", func(t *testing.T) {
		home := hermeticHome(t)
		t.Chdir(t.TempDir())
		writeHomeConfig(t, home, "api-url: https://home.example\napp-id: home-app\norganization: acme\norganization-id: org-uuid-123\n")

		if err := ClearOrganization(); err != nil {
			t.Fatalf("ClearOrganization: %v", err)
		}

		got := Load()
		if got.Organization != "" {
			t.Errorf("Organization = %q, want cleared", got.Organization)
		}
		if got.OrganizationID != "" {
			t.Errorf("OrganizationID = %q, want cleared", got.OrganizationID)
		}
		// Preserved keys.
		if got.APIURL != "https://home.example" {
			t.Errorf("APIURL = %q, want preserved", got.APIURL)
		}
		if got.AppID != "home-app" {
			t.Errorf("AppID = %q, want preserved", got.AppID)
		}
	})

	t.Run("no-op when no home config", func(t *testing.T) {
		hermeticHome(t)
		t.Chdir(t.TempDir())
		if err := ClearOrganization(); err != nil {
			t.Fatalf("ClearOrganization with no home config = %v, want nil", err)
		}
	})
}

func TestHomeOrganization(t *testing.T) {
	t.Run("reads from home file ignoring cwd pin", func(t *testing.T) {
		home := hermeticHome(t)
		cwd := t.TempDir()
		t.Chdir(cwd)
		writeHomeConfig(t, home, "organization: home-org\norganization-id: home-id\n")
		// A cwd pin must NOT influence HomeOrganization.
		writeCWDConfig(t, cwd, "organization: cwd-org\norganization-id: cwd-id\n")

		slug, id := HomeOrganization()
		if slug != "home-org" || id != "home-id" {
			t.Errorf("HomeOrganization() = (%q, %q), want (home-org, home-id)", slug, id)
		}
	})

	t.Run("empty when no home file", func(t *testing.T) {
		hermeticHome(t)
		t.Chdir(t.TempDir())
		slug, id := HomeOrganization()
		if slug != "" || id != "" {
			t.Errorf("HomeOrganization() = (%q, %q), want empty", slug, id)
		}
	})
}

func TestCWDOrganization(t *testing.T) {
	t.Run("reads from cwd kagi.yaml ignoring home", func(t *testing.T) {
		home := hermeticHome(t)
		cwd := t.TempDir()
		t.Chdir(cwd)
		writeHomeConfig(t, home, "organization: home-org\norganization-id: home-id\n")
		writeCWDConfig(t, cwd, "organization: cwd-org\norganization-id: cwd-id\n")

		slug, id := CWDOrganization()
		if slug != "cwd-org" || id != "cwd-id" {
			t.Errorf("CWDOrganization() = (%q, %q), want (cwd-org, cwd-id)", slug, id)
		}
	})

	t.Run("empty when no cwd file", func(t *testing.T) {
		hermeticHome(t)
		t.Chdir(t.TempDir())
		slug, id := CWDOrganization()
		if slug != "" || id != "" {
			t.Errorf("CWDOrganization() = (%q, %q), want empty", slug, id)
		}
	})
}

func TestIsLegacy(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want bool
	}{
		{
			name: "legacy: project set, no app-id",
			cfg:  Config{Project: "village", App: "kaizen"},
			want: true,
		},
		{
			name: "legacy: only app set, no app-id",
			cfg:  Config{App: "kaizen"},
			want: true,
		},
		{
			name: "not legacy: app-id present alongside project/app",
			cfg:  Config{AppID: "app-123", Project: "village", App: "kaizen"},
			want: false,
		},
		{
			name: "not legacy: app-id present, no project/app",
			cfg:  Config{AppID: "app-123"},
			want: false,
		},
		{
			name: "not legacy: empty config",
			cfg:  Config{},
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.IsLegacy(); got != tc.want {
				t.Errorf("IsLegacy() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestLoad_LegacyDetectorFootgun documents the known sharp edge called out in
// plan Phase 6.3 (config.go:87). Load merges cwd over home field-by-field, so a
// cwd kagi.yaml carrying ONLY the legacy project/app keys, layered over a home
// config that already has an app-id, produces a merged Config whose AppID is
// still non-empty (inherited from home) AND whose Project/App are now set. Per
// IsLegacy's definition that is NOT legacy, even though the cwd file, read on its
// own, clearly is legacy.
//
// This asserts the CURRENT (arguably surprising) behavior so a future change to
// the merge or the detector is caught. It is a KNOWN footgun, not desired
// behavior: the folder-model app-id from home masks the legacy cwd pin.
func TestLoad_LegacyDetectorFootgun(t *testing.T) {
	home := hermeticHome(t)
	cwd := t.TempDir()
	t.Chdir(cwd)

	// Home config already migrated to the folder model (has app-id).
	writeHomeConfig(t, home, "app-id: home-app\nfolder-path: /village/kaizen\n")
	// CWD pin is legacy-only: just project/app, no app-id.
	writeCWDConfig(t, cwd, "project: village\napp: kaizen\n")

	got := Load()
	// Sanity: merge left home's app-id in place and layered the legacy keys on.
	if got.AppID != "home-app" {
		t.Fatalf("AppID = %q, want home-app (inherited from home)", got.AppID)
	}
	if got.Project != "village" || got.App != "kaizen" {
		t.Fatalf("Project/App = %q/%q, want village/kaizen (from cwd)", got.Project, got.App)
	}

	// The footgun: the merged config is reported NOT legacy because the home
	// app-id masks the legacy cwd pin. Read alone, the cwd file IS legacy.
	if got.IsLegacy() {
		t.Errorf("IsLegacy() = true, want false (documents known footgun)")
	}
	cwdOnly := Config{Project: "village", App: "kaizen"}
	if !cwdOnly.IsLegacy() {
		t.Errorf("cwd file read alone IsLegacy() = false, want true")
	}
}
