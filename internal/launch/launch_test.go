package launch

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestStripJSONC(t *testing.T) {
	in := `{
  // line comment with "quotes"
  "a": "http://not-a-comment", /* block
  comment */ "b": [1, 2,],
  "c": "escaped \" // still string",
}`
	want := "{\n  \n  \"a\": \"http://not-a-comment\",  \"b\": [1, 2],\n  \"c\": \"escaped \\\" // still string\"\n}"

	if got := string(StripJSONC([]byte(in))); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadWorkspace(t *testing.T) {
	dir := t.TempDir()
	content := `{"configurations": [{"name": "a", "type": "go", "request": "launch"},]}`

	writeFile(t, filepath.Join(dir, ".vscode", "launch.json"), content)
	f, err := Load(filepath.Join(dir, ".vscode", "launch.json"))
	if err != nil {
		t.Fatal(err)
	}
	if f.Workspace != dir {
		t.Fatalf("workspace %s, want %s", f.Workspace, dir)
	}

	writeFile(t, filepath.Join(dir, "other", "debug.json"), content)
	f, err = Load(filepath.Join(dir, "other", "debug.json"))
	if err != nil {
		t.Fatal(err)
	}
	if f.Workspace != filepath.Join(dir, "other") {
		t.Fatalf("workspace %s", f.Workspace)
	}
}

func TestLoadRejectsInvalidConfigurations(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"empty":      `{"configurations": []}`,
		"no name":    `{"configurations": [{"type": "go", "request": "launch"}]}`,
		"no type":    `{"configurations": [{"name": "a", "request": "launch"}]}`,
		"bad method": `{"configurations": [{"name": "a", "type": "go", "request": "run"}]}`,
		"bad json":   `{"configurations": [}`,
	} {
		path := filepath.Join(dir, name+".json")
		writeFile(t, path, content)
		if _, err := Load(path); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestSubstitute(t *testing.T) {
	v := Vars{
		Workspace: "/ws/project",
		File:      "/ws/project/pkg/main_test.go",
		Cwd:       "/cwd",
		Home:      "/home/me",
		Env:       func(k string) string { return map[string]string{"TOKEN": "secret"}[k] },
	}

	got, err := v.Substitute(map[string]any{
		"program": "${fileDirname}",
		"args":    []any{"${fileBasenameNoExtension}", "${relativeFile}", "${env:TOKEN}", "${env:MISSING}"},
		"cwd":     "${workspaceFolder}/sub",
		"name":    "${workspaceFolderBasename} ${userHome} ${cwd}",
		"port":    5678.0,
	})
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]any{
		"program": "/ws/project/pkg",
		"args":    []any{"main_test", "pkg/main_test.go", "secret", ""},
		"cwd":     "/ws/project/sub",
		"name":    "project /home/me /cwd",
		"port":    5678.0,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
}

func TestSubstituteErrors(t *testing.T) {
	v := Vars{Workspace: "/ws"}

	if _, err := v.Substitute("${input:pickPort}"); !errors.Is(err, ErrUnsupportedVariable) {
		t.Errorf("input variable: %v", err)
	}

	if _, err := v.Substitute("${command:pickProcess}"); !errors.Is(err, ErrUnsupportedVariable) {
		t.Errorf("command variable: %v", err)
	}

	if _, err := v.Substitute("${file}"); err == nil {
		t.Error("${file} without an open file should fail")
	}
}

func stubDebugpy(t *testing.T, available func(string) bool) {
	t.Helper()
	old := HasDebugpy
	HasDebugpy = available
	t.Cleanup(func() { HasDebugpy = old })
}

func TestPreparePythonLaunch(t *testing.T) {
	ws := t.TempDir()
	writeFile(t, filepath.Join(ws, ".venv", "bin", "python"), "")
	venv := filepath.Join(ws, ".venv", "bin", "python")
	stubDebugpy(t, func(p string) bool { return p == venv })

	plan, err := Prepare(Config{
		"name": "app", "type": "debugpy", "request": "launch",
		"program": "${workspaceFolder}/app.py", "console": "integratedTerminal",
		"preLaunchTask": "build",
	}, Vars{Workspace: ws})
	if err != nil {
		t.Fatal(err)
	}

	if plan.Transport != Stdio || !reflect.DeepEqual(plan.Command, []string{venv, "-m", "debugpy.adapter"}) {
		t.Fatalf("adapter %v %v", plan.Transport, plan.Command)
	}

	args := plan.Arguments
	if args["console"] != "integratedTerminal" || args["redirectOutput"] != nil || args["cwd"] != ws || args["python"] != venv {
		t.Fatalf("arguments %#v", args)
	}
	if args["program"] != filepath.Join(ws, "app.py") {
		t.Fatalf("program %v", args["program"])
	}

	if _, ok := args["preLaunchTask"]; ok {
		t.Error("client-only key passed to adapter")
	}
	if plan.PreLaunchTask != "build" {
		t.Errorf("preLaunchTask %q", plan.PreLaunchTask)
	}
}

func TestPreparePythonAttach(t *testing.T) {
	stubDebugpy(t, func(string) bool { return false })

	plan, err := Prepare(Config{
		"name": "remote", "type": "python", "request": "attach",
		"connect":      map[string]any{"host": "10.0.0.5", "port": 5678.0},
		"pathMappings": []any{map[string]any{"localRoot": "${workspaceFolder}", "remoteRoot": "/app"}},
	}, Vars{Workspace: "/ws"})
	if err != nil {
		t.Fatal(err)
	}

	if plan.Transport != Dial || plan.Address != "10.0.0.5:5678" {
		t.Fatalf("got %v %s", plan.Transport, plan.Address)
	}
	mapping := plan.Arguments["pathMappings"].([]any)[0].(map[string]any)
	if mapping["localRoot"] != "/ws" {
		t.Fatalf("mapping %v", mapping)
	}

	plan, err = Prepare(Config{"name": "legacy", "type": "python", "request": "attach", "port": 1234.0}, Vars{Workspace: "/ws"})
	if err != nil || plan.Address != "127.0.0.1:1234" {
		t.Fatalf("legacy host/port: %v %v", plan, err)
	}

	if _, err := Prepare(Config{"name": "bad", "type": "python", "request": "attach"}, Vars{Workspace: "/ws"}); err == nil {
		t.Fatal("attach without an endpoint should fail")
	}
}

func TestPrepareGo(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "cmd", "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(ws, ".env"), "# comment\nexport A=1\nB=\"two\"\n")

	plan, err := Prepare(Config{
		"name": "app", "type": "go", "request": "launch", "mode": "auto",
		"program": "${workspaceFolder}/cmd/app", "dlvToolPath": "/opt/dlv",
		"dlvFlags": []any{"--log"}, "envFile": "${workspaceFolder}/.env",
		"env": map[string]any{"B": "override"},
	}, Vars{Workspace: ws})
	if err != nil {
		t.Fatal(err)
	}

	if plan.Transport != Reverse || !reflect.DeepEqual(plan.Command, []string{"/opt/dlv", "dap", "--client-addr={address}", "--log"}) {
		t.Fatalf("adapter %v %v", plan.Transport, plan.Command)
	}
	if plan.Dir != filepath.Join(ws, "cmd", "app") || plan.Arguments["mode"] != "debug" {
		t.Fatalf("dir %s mode %v", plan.Dir, plan.Arguments["mode"])
	}
	if env := plan.Arguments["env"].(map[string]any); env["A"] != "1" || env["B"] != "override" {
		t.Fatalf("env %v", env)
	}
	if _, ok := plan.Arguments["envFile"]; ok {
		t.Error("envFile passed to adapter")
	}

	plan, err = Prepare(Config{"name": "t", "type": "go", "request": "launch", "program": "/x/a_test.go", "dlvToolPath": "/opt/dlv"}, Vars{Workspace: ws})
	if err != nil || plan.Arguments["mode"] != "test" {
		t.Fatalf("test mode: %v %v", plan, err)
	}

	plan, err = Prepare(Config{"name": "r", "type": "go", "request": "attach", "mode": "remote", "port": 2345.0}, Vars{Workspace: ws})
	if err != nil || plan.Transport != Dial || plan.Address != "127.0.0.1:2345" {
		t.Fatalf("remote attach: %v %v", plan, err)
	}

	plan, err = Prepare(Config{"name": "l", "type": "go", "request": "attach", "processId": "42", "dlvToolPath": "/opt/dlv"}, Vars{Workspace: ws})
	if err != nil || plan.Transport != Reverse || plan.Arguments["processId"] != 42 || plan.Arguments["mode"] != "local" {
		t.Fatalf("local attach: %v %v", plan, err)
	}
}

func TestPrepareDebugServerAndUnknownTypes(t *testing.T) {
	plan, err := Prepare(Config{"name": "n", "type": "node", "request": "launch", "debugServer": 4711.0}, Vars{Workspace: "/ws"})
	if err != nil || plan.Transport != Dial || plan.Address != "127.0.0.1:4711" {
		t.Fatalf("debugServer: %v %v", plan, err)
	}

	if _, err := Prepare(Config{"name": "n", "type": "node", "request": "launch"}, Vars{Workspace: "/ws"}); err == nil {
		t.Fatal("unknown type without an adapter should fail")
	}
}

func TestPreparePlatformOverrides(t *testing.T) {
	platform := map[string]string{"darwin": "osx", "linux": "linux", "windows": "windows"}[runtime.GOOS]

	plan, err := Prepare(Config{
		"name": "o", "type": "go", "request": "attach", "mode": "remote", "port": 1.0,
		platform: map[string]any{"port": 2.0},
	}, Vars{Workspace: "/ws"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Address != "127.0.0.1:2" {
		t.Fatalf("override not applied: %s", plan.Address)
	}
}

func TestLoadCompounds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "launch.json")
	writeFile(t, path, `{
  "configurations": [
    {"name": "server", "type": "go", "request": "launch"},
    {"name": "client", "type": "debugpy", "request": "launch"}
  ],
  "compounds": [
    {"name": "both", "configurations": ["server", {"name": "client", "folder": "app"}], "stopAll": true, "preLaunchTask": "build"}
  ]
}`)
	f, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	c := f.Compounds[0]
	if !reflect.DeepEqual(c.Members(), []string{"server", "client"}) || !c.StopAll || c.PreLaunchTask != "build" {
		t.Fatalf("compound %+v", c)
	}

	writeFile(t, path, `{"configurations": [{"name": "a", "type": "go", "request": "launch"}], "compounds": [{"name": "x", "configurations": ["missing"]}]}`)
	if _, err := Load(path); err == nil {
		t.Fatal("a compound naming a missing configuration should fail")
	}
}
