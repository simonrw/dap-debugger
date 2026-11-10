package launch

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const tasksJSON = `{
  "version": "2.0.0",
  "options": {"cwd": "${workspaceFolder}/default"},
  "tasks": [
    {"label": "build", "type": "shell", "command": "go build", "args": ["-o", "my app", {"value": "./...", "quoting": "escape"}], "dependsOn": ["gen", "lint"]},
    {"label": "gen", "type": "process", "command": "go", "args": ["generate"], "options": {"cwd": "${workspaceFolder}/gen", "env": {"MODE": "${env:MODE}"}}},
    {"label": "lint", "type": "process", "command": "vet"},
    {"label": "all", "dependsOn": "build"},
    {"label": "watch", "type": "shell", "command": "air", "isBackground": true},
    {"label": "ant", "type": "ant", "target": "build"},
    {"label": "loop", "dependsOn": "loop2"},
    {"label": "loop2", "dependsOn": "loop"},
  ]
}`

func TestResolveTask(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.json")
	writeFile(t, path, tasksJSON)
	t.Setenv("SHELL", "/bin/sh")
	vars := Vars{Workspace: "/ws", Env: func(string) string { return "fast" }}

	cmds, err := ResolveTask(path, "all", vars)
	if err != nil {
		t.Fatal(err)
	}

	want := []TaskCommand{
		{Label: "gen", Args: []string{"go", "generate"}, Dir: "/ws/gen", Env: []string{"MODE=fast"}},
		{Label: "lint", Args: []string{"vet"}, Dir: "/ws/default"},
		{Label: "build", Args: []string{"/bin/sh", "-c", "go build -o 'my app' ./..."}, Dir: "/ws/default"},
	}
	if !reflect.DeepEqual(cmds, want) {
		t.Fatalf("got %#v\nwant %#v", cmds, want)
	}
}

func TestResolveTaskErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.json")
	writeFile(t, path, tasksJSON)

	for label, want := range map[string]string{
		"missing": "no task labelled",
		"watch":   "problemMatcher",
		"ant":     "not supported",
		"loop":    "depends on itself",
	} {
		_, err := ResolveTask(path, label, Vars{Workspace: "/ws"})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", label, err, want)
		}
	}
}

func TestResolveProvidedAndBackgroundTasks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.json")
	writeFile(t, path, `{
  "version": "2.0.0",
  "tasks": [
    {"label": "npm: build", "type": "npm", "script": "build", "path": "web"},
    {"label": "tsc: watch", "type": "typescript", "tsconfig": "app/tsconfig.json", "option": "watch"},
    {"label": "go: test", "type": "go", "command": "test", "args": ["./..."]},
    {"label": "serve", "type": "shell", "command": "make serve", "isBackground": true,
     "problemMatcher": {"owner": "x", "pattern": {"regexp": "^x$"}, "background": {"beginsPattern": "^start", "endsPattern": "^listening on \\d+"}}},
    {"label": "named", "type": "shell", "command": "tsc -w", "isBackground": true, "problemMatcher": ["$tsc-watch"]},
    {"label": "esbuild", "type": "npm", "script": "watch", "isBackground": true, "problemMatcher": "$esbuild-watch"},
    {"label": "custom", "type": "shell", "command": "serve", "isBackground": true, "problemMatcher": "$my-server"},
    {"label": "unknown", "type": "shell", "command": "serve", "isBackground": true, "problemMatcher": "$nobody-knows"},
    {"label": "gulp: build", "type": "gulp", "task": "build"},
    {"label": "cargo build", "type": "cargo", "command": "build", "args": ["--release"]}
  ]
}`)
	t.Setenv("SHELL", "/bin/sh")
	vars := Vars{Workspace: "/ws", Matchers: ProblemMatchers{"my-server": `^ready in \d+ms`}}

	if _, err := ResolveTask(path, "unknown", vars); err == nil || !strings.Contains(err.Error(), "problemMatchers.json") {
		t.Errorf("unknown named matcher: %v", err)
	}

	for label, want := range map[string]TaskCommand{
		"gulp: build": {Label: "gulp: build", Args: []string{"gulp", "build"}, Dir: "/ws"},
		"cargo build": {Label: "cargo build", Args: []string{"cargo", "build", "--release"}, Dir: "/ws"},
		"npm: build":  {Label: "npm: build", Args: []string{"npm", "run", "build"}, Dir: "/ws/web"},
		"go: test":    {Label: "go: test", Args: []string{"go", "test", "./..."}, Dir: "/ws"},
	} {
		cmds, err := ResolveTask(path, label, vars)
		if err != nil || len(cmds) != 1 || !reflect.DeepEqual(cmds[0], want) {
			t.Errorf("%s: got %#v %v", label, cmds, err)
		}
	}

	for label, ready := range map[string]string{
		"tsc: watch": "Found 0 errors. Watching for file changes.",
		"serve":      "listening on 8080",
		"named":      "Found 2 errors. Watching for file changes.",
		"esbuild":    "[watch] build finished, watching for changes...",
		"custom":     "ready in 120ms",
	} {
		cmds, err := ResolveTask(path, label, vars)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if cmds[0].Ready == nil || !cmds[0].Ready.MatchString(ready) || cmds[0].Ready.MatchString("compiling") {
			t.Errorf("%s: ready pattern %v", label, cmds[0].Ready)
		}
	}

	cmds, _ := ResolveTask(path, "tsc: watch", vars)
	if !reflect.DeepEqual(cmds[0].Args, []string{"tsc", "-p", "app/tsconfig.json", "--watch"}) {
		t.Errorf("tsc args %v", cmds[0].Args)
	}
}

func TestLoadProblemMatchers(t *testing.T) {
	dir := t.TempDir()
	if m, err := LoadProblemMatchers(filepath.Join(dir, "missing.json")); err != nil || m != nil {
		t.Fatalf("missing: %v %v", m, err)
	}
	path := filepath.Join(dir, "problemMatchers.json")
	writeFile(t, path, `{"$vite": "ready in \\d+ ms", /* comment */ }`)
	m, err := LoadProblemMatchers(path)
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := m.lookup("$vite"); !ok || p != `ready in \d+ ms` {
		t.Fatalf("lookup %q %v", p, ok)
	}
	writeFile(t, path, `{"bad": "("}`)
	if _, err := LoadProblemMatchers(path); err == nil {
		t.Fatal("an invalid pattern should fail")
	}
}
