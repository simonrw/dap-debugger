package daptest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/simonrw/dap-debugger/internal/launch"
)

func TestCheckPythonRejectsWorkspaceVenv(t *testing.T) {
	python := UseAdapter(t, "debugpy")

	for _, name := range []string{".venv", "venv"} {
		workspace := t.TempDir()
		stray := filepath.Join(workspace, name, "bin", "python")
		if err := os.MkdirAll(filepath.Dir(stray), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(stray, nil, 0o755); err != nil {
			t.Fatal(err)
		}

		err := checkPython(python, workspace)
		if err == nil || !strings.Contains(err.Error(), stray) {
			t.Errorf("%s: got %v, want an error naming %s", name, err, stray)
		}
	}
}

func TestCheckPythonRejectsMissingVenv(t *testing.T) {
	venv := filepath.Join(t.TempDir(), ".venv")
	t.Setenv("VIRTUAL_ENV", venv)
	python := filepath.Join(venv, "bin", "python")

	err := checkPython(python, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "the repository's .venv is missing") {
		t.Errorf("got %v, want an error saying the .venv is missing", err)
	}
}

func TestUseAdapterPutsDlvOnPath(t *testing.T) {
	installed, err := launch.FindDlv()
	if err != nil {
		t.Skip(err)
	}
	gobin := t.TempDir()
	if err := os.Symlink(installed, filepath.Join(gobin, "dlv")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOBIN", gobin)
	t.Setenv("PATH", t.TempDir())

	dlv := UseAdapter(t, "go")

	found, err := exec.LookPath("dlv")
	if err != nil || found != dlv {
		t.Errorf("dlv on PATH is %q (%v), want %s", found, err, dlv)
	}
}
