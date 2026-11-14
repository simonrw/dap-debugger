package daptest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVenvPythonRejectsExamplesVenv(t *testing.T) {
	venv, err := filepath.Abs(filepath.Join("..", "..", ".venv"))
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{".venv", "venv"} {
		root := t.TempDir()
		if err := os.Symlink(venv, filepath.Join(root, ".venv")); err != nil {
			t.Fatal(err)
		}
		stray := filepath.Join(root, "examples", name)
		if err := os.MkdirAll(filepath.Join(stray, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stray, "bin", "python"), nil, 0o755); err != nil {
			t.Fatal(err)
		}

		_, err := venvPython(root)
		if err == nil || !strings.Contains(err.Error(), stray) {
			t.Errorf("examples/%s: got %v, want an error naming %s", name, err, stray)
		}
	}
}
