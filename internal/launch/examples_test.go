package launch

import (
	"path/filepath"
	"testing"
)

// TestExamples loads every example launch file and prepares each configuration.
func TestExamples(t *testing.T) {
	stubDebugpy(t, func(string) bool { return true })
	_, dlvErr := FindDlv()

	files, _ := filepath.Glob(filepath.Join("..", "..", "examples", "*.json"))
	files = append(files, filepath.Join("..", "..", "examples", ".vscode", "launch.json"))
	if len(files) < 5 {
		t.Fatalf("expected the example launch files, found %v", files)
	}

	for _, path := range files {
		f, err := Load(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if filepath.Base(f.Workspace) != "examples" {
			t.Errorf("%s: workspace %s", path, f.Workspace)
		}

		for _, cfg := range f.Configurations {
			if cfg.String("type") == "go" && cfg.String("request") == "launch" && dlvErr != nil {
				continue
			}
			vars := Vars{Workspace: f.Workspace, File: filepath.Join(f.Workspace, "hello.py")}
			if _, err := Prepare(cfg, vars); err != nil {
				t.Errorf("%s: %q: %v", path, cfg.Name(), err)
			}
		}
	}
}
