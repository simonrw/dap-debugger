package daptest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// UseAdapter makes the real adapter for a configuration type available for
// the rest of the test and returns its executable.
//
// For "debugpy" it activates the repository's .venv, which `uv sync` builds
// with debugpy, by setting VIRTUAL_ENV, and returns its interpreter. The
// launch package prefers a workspace .venv or venv over VIRTUAL_ENV, so the
// test fails if the examples workspace has one, as it does if the repository
// .venv is missing.
//
// For "go" it returns the dlv on PATH and skips the test when there is none.
func UseAdapter(t testing.TB, typ string) string {
	t.Helper()

	switch typ {
	case "debugpy":
		_, file, _, _ := runtime.Caller(0)
		root := filepath.Join(filepath.Dir(file), "..", "..")
		python, err := venvPython(root)
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv("VIRTUAL_ENV", filepath.Dir(filepath.Dir(python)))
		return python

	case "go":
		dlv, err := exec.LookPath("dlv")
		if err != nil {
			t.Skip("dlv not installed")
		}
		return dlv
	}

	t.Fatalf("no adapter for configuration type %q", typ)
	return ""
}

// venvPython returns the interpreter of the .venv under root, after checking
// it can import debugpy and no venv in root/examples would take its place.
func venvPython(root string) (string, error) {
	for _, name := range []string{".venv", "venv"} {
		stray := filepath.Join(root, "examples", name)
		if _, err := os.Stat(filepath.Join(stray, "bin", "python")); err == nil {
			return "", fmt.Errorf("%s would replace the repository .venv as the debuggee's interpreter; remove it", stray)
		}
	}

	python := filepath.Join(root, ".venv", "bin", "python")
	if err := exec.Command(python, "-c", "import debugpy").Run(); err != nil {
		return "", fmt.Errorf("%s cannot import debugpy (%v); run 'make test' or 'uv sync' to create it", python, err)
	}
	return python, nil
}
