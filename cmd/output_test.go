package cmd

import (
	"errors"
	"strings"
	"testing"
)

func TestLooksLikeFilePath(t *testing.T) {
	paths := []string{"/tmp/app.env", ".env", "secrets.env", "out.json", `C:\tmp\app.env`, "./x", "dir/file"}
	for _, p := range paths {
		if !looksLikeFilePath(p) {
			t.Errorf("looksLikeFilePath(%q) = false, want true", p)
		}
	}

	formats := []string{"", "table", "json", "yaml", "env", " JSON ", "xml"}
	for _, f := range formats {
		if looksLikeFilePath(f) {
			t.Errorf("looksLikeFilePath(%q) = true, want false", f)
		}
	}
}

// A caller carrying an old `kagi pull --output <path>` invocation forward gets
// pointed at --out-file rather than a bare "unsupported format".
func TestEnrichFormatError_PullHint(t *testing.T) {
	base := errors.New("unsupported format")
	err := enrichFormatError("/tmp/app.env", base, true)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "--out-file /tmp/app.env") {
		t.Errorf("expected an --out-file hint naming the path, got: %v", err)
	}
}

// Commands without an --out-file flag point at stdout redirection instead, so
// the hint never names a flag the command does not have.
func TestEnrichFormatError_RedirectHint(t *testing.T) {
	base := errors.New("invalid output format")
	err := enrichFormatError("/tmp/list.json", base, false)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "--out-file") {
		t.Errorf("hint must not name --out-file for commands that lack it, got: %v", err)
	}
	if !strings.Contains(err.Error(), "> /tmp/list.json") {
		t.Errorf("expected a redirect hint naming the path, got: %v", err)
	}
}

// A genuine format typo is left alone: the original error is returned unwrapped
// so callers keep the "want table, json, or yaml" guidance.
func TestEnrichFormatError_PassesThroughNonPaths(t *testing.T) {
	base := errors.New("invalid output format \"xml\"")
	if got := enrichFormatError("xml", base, true); !errors.Is(got, base) {
		t.Errorf("expected the original error for a non-path value, got: %v", got)
	}
}

// outputFormat routes an unparseable value through the hint.
func TestOutputFormat_FilePathValueGetsHint(t *testing.T) {
	orig := outputValue
	t.Cleanup(func() { outputValue = orig })

	outputValue = "creds.env"
	_, err := outputFormat()
	if err == nil {
		t.Fatal("expected an error for a file-path --output value")
	}
	if !strings.Contains(err.Error(), "not a file to write") {
		t.Errorf("expected the file-path hint, got: %v", err)
	}

	outputValue = "json"
	if _, err := outputFormat(); err != nil {
		t.Errorf("valid format should parse: %v", err)
	}
}
