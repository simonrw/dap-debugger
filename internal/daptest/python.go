package daptest

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// Python returns the interpreter of the repository's .venv, which has debugpy,
// and sets VIRTUAL_ENV for the rest of the test so the debugger under test
// runs debugpy with it too. It fails the test when the .venv is missing.
func Python(t testing.TB) string {
	t.Helper()

	_, file, _, _ := runtime.Caller(0)
	venv := filepath.Join(filepath.Dir(file), "..", "..", ".venv")
	python := filepath.Join(venv, "bin", "python")

	if err := exec.Command(python, "-c", "import debugpy").Run(); err != nil {
		t.Fatalf("%s cannot import debugpy (%v); run 'make test' or 'uv sync' to create it", python, err)
	}

	t.Setenv("VIRTUAL_ENV", venv)
	return python
}
