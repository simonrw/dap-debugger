package daptest

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/simonrw/dap-debugger/internal/launch"
)

// UseAdapter makes the real adapter for a configuration type available for
// the rest of the test and returns its executable.
//
// For "debugpy" or "python" it activates the repository's .venv, which
// `uv sync` builds from uv.lock, by setting VIRTUAL_ENV and clearing
// PYTHONPATH and PYTHONHOME. It returns the .venv interpreter, and fails the
// test unless launch would run both the adapter and the debuggee of a
// configuration in Examples with it.
//
// For "go" it returns the dlv that launch would use, and skips the test when
// there is none.
func UseAdapter(t testing.TB, typ string) string {
	t.Helper()

	switch typ {
	case "debugpy", "python":
		venv := filepath.Join(repoRoot(t), ".venv")
		t.Setenv("VIRTUAL_ENV", venv)
		t.Setenv("PYTHONPATH", "")
		t.Setenv("PYTHONHOME", "")

		python := filepath.Join(venv, "bin", "python")
		if err := checkPython(python, Examples(t)); err != nil {
			t.Fatal(err)
		}
		return python

	case "go":
		dlv, err := launch.FindDlv()
		if err != nil {
			t.Skip(err)
		}
		return dlv
	}

	t.Fatalf("no adapter for configuration type %q", typ)
	return ""
}

// Examples returns the repository's examples workspace.
func Examples(t testing.TB) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "examples")
}

// checkPython returns an error unless launch would run a debugpy launch
// configuration in workspace with python as both adapter and debuggee
// interpreter.
func checkPython(python, workspace string) error {
	cfg := launch.Config{"name": "daptest", "type": "debugpy", "request": "launch", "program": "hello.py"}
	plan, err := launch.Prepare(cfg, launch.Vars{Workspace: workspace, Env: os.Getenv})
	if err != nil {
		return fmt.Errorf("%v; run 'make test' or 'uv sync' to create %s", err, python)
	}

	adapter := plan.Command[0]
	// debugpy runs the program with the adapter's interpreter unless python is set
	program := adapter
	if p, ok := plan.Arguments["python"].(string); ok {
		program = p
	}
	if adapter != python || program != python {
		return fmt.Errorf("launch would run the debugpy adapter with %s and the program with %s, not %s; run 'make test' or 'uv sync' to create it with debugpy, and remove any other virtual environment from %s", adapter, program, python, workspace)
	}
	return nil
}

// repoRoot returns the directory holding go.mod above the working directory,
// which go test sets to the package under test.
func repoRoot(t testing.TB) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the working directory")
		}
		dir = parent
	}
}
