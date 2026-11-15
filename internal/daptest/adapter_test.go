package daptest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if err == nil || !strings.Contains(err.Error(), "uv sync") {
		t.Errorf("got %v, want an error suggesting uv sync", err)
	}
}
