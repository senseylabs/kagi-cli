package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/senseylabs/kagi-cli/internal/ui"
)

// parsePullFlags parses args against the real pull command's flag set (so the
// tests exercise the flags users actually pass, including the inherited
// persistent --output) and restores the process-wide flag state afterwards:
// both the bound globals and pflag's Changed bits are shared across tests.
func parsePullFlags(t *testing.T, args ...string) {
	t.Helper()

	if err := pullCmd.ParseFlags(nil); err != nil {
		t.Fatalf("merge pull flags: %v", err)
	}
	outputFlag := pullCmd.Flags().Lookup("output")
	outFileFlag := pullCmd.Flags().Lookup("out-file")

	origOutput, origOutFile := outputValue, pullOutFile
	origOutputChanged, origOutFileChanged := outputFlag.Changed, outFileFlag.Changed
	t.Cleanup(func() {
		outputValue, pullOutFile = origOutput, origOutFile
		outputFlag.Changed, outFileFlag.Changed = origOutputChanged, origOutFileChanged
	})

	if err := pullCmd.ParseFlags(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
}

// resolveParsedPullOutFile runs the resolver over whatever parsePullFlags left
// on the command, capturing the two streams so a test can assert where a
// message landed.
func resolveParsedPullOutFile(t *testing.T) (outFile string, usedAlias bool, stdout, stderr string, err error) {
	t.Helper()

	var out, errBuf bytes.Buffer
	u := ui.New(ui.Options{Out: &out, Err: &errBuf, Color: ui.ColorNever})
	outFile, usedAlias, err = resolvePullOutFile(u, outputValue, pullCmd.Flags().Changed("output"),
		pullOutFile, pullCmd.Flags().Changed("out-file"))
	return outFile, usedAlias, out.String(), errBuf.String(), err
}

// The deprecated `--output <path>` spelling must write the same file, with the
// same contents and mode, as `--out-file <path>`: this is what unpins
// global-pipeline's kagi-fetch, which still passes --output "<dir>/.env".
func TestPull_OutputAliasWritesSameFileAsOutFile(t *testing.T) {
	dir := t.TempDir()
	payload := []byte("KEY=\"value\"\n")

	write := func(t *testing.T, args ...string) string {
		t.Helper()
		parsePullFlags(t, args...)
		outFile, _, _, _, err := resolveParsedPullOutFile(t)
		if err != nil {
			t.Fatalf("resolve %v: %v", args, err)
		}
		if outFile == "" {
			t.Fatalf("resolve %v: got stdout, want a file", args)
		}
		// Mirror the write runPull performs with the resolved path.
		if err := os.WriteFile(outFile, payload, 0600); err != nil {
			t.Fatalf("write %s: %v", outFile, err)
		}
		return outFile
	}

	var aliasPath, canonicalPath string
	t.Run("deprecated --output", func(t *testing.T) {
		aliasPath = write(t, "--output", filepath.Join(dir, "alias.env"))
	})
	t.Run("canonical --out-file", func(t *testing.T) {
		canonicalPath = write(t, "--out-file", filepath.Join(dir, "canonical.env"))
	})

	aliasData, err := os.ReadFile(aliasPath)
	if err != nil {
		t.Fatalf("read alias file: %v", err)
	}
	canonicalData, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatalf("read canonical file: %v", err)
	}
	if !bytes.Equal(aliasData, canonicalData) {
		t.Errorf("alias wrote %q, --out-file wrote %q", aliasData, canonicalData)
	}

	aliasInfo, err := os.Stat(aliasPath)
	if err != nil {
		t.Fatalf("stat alias file: %v", err)
	}
	if got := aliasInfo.Mode().Perm(); got != 0600 {
		t.Errorf("alias file mode = %v, want 0600", got)
	}
}

// A bare ".env" — the exact value kagi-fetch passes, and the shape that has no
// dot-free segment to fall back on — must still resolve as a file.
func TestPull_OutputAliasAcceptsBareDotEnv(t *testing.T) {
	parsePullFlags(t, "--output", ".env")

	outFile, usedAlias, _, _, err := resolveParsedPullOutFile(t)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !usedAlias {
		t.Error("expected --output .env to be consumed as a file")
	}
	if outFile != ".env" {
		t.Errorf("outFile = %q, want %q", outFile, ".env")
	}
}

