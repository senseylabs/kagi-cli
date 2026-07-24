package kube

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestListContexts_Success(t *testing.T) {
	var gotArgs []string
	withRunner(t, func(args ...string) ([]byte, error) {
		gotArgs = args
		return []byte("prod\nstaging\ndev\n"), nil
	})

	contexts, err := ListContexts()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"prod", "staging", "dev"}
	if strings.Join(contexts, ",") != strings.Join(want, ",") {
		t.Fatalf("contexts = %v, want %v", contexts, want)
	}

	// It must query kubectl for names only, in file order.
	wantArgs := []string{"config", "get-contexts", "-o", "name"}
	if strings.Join(gotArgs, " ") != strings.Join(wantArgs, " ") {
		t.Fatalf("args = %v, want %v", gotArgs, wantArgs)
	}
}

func TestListContexts_TrimsBlankLines(t *testing.T) {
	withRunner(t, func(args ...string) ([]byte, error) {
		return []byte("  prod  \n\n staging \n"), nil
	})

	contexts, err := ListContexts()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Join(contexts, ",") != "prod,staging" {
		t.Fatalf("contexts = %v, want [prod staging]", contexts)
	}
}

func TestListContexts_Empty(t *testing.T) {
	withRunner(t, func(args ...string) ([]byte, error) {
		return []byte("\n\n"), nil
	})

	_, err := ListContexts()
	if err == nil {
		t.Fatal("expected an error for an empty kubeconfig")
	}
	if !strings.Contains(err.Error(), "no kubeconfig contexts found") {
		t.Fatalf("expected a no-contexts message, got: %v", err)
	}
}

func TestListContexts_KubectlNotFound(t *testing.T) {
	withRunner(t, func(args ...string) ([]byte, error) {
		return nil, exec.ErrNotFound
	})

	_, err := ListContexts()
	if err == nil {
		t.Fatal("expected an error when kubectl is absent")
	}
	if !strings.Contains(err.Error(), "kubectl was not found") {
		t.Fatalf("expected a kubectl-not-found message, got: %v", err)
	}
}

func TestCurrentContext_Success(t *testing.T) {
	var gotArgs []string
	withRunner(t, func(args ...string) ([]byte, error) {
		gotArgs = args
		return []byte("prod\n"), nil
	})

	current, err := CurrentContext()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if current != "prod" {
		t.Fatalf("current = %q, want prod", current)
	}
	wantArgs := []string{"config", "current-context"}
	if strings.Join(gotArgs, " ") != strings.Join(wantArgs, " ") {
		t.Fatalf("args = %v, want %v", gotArgs, wantArgs)
	}
}

func TestCurrentContext_UnsetIsNotAnError(t *testing.T) {
	// `kubectl config current-context` exits non-zero when nothing is set; that is
	// a normal state for the picker, so it must return "" with no error.
	withRunner(t, func(args ...string) ([]byte, error) {
		return nil, errors.New("error: current-context is not set")
	})

	current, err := CurrentContext()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if current != "" {
		t.Fatalf("current = %q, want empty", current)
	}
}

func TestCurrentContext_KubectlNotFound(t *testing.T) {
	withRunner(t, func(args ...string) ([]byte, error) {
		return nil, exec.ErrNotFound
	})

	_, err := CurrentContext()
	if err == nil {
		t.Fatal("expected an error when kubectl is absent")
	}
	if !strings.Contains(err.Error(), "kubectl was not found") {
		t.Fatalf("expected a kubectl-not-found message, got: %v", err)
	}
}
