package cmd

import "testing"

// TestDecidePruneGate locks in the CI-safety rule for `cluster apply --prune`: a
// run with bindings to delete and neither --yes nor an interactive terminal must
// be BLOCKED (a hard error), never silently skipped — otherwise CI would exit 0
// while the revoked bindings keep their secret access.
func TestDecidePruneGate(t *testing.T) {
	tests := []struct {
		name        string
		toPrune     int
		yes         bool
		interactive bool
		want        pruneGate
	}{
		{name: "nothing to prune proceeds", toPrune: 0, yes: false, interactive: false, want: pruneProceed},
		{name: "nothing to prune proceeds on tty", toPrune: 0, yes: false, interactive: true, want: pruneProceed},
		{name: "yes authorizes non-interactive", toPrune: 3, yes: true, interactive: false, want: pruneProceed},
		{name: "yes authorizes interactive", toPrune: 3, yes: true, interactive: true, want: pruneProceed},
		{name: "tty without yes prompts", toPrune: 3, yes: false, interactive: true, want: prunePrompt},
		{name: "non-interactive without yes is blocked", toPrune: 3, yes: false, interactive: false, want: pruneBlocked},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := decidePruneGate(tt.toPrune, tt.yes, tt.interactive); got != tt.want {
				t.Fatalf("decidePruneGate(%d, %t, %t) = %v, want %v", tt.toPrune, tt.yes, tt.interactive, got, tt.want)
			}
		})
	}
}