// The deprecation warning is a warning: it must reach stderr (so a caller
// piping the KEY=VALUE stream still parses clean stdout) and must not fail the
// command.
func TestPull_OutputAliasWarnsOnStderrNotStdout(t *testing.T) {
	parsePullFlags(t, "--output", "/tmp/app.env")

	_, usedAlias, stdout, stderr, err := resolveParsedPullOutFile(t)
	if err != nil {
		t.Fatalf("the alias must not fail the command, got: %v", err)
	}
	if !usedAlias {
		t.Fatal("expected --output to be consumed as a file")
	}
	if stdout != "" {
		t.Errorf("stdout must stay clean, got: %q", stdout)
	}
	if !strings.Contains(stderr, "deprecated") {
		t.Errorf("expected a deprecation warning on stderr, got: %q", stderr)
	}
	if !strings.Contains(stderr, "--out-file") {
		t.Errorf("the warning must name --out-file as the replacement, got: %q", stderr)
	}
}

// Passing both spellings is a caller bug, so it errors rather than silently
// picking one.
func TestPull_OutputAliasWithOutFileIsAnError(t *testing.T) {
	parsePullFlags(t, "--output", "/tmp/alias.env", "--out-file", "/tmp/canonical.env")

	_, _, _, _, err := resolveParsedPullOutFile(t)
	if err == nil {
		t.Fatal("expected an error when both --output <path> and --out-file are given")
	}
	for _, want := range []string{"/tmp/alias.env", "/tmp/canonical.env", "--out-file"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must mention %q, got: %v", want, err)
		}
	}
}

// A format name keeps its v0.20.0 meaning: --output json still selects the
// payload format and leaves the file unset, and combining it with --out-file
// stays legal.
func TestPull_OutputFormatValueIsNotTreatedAsAFile(t *testing.T) {
	t.Run("format only", func(t *testing.T) {
		parsePullFlags(t, "--output", "json")

		outFile, usedAlias, _, stderr, err := resolveParsedPullOutFile(t)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if usedAlias || outFile != "" {
			t.Errorf("format value was taken as a file: outFile=%q usedAlias=%v", outFile, usedAlias)
		}
		if stderr != "" {
			t.Errorf("a format value must not warn, got: %q", stderr)
		}
	})

	t.Run("format with out-file", func(t *testing.T) {
		parsePullFlags(t, "-o", "json", "--out-file", "/tmp/app.json")

		outFile, usedAlias, _, _, err := resolveParsedPullOutFile(t)
		if err != nil {
			t.Fatalf("-o json --out-file is valid v0.20.0 usage, got: %v", err)
		}
		if usedAlias {
			t.Error("a format value must not be consumed as the file")
		}
		if outFile != "/tmp/app.json" {
			t.Errorf("outFile = %q, want /tmp/app.json", outFile)
		}
	})
}

// With neither flag, pull writes to stdout as before.
func TestPull_NoOutputFlagsWritesToStdout(t *testing.T) {
	parsePullFlags(t)

	outFile, usedAlias, _, stderr, err := resolveParsedPullOutFile(t)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if outFile != "" || usedAlias {
		t.Errorf("expected stdout, got outFile=%q usedAlias=%v", outFile, usedAlias)
	}
	if stderr != "" {
		t.Errorf("expected no warning, got: %q", stderr)
	}
}

// The deprecated alias is documented in --help rather than hidden, so someone
// migrating sees both the old name and its replacement.
func TestPull_HelpDocumentsTheDeprecatedAlias(t *testing.T) {
	// Merge the inherited persistent --output into pull's flag set so the
	// lookups below see it regardless of test ordering.
	if err := pullCmd.ParseFlags(nil); err != nil {
		t.Fatalf("merge pull flags: %v", err)
	}
	if !strings.Contains(pullCmd.Long, "--output <path>") || !strings.Contains(pullCmd.Long, "--out-file") {
		t.Errorf("pull long help must name the deprecated alias and its replacement, got: %q", pullCmd.Long)
	}
	usage := pullCmd.Flags().Lookup("out-file").Usage
	if !strings.Contains(usage, "deprecated alias: --output") {
		t.Errorf("--out-file usage must name the deprecated alias, got: %q", usage)
	}
	if pullCmd.Flags().Lookup("out-file").Hidden {
		t.Error("--out-file must not be hidden")
	}
	if outputFlag := pullCmd.Flags().Lookup("output"); outputFlag.Hidden {
		t.Error("--output must stay visible in help, not be hidden")
	}
}
