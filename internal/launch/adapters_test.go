package launch

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadAdapters(t *testing.T) {
	dir := t.TempDir()

	if adapters, err := LoadAdapters(filepath.Join(dir, "missing.json")); err != nil || adapters != nil {
		t.Fatalf("missing file: %v %v", adapters, err)
	}

	path := filepath.Join(dir, "adapters.json")
	writeFile(t, path, `{
  // comments are allowed
  "codelldb": {"command": ["codelldb", "--port", "{port}"]},
  "remote": {"address": "10.0.0.2:4711"},
}`)
	adapters, err := LoadAdapters(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(adapters) != 2 || adapters["remote"].Address != "10.0.0.2:4711" {
		t.Fatalf("got %+v", adapters)
	}

	writeFile(t, path, `{"broken": {}}`)
	if _, err := LoadAdapters(path); err == nil || !strings.Contains(err.Error(), "broken") {
		t.Fatalf("empty definition: %v", err)
	}
}

func TestPrepareGenericAdapters(t *testing.T) {
	ws := t.TempDir()
	user := map[string]Adapter{
		"codelldb": {Command: []string{"codelldb", "--port", "{port}"}, Env: map[string]string{"RUST_LOG": "debug"}},
		"node":     {Command: []string{"./tools/js-debug", "{address}"}, Cwd: "tools"},
		// A user definition replaces the built-in Delve setup.
		"go": {Address: "127.0.0.1:2345"},
	}

	for name, tc := range map[string]struct {
		cfg       Config
		transport Transport
		command   []string
		address   string
	}{
		"listen":   {Config{"type": "codelldb"}, Listen, []string{"codelldb", "--port", "{port}"}, ""},
		"reverse":  {Config{"type": "node"}, Reverse, []string{filepath.Join(ws, "tools/js-debug"), "{address}"}, ""},
		"override": {Config{"type": "go"}, Dial, nil, "127.0.0.1:2345"},
		"builtin":  {Config{"type": "lldb-dap"}, Stdio, []string{"lldb-dap"}, ""},
		"config": {Config{"type": "anything", "debugAdapter": map[string]any{"command": []any{"my-adapter", "--stdio"}}},
			Stdio, []string{"my-adapter", "--stdio"}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			tc.cfg["name"], tc.cfg["request"] = name, "launch"
			plan, err := Prepare(tc.cfg, Vars{Workspace: ws, Adapters: user})
			if err != nil {
				t.Fatal(err)
			}
			if plan.Transport != tc.transport || !reflect.DeepEqual(plan.Command, tc.command) || plan.Address != tc.address {
				t.Fatalf("got %v %v %q", plan.Transport, plan.Command, plan.Address)
			}
			if plan.AdapterID != tc.cfg.String("type") {
				t.Errorf("adapter ID %q", plan.AdapterID)
			}
			if _, ok := plan.Arguments["debugAdapter"]; ok {
				t.Error("debugAdapter passed to the adapter")
			}
		})
	}

	plan, _ := Prepare(Config{"name": "e", "type": "codelldb", "request": "launch"}, Vars{Workspace: ws, Adapters: user})
	if !reflect.DeepEqual(plan.Env, []string{"RUST_LOG=debug"}) || plan.Dir != ws {
		t.Errorf("env %v dir %s", plan.Env, plan.Dir)
	}
	plan, _ = Prepare(Config{"name": "c", "type": "node", "request": "launch"}, Vars{Workspace: ws, Adapters: user})
	if plan.Dir != filepath.Join(ws, "tools") {
		t.Errorf("cwd %s", plan.Dir)
	}
}
