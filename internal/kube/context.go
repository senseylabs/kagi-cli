package kube

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ListContexts returns the kubeconfig context names via
// `kubectl config get-contexts -o name`, in kubectl's own (file) order. It
// reuses the same injectable Runner as issuer/JWKS detection, so it honors the
// user's existing kubeconfig and $KUBECONFIG and stays network-free in tests.
// A missing kubectl binary or an empty kubeconfig surfaces as an actionable
// error telling the user to install kubectl or pass --issuer-url explicitly,
// matching the phrasing used by DetectIssuerURL.
func ListContexts() ([]string, error) {
	out, err := Runner("config", "get-contexts", "-o", "name")
	if err != nil {
		return nil, mapContextErr("list kubeconfig contexts", err)
	}

	contexts := make([]string, 0)
	for _, line := range strings.Split(string(out), "\n") {
		name := strings.TrimSpace(line)
		if name != "" {
			contexts = append(contexts, name)
		}
	}
	if len(contexts) == 0 {
		return nil, fmt.Errorf("no kubeconfig contexts found. Configure cluster access with kubectl, or pass --issuer-url explicitly")
	}
	return contexts, nil
}

// CurrentContext returns the active context name via
// `kubectl config current-context`, or "" (with no error) when no current
// context is set — an unset current context is a normal state, not a failure,
// so the interactive picker can still fall back to the first entry. A missing
// kubectl binary or other invocation failure is returned as an actionable error.
func CurrentContext() (string, error) {
	out, err := Runner("config", "current-context")
	if err != nil {
		// `kubectl config current-context` exits non-zero when no current context
		// is set. That is not fatal for the picker (it just means "no default
		// highlight"), so translate a plain command failure into an empty result
		// rather than an error, while still surfacing a missing-binary error.
		if errors.Is(err, exec.ErrNotFound) {
			return "", mapContextErr("read the current kubeconfig context", err)
		}
		return "", nil
	}
	return strings.TrimSpace(string(out)), nil
}

// mapContextErr maps a kubectl invocation failure to an actionable error,
// calling out a missing kubectl binary specifically and otherwise surfacing
// kubectl's stderr (which explains the real cause) verbatim.
func mapContextErr(action string, err error) error {
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("kubectl was not found on your PATH. Install kubectl and configure cluster access, or pass --issuer-url explicitly")
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
		return fmt.Errorf("failed to %s via kubectl: %s. Pass --issuer-url explicitly to skip auto-detection", action, string(exitErr.Stderr))
	}
	return fmt.Errorf("failed to %s via kubectl: %w. Pass --issuer-url explicitly to skip auto-detection", action, err)
}
