package ui

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	godap "github.com/google/go-dap"

	"github.com/simonrw/dap-debugger/internal/daptest"
	"github.com/simonrw/dap-debugger/internal/launch"
	"github.com/simonrw/dap-debugger/internal/session"
	"github.com/simonrw/dap-debugger/internal/state"
)

// driver runs a Model the way Bubble Tea does: commands run in goroutines and
// their messages are fed back through Update.
type driver struct {
	t    *testing.T
	m    *Model
	msgs chan tea.Msg
}

func newDriver(t *testing.T, m *Model) *driver {
	d := &driver{t: t, m: m, msgs: make(chan tea.Msg, 1024)}
	d.send(tea.WindowSizeMsg{Width: 140, Height: 40})
	return d
}

func (d *driver) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() { d.msgs <- cmd() }()
}

func (d *driver) send(msg tea.Msg) {
	switch msg := msg.(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range msg {
			d.run(c)
		}
	default:
		_, cmd := d.m.Update(msg)
		d.run(cmd)
	}
}

// key sends a key press written the way Bubble Tea prints it, such as "f5" or "shift+f11".
func (d *driver) key(keys ...string) {
	for _, k := range keys {
		d.send(keyPress(k))
		d.settle()
	}
}

func keyPress(k string) tea.KeyPressMsg {
	codes := map[string]rune{
		"enter": tea.KeyEnter, "tab": tea.KeyTab, "esc": tea.KeyEscape, "space": tea.KeySpace,
		"up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight,
		"f1": tea.KeyF1, "f5": tea.KeyF5, "f6": tea.KeyF6, "f7": tea.KeyF7, "f8": tea.KeyF8,
		"f9": tea.KeyF9, "f11": tea.KeyF11,
	}
	var mod tea.KeyMod
	parts := strings.Split(k, "+")
	for _, p := range parts[:len(parts)-1] {
		switch p {
		case "ctrl":
			mod |= tea.ModCtrl
		case "shift":
			mod |= tea.ModShift
		}
	}
	last := parts[len(parts)-1]
	if code, ok := codes[last]; ok {
		return tea.KeyPressMsg{Code: code, Mod: mod}
	}
	r := []rune(last)[0]
	text := last
	if mod != 0 {
		text = ""
	}
	return tea.KeyPressMsg{Code: r, Text: text, Mod: mod}
}

func (d *driver) typeText(s string) {
	for _, r := range s {
		d.send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	d.settle()
}

// settle processes messages that arrive within a short quiet period.
func (d *driver) settle() {
	for {
		select {
		case msg := <-d.msgs:
			d.send(msg)
		case <-time.After(20 * time.Millisecond):
			return
		}
	}
}

func (d *driver) until(what string, cond func() bool) {
	d.t.Helper()
	deadline := time.After(90 * time.Second)
	for !cond() {
		select {
		case msg := <-d.msgs:
			d.send(msg)
		case <-deadline:
			d.t.Fatalf("timed out waiting for %s\n%s", what, d.screen())
		}
	}
}

func (d *driver) screen() string {
	return ansi.Strip(d.m.render())
}

// programOutput is everything the program printed, in the console and terminal.
func (d *driver) programOutput() string {
	var b strings.Builder
	for _, l := range d.m.console.lines {
		b.WriteString(l.text + "\n")
	}
	if d.m.term != nil {
		b.WriteString(d.m.term.transcript.String())
	}
	return b.String()
}

func examplesDir(t *testing.T) string {
	_, file, _, _ := runtime.Caller(0)
	dir, err := filepath.Abs(filepath.Join(filepath.Dir(file), "..", "..", "examples"))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func newModel(t *testing.T, workspace string, configs []launch.Config, statePath string) *Model {
	t.Helper()
	store, err := state.Open(statePath, workspace)
	if err != nil {
		t.Fatal(err)
	}
	file := &launch.File{Path: filepath.Join(workspace, ".vscode", "launch.json"), Workspace: workspace, Configurations: configs}
	return New(Options{File: file, Store: store})
}

func tempWorkspace(t *testing.T) string {
	ws := t.TempDir()
	for name, content := range map[string]string{
		"main.py":        "def main():\n    x = 1\n    return x\n\nmain()\n",
		"pkg/helpers.py": "VALUE = 1\n",
	} {
		path := filepath.Join(ws, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return ws
}

var pythonConfig = launch.Config{"name": "Python", "type": "debugpy", "request": "launch", "program": "${file}"}

func TestBrowseAndToggleBreakpointsBeforeStarting(t *testing.T) {
	ws := tempWorkspace(t)
	statePath := filepath.Join(t.TempDir(), "state.json")
	d := newDriver(t, newModel(t, ws, []launch.Config{pythonConfig}, statePath))

	// Explorer lists folders first; open main.py from the tree.
	d.key("down", "enter")
	if d.m.editor.path != filepath.Join(ws, "main.py") || d.m.focus != paneEditor {
		t.Fatalf("main.py not opened: %q focus %v", d.m.editor.path, d.m.focus)
	}

	d.key("down", "b")
	d.key("down", "b", "c")
	d.typeText("x > 0")
	d.key("enter")
	if !strings.Contains(d.screen(), "◉") || !strings.Contains(d.screen(), "●") {
		t.Fatalf("gutter markers missing:\n%s", d.screen())
	}

	// Breakpoints survive a restart of the debugger.
	m := newModel(t, ws, []launch.Config{pythonConfig}, statePath)
	got := m.store.BreakpointsIn(filepath.Join(ws, "main.py"))
	if len(got) != 2 || got[0].Line != 2 || got[1].Line != 3 || got[1].Condition != "x > 0" {
		t.Fatalf("breakpoints after reload: %+v", got)
	}
	if m.editor.path != filepath.Join(ws, "main.py") {
		t.Fatalf("last file not reopened: %q", m.editor.path)
	}

	// Disable from the breakpoints pane, then remove.
	d.key("ctrl+r")
	for d.m.focus != paneBreakpoints {
		d.key("ctrl+j")
	}
	d.key("space")
	if d.m.store.Breakpoints[0].Enabled {
		t.Fatal("space did not disable the breakpoint")
	}
	d.key("d")
	if len(d.m.store.Breakpoints) != 1 {
		t.Fatalf("d did not remove: %+v", d.m.store.Breakpoints)
	}
}

func TestQuickOpenAndWatchExpressions(t *testing.T) {
	ws := tempWorkspace(t)
	d := newDriver(t, newModel(t, ws, []launch.Config{pythonConfig}, filepath.Join(t.TempDir(), "s.json")))

	d.key("ctrl+p")
	d.until("quick open", func() bool { return d.m.modal != nil })
	d.typeText("helpers")
	d.key("enter")
	if d.m.editor.path != filepath.Join(ws, "pkg", "helpers.py") {
		t.Fatalf("quick open opened %q", d.m.editor.path)
	}

	d.key("ctrl+r")
	d.key("ctrl+j")
	if d.m.focus != paneWatch {
		t.Fatalf("focus %v", d.m.focus)
	}
	d.key("a")
	d.typeText("len(items)")
	d.key("enter")
	if len(d.m.store.Watch) != 1 || !strings.Contains(d.screen(), "len(items)") {
		t.Fatalf("watch not added:\n%s", d.screen())
	}
}

func TestRendersAtSmallSizes(t *testing.T) {
	ws := tempWorkspace(t)
	m := newModel(t, ws, []launch.Config{pythonConfig}, filepath.Join(t.TempDir(), "s.json"))
	for _, view := range []sidebarView{viewExplorer, viewDebug} {
		m.view = view
		for _, size := range [][2]int{{10, 4}, {20, 8}, {80, 10}, {59, 12}, {80, 24}, {200, 60}} {
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			out := m.render()
			if lines := strings.Count(out, "\n") + 1; lines != size[1] {
				t.Errorf("view %d at %dx%d rendered %d lines", view, size[0], size[1], lines)
			}
		}
	}
}

// TestDebugGoProgram drives a real Delve session with the user's VS Code keys.
func TestDebugGoProgram(t *testing.T) {
	daptest.UseAdapter(t, "go")
	ws := examplesDir(t)
	main := filepath.Join(ws, "go", "main.go")
	cfg := launch.Config{"name": "Go", "type": "go", "request": "launch", "program": "${workspaceFolder}/go"}
	d := newDriver(t, newModel(t, ws, []launch.Config{cfg}, filepath.Join(t.TempDir(), "s.json")))

	d.m.openFile(main, 10)
	d.m.setFocus(paneEditor)
	d.key("b")

	d.key("f5")
	d.until("breakpoint hit", func() bool { return d.m.status == statusStopped && d.m.execLoc.line == 10 })
	d.until("locals", func() bool { return strings.Contains(d.screen(), `message: "Hello, debugger 0!"`) })

	d.key("f8")
	d.until("step over", func() bool { return d.m.status == statusStopped && d.m.execLoc.line == 11 })

	d.key("shift+f11")
	d.until("step out", func() bool { return d.m.status == statusStopped && d.m.execLoc.line == 16 })

	d.key("f9")
	d.until("breakpoint hit again", func() bool {
		return d.m.status == statusStopped && strings.Contains(d.screen(), `"debugger 1"`)
	})

	d.key("shift+f5")
	d.until("session end", func() bool { return d.m.sess == nil })
}

// TestDebugPythonProgram drives debugpy, including ${file}, step into and the console.
func TestDebugPythonProgram(t *testing.T) {
	ws := examplesDir(t)
	hello := filepath.Join(ws, "hello.py")
	daptest.UseAdapter(t, "debugpy")
	d := newDriver(t, newModel(t, ws, []launch.Config{pythonConfig}, filepath.Join(t.TempDir(), "s.json")))

	d.m.openFile(hello, 13)
	d.m.setFocus(paneEditor)
	d.key("b")

	d.key("f5")
	d.until("breakpoint hit", func() bool { return d.m.status == statusStopped && d.m.execLoc.line == 13 })

	d.key("f7")
	d.until("step into greet", func() bool { return d.m.status == statusStopped && d.m.execLoc.line == 7 })

	// Add the local to Watch, then change its value from Variables.
	d.until("locals", func() bool { return strings.Contains(d.screen(), "name: 'debugger 0'") })
	d.key("ctrl+h", "down", "w")
	d.until("watch added", func() bool { return len(d.m.store.Watch) == 1 && d.m.store.Watch[0] == "name" })
	d.key("e", "ctrl+u")
	d.typeText("'changed'")
	d.key("enter")
	d.until("value set", func() bool { return strings.Contains(d.screen(), "name: 'changed'") })

	d.key("ctrl+l", "ctrl+j")
	d.typeText("name.upper()")
	d.key("enter")
	d.until("console result", func() bool { return strings.Contains(d.screen(), "'CHANGED'") })

	d.key("ctrl+k", "ctrl+g")
	d.typeText("13")
	d.key("enter", "b")
	d.until("breakpoint removed", func() bool { return len(d.m.store.Breakpoints) == 0 })
	d.key("f9")
	d.until("program end", func() bool { return d.m.sess == nil })
	if out := d.programOutput(); !strings.Contains(out, "Hello, changed!") || !strings.Contains(out, "Hello, debugger 4!") {
		t.Fatalf("program output missing:\n%s", out)
	}
}

// TestFunctionBreakpointsAndLogpoints uses VS Code's other breakpoint kinds with Delve.
func TestFunctionBreakpointsAndLogpoints(t *testing.T) {
	daptest.UseAdapter(t, "go")
	ws := examplesDir(t)
	main := filepath.Join(ws, "go", "main.go")
	cfg := launch.Config{"name": "Go", "type": "go", "request": "launch", "program": "${workspaceFolder}/go"}
	d := newDriver(t, newModel(t, ws, []launch.Config{cfg}, filepath.Join(t.TempDir(), "s.json")))

	d.m.openFile(main, 17)
	d.m.setFocus(paneEditor)
	d.key("p")
	d.typeText("logged {greeting}")
	d.key("enter")

	d.key("ctrl+r")
	for d.m.focus != paneBreakpoints {
		d.key("ctrl+j")
	}
	d.key("f")
	d.typeText("main.greet")
	d.key("enter")

	// The list is the function breakpoint then the logpoint; the hit count
	// makes the logpoint fire on the third pass only.
	d.key("down", "H")
	d.typeText("3")
	d.key("enter")
	if fns := d.m.store.Functions; len(fns) != 1 || fns[0].Name != "main.greet" {
		t.Fatalf("function breakpoints %+v", fns)
	}

	d.key("f5")
	d.until("function breakpoint hit", func() bool {
		return d.m.status == statusStopped && d.m.execLoc.line >= 8 && d.m.execLoc.line <= 9
	})
	d.until("locals", func() bool { return strings.Contains(d.screen(), `name: "debugger 0"`) })

	// Disable the function breakpoint so only the logpoint remains.
	d.key("ctrl+r")
	for d.m.focus != paneBreakpoints {
		d.key("ctrl+j")
	}
	d.m.lists[paneBreakpoints].cursor = 2
	d.key("space")
	d.key("f9")
	d.until("program end", func() bool { return d.m.sess == nil })

	var output strings.Builder
	for _, l := range d.m.console.lines {
		output.WriteString(l.text + "\n")
	}
	if strings.Count(output.String(), "logged") != 1 || !strings.Contains(output.String(), `logged "Hello, debugger 2!"`) {
		t.Fatalf("logpoint should fire once, on the third pass:\n%s", output.String())
	}
}

// TestProgramInput answers a prompt from a program started in an integrated terminal.
func TestProgramInput(t *testing.T) {
	ws := examplesDir(t)
	echo, _ := filepath.Abs(filepath.Join("..", "session", "testdata", "echo.py"))
	cfg := launch.Config{"name": "Echo", "type": "debugpy", "request": "launch", "program": echo, "console": "integratedTerminal"}
	daptest.UseAdapter(t, "debugpy")
	d := newDriver(t, newModel(t, ws, []launch.Config{cfg}, filepath.Join(t.TempDir(), "s.json")))

	d.key("f5")
	d.until("prompt", func() bool { return strings.Contains(d.screen(), "name? ") })
	if d.m.panel != panelTerminal || !strings.Contains(d.screen(), "TERMINAL") {
		t.Fatalf("terminal not shown:\n%s", d.screen())
	}

	// Keys typed in the focused terminal go to the program.
	d.key("ctrl+l", "ctrl+j")
	d.typeText("gopher")
	d.key("enter")
	d.until("password prompt", func() bool { return strings.Contains(d.screen(), "secret? ") })
	d.typeText("hunter")
	d.key("enter")
	d.until("program end", func() bool { return d.m.sess == nil })

	out := d.programOutput()
	if !strings.Contains(out, "hello gopher") || !strings.Contains(out, "secret has 6 characters, tty: True") {
		t.Fatalf("program did not get its input:\n%s", out)
	}
	if strings.Contains(out, "hunter") {
		t.Fatalf("password was shown:\n%s", out)
	}

	// ctrl+t switches back to the Debug Console.
	d.key("ctrl+t")
	if d.m.panel != panelConsole {
		t.Fatal("ctrl+t did not switch to the Debug Console")
	}
}

// TestPreLaunchAndPostDebugTasks runs tasks from tasks.json around a session,
// and a failing preLaunchTask stops the launch.
func TestPreLaunchAndPostDebugTasks(t *testing.T) {
	ws := tempWorkspace(t)
	marker := filepath.Join(ws, "built")
	tasks := `{
  "version": "2.0.0",
  "tasks": [
    {"label": "build", "type": "shell", "command": "echo building && touch built"},
    {"label": "cleanup", "type": "process", "command": "echo", "args": ["cleaned up"]},
    {"label": "broken", "type": "shell", "command": "echo failing; exit 3"}
  ]
}`
	if err := os.MkdirAll(filepath.Join(ws, ".vscode"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".vscode", "tasks.json"), []byte(tasks), 0o644); err != nil {
		t.Fatal(err)
	}

	program := filepath.Join(ws, "main.py")
	good := launch.Config{"name": "Good", "type": "debugpy", "request": "launch", "program": program,
		"preLaunchTask": "build", "postDebugTask": "cleanup"}
	bad := launch.Config{"name": "Bad", "type": "debugpy", "request": "launch", "program": program, "preLaunchTask": "broken"}
	daptest.UseAdapter(t, "debugpy")

	d := newDriver(t, newModel(t, ws, []launch.Config{bad, good}, filepath.Join(t.TempDir(), "s.json")))
	console := func() string {
		var b strings.Builder
		for _, l := range d.m.console.lines {
			b.WriteString(l.text + "\n")
		}
		return b.String()
	}

	d.key("f5")
	d.until("failed task", func() bool { return strings.Contains(console(), "exit status 3") })
	if d.m.sess != nil || d.m.status != statusInactive {
		t.Fatalf("session started despite failed task: %v", d.m.status)
	}

	d.m.configIndex = 1
	d.key("f5")
	d.until("post debug task", func() bool { return strings.Contains(console(), "cleaned up") })
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("preLaunchTask did not run: %v\n%s", err, console())
	}
	if out := console(); strings.Index(out, "building") > strings.Index(out, `Debug session "Good" ended`) {
		t.Fatalf("tasks ran in the wrong order:\n%s", out)
	}
}

// TestDataBreakpoint breaks when a variable changes, with Delve.
func TestDataBreakpoint(t *testing.T) {
	daptest.UseAdapter(t, "go")
	ws := examplesDir(t)
	main := filepath.Join(ws, "go", "main.go")
	cfg := launch.Config{"name": "Go", "type": "go", "request": "launch", "program": "${workspaceFolder}/go"}
	d := newDriver(t, newModel(t, ws, []launch.Config{cfg}, filepath.Join(t.TempDir(), "s.json")))

	d.m.openFile(main, 16)
	d.m.setFocus(paneEditor)
	d.key("b", "f5")
	d.until("breakpoint hit", func() bool { return d.m.status == statusStopped && d.m.execLoc.line == 16 })
	d.until("locals", func() bool { return strings.Contains(d.screen(), "index: 0") })

	d.key("b", "ctrl+h", "down", "v")
	d.until("data breakpoint verified", func() bool { return len(d.m.dataBps) == 1 && d.m.dataBps[0].verified })
	if !strings.Contains(d.screen(), "on change") {
		t.Fatalf("data breakpoint not listed:\n%s", d.screen())
	}

	d.key("f9")
	// Delve stops inside the loop's post statement, where the variable is out
	// of scope, so step back into the loop body to see the new value.
	d.until("value change", func() bool { return d.m.status == statusStopped && d.m.stopReason == "data breakpoint" })
	d.key("f8")
	d.until("new value", func() bool { return d.m.status == statusStopped && strings.Contains(d.screen(), "index: 1") })

	d.key("shift+f5")
	d.until("session end", func() bool { return d.m.sess == nil })
	if len(d.m.dataBps) != 0 {
		t.Fatal("data breakpoints should not outlive the session")
	}
}

func TestRelPath(t *testing.T) {
	ws := "/ws/project"
	for path, want := range map[string]string{
		"/ws/project/main.go":        "main.go",
		"/ws/project/.vscode/a.json": ".vscode/a.json",
		"/ws/project/..hidden/x":     "..hidden/x",
		"/ws/other/main.go":          "/ws/other/main.go",
	} {
		if got := relPath(ws, path); got != want {
			t.Errorf("relPath(%q) = %q, want %q", path, got, want)
		}
	}
}

// TestMovedBreakpointEditing edits a breakpoint at the line the adapter moved it to.
func TestMovedBreakpointEditing(t *testing.T) {
	ws := tempWorkspace(t)
	m := newModel(t, ws, []launch.Config{pythonConfig}, filepath.Join(t.TempDir(), "s.json"))
	path := filepath.Join(ws, "main.py")
	m.store.Toggle(path, 4)
	m.configured = true
	m.records[path] = map[bpKey]godap.Breakpoint{{4, 0}: {Verified: true, Line: 5}}

	m.toggleEnabledAt(path, 5)
	if m.store.Breakpoints[0].Enabled || len(m.store.Breakpoints) != 1 {
		t.Fatalf("toggling at the moved line: %+v", m.store.Breakpoints)
	}

	m.toggleBreakpointAt(path, 5)
	if len(m.store.Breakpoints) != 0 {
		t.Fatalf("removing at the moved line: %+v", m.store.Breakpoints)
	}
}

// TestRejectedStepStaysPaused returns to the paused state when the adapter
// rejects a step, since no stopped event will follow.
func TestRejectedStepStaysPaused(t *testing.T) {
	ws := tempWorkspace(t)
	m := newModel(t, ws, []launch.Config{pythonConfig}, filepath.Join(t.TempDir(), "s.json"))
	m.sess = session.New(&launch.Plan{Name: "fake"}, session.Options{})
	m.runs = append(m.runs, m.run)
	m.status = statusStopped
	m.threadID = 1
	m.stopReason = "breakpoint"

	cmd := m.execute("stepOut")
	if m.status != statusRunning || cmd == nil {
		t.Fatalf("status %v after step", m.status)
	}

	m.handleDebugMsg(executeErrMsg{sess: m.sess, gen: m.gen, command: "stepOut", thread: 1, reason: "breakpoint", err: errors.New("no frame to step out of")})
	if m.status != statusStopped || m.stopReason != "breakpoint" {
		t.Fatalf("status %v reason %q after rejected step", m.status, m.stopReason)
	}
}

func TestMain(m *testing.M) {
	daptest.Main()
	os.Exit(m.Run())
}

// TestDebugWithAnyAdapter drives a whole session with keys against an adapter
// known only from its definition, showing nothing in the UI is tied to one debugger.
func TestDebugWithAnyAdapter(t *testing.T) {
	ws := tempWorkspace(t)
	program := filepath.Join(ws, "main.py")
	cfg := launch.Config{"name": "Fake", "type": "fake", "request": "launch", "program": program}
	m := newModel(t, ws, []launch.Config{cfg}, filepath.Join(t.TempDir(), "s.json"))
	m.adapters = map[string]launch.Adapter{"fake": {Command: []string{os.Args[0]}, Env: map[string]string{daptest.Env: "stdio"}}}
	d := newDriver(t, m)

	d.m.openFile(program, 2)
	d.m.setFocus(paneEditor)
	d.key("b", "f5")
	d.until("stopped at breakpoint", func() bool { return d.m.status == statusStopped && d.m.execLoc == location{path: program, line: 2} })
	d.until("variables", func() bool { return strings.Contains(d.screen(), "answer: 42") })
	if !strings.Contains(d.screen(), "Raised Exceptions") {
		t.Fatalf("adapter exception filters missing:\n%s", d.screen())
	}

	d.key("ctrl+h", "down", "e", "ctrl+u")
	d.typeText("7")
	d.key("enter")
	d.until("value set", func() bool { return strings.Contains(d.screen(), "answer: 7") })

	d.key("f8")
	d.until("step", func() bool { return d.m.status == statusStopped && d.m.stopReason == "step" })

	d.key("f9")
	d.until("end", func() bool { return d.m.sess == nil })
	var out strings.Builder
	for _, l := range d.m.console.lines {
		out.WriteString(l.text + "\n")
	}
	if !strings.Contains(out.String(), "answer is 7") {
		t.Fatalf("console:\n%s", out.String())
	}
}

func fakeAdapters() map[string]launch.Adapter {
	return map[string]launch.Adapter{"fake": {Command: []string{os.Args[0]}, Env: map[string]string{daptest.Env: "stdio"}}}
}

// TestCompoundAndChildSessions starts a compound of two sessions, one of
// which asks for a child session, then stops them all with stopAll.
func TestCompoundAndChildSessions(t *testing.T) {
	ws := tempWorkspace(t)
	program := filepath.Join(ws, "main.py")
	server := launch.Config{"name": "Server", "type": "fake", "request": "launch", "program": program}
	client := launch.Config{"name": "Client", "type": "fake", "request": "launch", "program": program,
		"child": map[string]any{"type": "fake", "name": "Worker", "program": program}}

	m := newModel(t, ws, []launch.Config{server, client}, filepath.Join(t.TempDir(), "s.json"))
	m.file.Compounds = []*launch.Compound{{Name: "Both", Configurations: []any{"Server", "Client"}, StopAll: true}}
	m.adapters = fakeAdapters()
	d := newDriver(t, m)

	d.m.openFile(program, 2)
	d.m.setFocus(paneEditor)
	d.key("b")
	d.m.configIndex = 2
	if d.m.selectionName() != "Both" {
		t.Fatalf("selection %q", d.m.selectionName())
	}

	d.key("f5")
	d.until("three sessions paused", func() bool {
		if len(d.m.runs) != 3 {
			return false
		}
		for _, r := range d.m.runs {
			if r.status != statusStopped {
				return false
			}
		}
		return true
	})
	screen := d.screen()
	for _, name := range []string{"Server", "Client", "Worker"} {
		if !strings.Contains(screen, name) {
			t.Fatalf("call stack does not show %s:\n%s", name, screen)
		}
	}
	if worker := d.m.runs[2]; worker.parent != d.m.runs[1] {
		t.Fatal("child session has no parent")
	}

	// Select the first session from the call stack, then stop: stopAll ends every session.
	d.key("ctrl+r")
	for d.m.focus != paneStack {
		d.key("ctrl+j")
	}
	d.key("home", "enter")
	if d.m.active.name != "Server" {
		t.Fatalf("active session %q", d.m.active.name)
	}
	d.key("shift+f5")
	d.until("all sessions end", func() bool { return len(d.m.runs) == 0 })
	if d.m.status != statusInactive {
		t.Fatalf("status %v", d.m.status)
	}
}

// TestPythonSubprocessSession follows a Python subprocess into a child session.
func TestPythonSubprocessSession(t *testing.T) {
	ws := examplesDir(t)
	parent, _ := filepath.Abs(filepath.Join("..", "session", "testdata", "parent.py"))
	child, _ := filepath.Abs(filepath.Join("..", "session", "testdata", "child.py"))
	cfg := launch.Config{"name": "Parent", "type": "debugpy", "request": "launch", "program": parent, "subProcess": true, "console": "internalConsole"}
	daptest.UseAdapter(t, "debugpy")
	d := newDriver(t, newModel(t, ws, []launch.Config{cfg}, filepath.Join(t.TempDir(), "s.json")))

	d.m.openFile(child, 4)
	d.m.setFocus(paneEditor)
	d.key("b", "f5")
	d.until("child stops at breakpoint", func() bool {
		return len(d.m.runs) == 2 && d.m.status == statusStopped && d.m.execLoc == location{path: child, line: 4}
	})
	if d.m.active.parent != d.m.runs[0] {
		t.Fatal("subprocess session is not a child of the launch")
	}
	d.until("child locals", func() bool { return strings.Contains(d.screen(), "value: 41") })

	d.key("f9")
	d.until("all sessions end", func() bool { return len(d.m.runs) == 0 })
	var out strings.Builder
	for _, l := range d.m.console.lines {
		out.WriteString(l.text + "\n")
	}
	if !strings.Contains(out.String(), "child value 42") || !strings.Contains(out.String(), "parent done") {
		t.Fatalf("console:\n%s", out.String())
	}
}

// TestDisassembly shows Delve's disassembly, steps by instruction and stops
// at an instruction breakpoint.
func TestDisassembly(t *testing.T) {
	daptest.UseAdapter(t, "go")
	ws := examplesDir(t)
	main := filepath.Join(ws, "go", "main.go")
	cfg := launch.Config{"name": "Go", "type": "go", "request": "launch", "program": "${workspaceFolder}/go"}
	d := newDriver(t, newModel(t, ws, []launch.Config{cfg}, filepath.Join(t.TempDir(), "s.json")))

	d.m.openFile(main, 10)
	d.m.setFocus(paneEditor)
	d.key("b", "f5")
	d.until("breakpoint hit", func() bool { return d.m.status == statusStopped && d.m.execLoc.line == 10 })

	d.key("ctrl+y")
	d.until("disassembly", func() bool { return len(d.m.disasm.instrs) > 0 })
	if !strings.Contains(d.screen(), "Disassembly") || !strings.Contains(d.screen(), "main.go:10") {
		t.Fatalf("disassembly not shown:\n%s", d.screen())
	}
	first := d.m.disasm.ip

	d.key("f8")
	d.until("instruction step", func() bool {
		return d.m.status == statusStopped && d.m.disasm.ip != "" && !sameAddress(d.m.disasm.ip, first)
	})
	if d.m.execLoc.line != 10 {
		t.Fatalf("an instruction step left line 10 for %d", d.m.execLoc.line)
	}

	// Break a few instructions further on, then remove the line breakpoint so
	// only the instruction breakpoint can stop the program.
	d.key("down", "down", "down", "b")
	target := d.m.disasm.instrs[d.m.disasm.list.cursor].Address
	d.until("instruction breakpoint", func() bool { return len(d.m.instBps) == 1 })
	d.key("ctrl+y", "b", "ctrl+y")
	d.key("f9")
	d.until("stopped at instruction", func() bool {
		return d.m.status == statusStopped && sameAddress(d.m.disasm.ip, target)
	})

	d.key("shift+f5")
	d.until("session end", func() bool { return d.m.sess == nil })
}

// TestInlineBreakpoint sets a breakpoint at the cursor's column and sends the
// column to the adapter.
func TestInlineBreakpoint(t *testing.T) {
	ws := tempWorkspace(t)
	program := filepath.Join(ws, "main.py")
	statePath := filepath.Join(t.TempDir(), "s.json")
	cfg := launch.Config{"name": "Fake", "type": "fake", "request": "launch", "program": program}
	m := newModel(t, ws, []launch.Config{cfg}, statePath)
	m.adapters = fakeAdapters()
	d := newDriver(t, m)

	d.m.openFile(program, 0)
	d.m.setFocus(paneEditor)
	d.key("down", "right", "right", "right", "right", "i", "b")
	got := d.m.store.BreakpointsIn(program)
	if len(got) != 2 || got[0].Column != 0 || got[1].Column != 5 || got[1].Line != 2 {
		t.Fatalf("breakpoints %+v", got)
	}

	d.key("f5")
	d.until("verified inline breakpoint", func() bool {
		r, ok := d.m.records[program][bpKey{2, 5}]
		return ok && r.Verified && r.Column == 5
	})
	if !strings.Contains(d.screen(), "main.py:2:5") {
		t.Fatalf("inline breakpoint not listed:\n%s", d.screen())
	}
	d.key("shift+f5")
	d.until("session end", func() bool { return d.m.sess == nil })

	// The column survives a restart.
	again := newModel(t, ws, []launch.Config{cfg}, statePath)
	if bps := again.store.BreakpointsIn(program); len(bps) != 2 || bps[1].Column != 5 {
		t.Fatalf("after reload %+v", bps)
	}
}

// TestFullScreenProgram draws a full-screen program in the Terminal tab and
// sends it an arrow key.
func TestFullScreenProgram(t *testing.T) {
	ws := examplesDir(t)
	screen, _ := filepath.Abs(filepath.Join("..", "session", "testdata", "screen.py"))
	cfg := launch.Config{"name": "Screen", "type": "debugpy", "request": "launch", "program": screen, "console": "integratedTerminal"}
	daptest.UseAdapter(t, "debugpy")
	d := newDriver(t, newModel(t, ws, []launch.Config{cfg}, filepath.Join(t.TempDir(), "s.json")))

	d.key("f5")
	d.until("full-screen drawing", func() bool {
		if d.m.term == nil {
			return false
		}
		c := d.m.term.emu.CellAt(9, 2)
		return c != nil && c.Content == "F" && strings.Contains(d.screen(), "FULL SCREEN")
	})

	d.key("ctrl+l", "ctrl+j", "up")
	d.until("program end", func() bool { return d.m.sess == nil })
	if out := d.programOutput(); !strings.Contains(out, `got key '\x1b[A'`) {
		t.Fatalf("arrow key not received:\n%s", out)
	}
}

// TestMemoryViewAndBreakpointHints reads and writes memory and snaps inline
// breakpoints to the positions the adapter reports.
func TestMemoryViewAndBreakpointHints(t *testing.T) {
	ws := tempWorkspace(t)
	program := filepath.Join(ws, "main.py")
	cfg := launch.Config{"name": "Fake", "type": "fake", "request": "launch", "program": program}
	m := newModel(t, ws, []launch.Config{cfg}, filepath.Join(t.TempDir(), "s.json"))
	m.adapters = fakeAdapters()
	d := newDriver(t, m)

	d.m.openFile(program, 2)
	d.m.setFocus(paneEditor)
	d.key("b", "f5")
	d.until("stopped", func() bool { return d.m.status == statusStopped && strings.Contains(d.screen(), "answer: 42") })

	d.key("ctrl+h", "down", "m")
	d.until("memory", func() bool { return strings.Contains(d.screen(), "answer=42 lives") })
	if !strings.Contains(d.screen(), "0x0000000000001000") {
		t.Fatalf("memory address missing:\n%s", d.screen())
	}
	d.key("e", "ctrl+u")
	d.typeText("41 4e")
	d.key("enter")
	d.until("memory written", func() bool { return strings.Contains(d.screen(), "ANswer=42 lives") })
	d.key("esc")
	if d.m.memory.on {
		t.Fatal("esc did not close the memory view")
	}

	// The adapter allows columns 1 and 5 on every line; column 3 snaps to 5.
	d.key("ctrl+l", "down")
	d.until("hints", func() bool { return len(d.m.hints.cols) == 2 })
	d.key("right", "right", "i")
	bps := d.m.store.BreakpointsIn(program)
	if last := bps[len(bps)-1]; last.Line != 3 || last.Column != 5 {
		t.Fatalf("inline breakpoint %+v", last)
	}

	d.key("shift+f5")
	d.until("end", func() bool { return d.m.sess == nil })
}

// TestMemoryViewWithDelve opens the memory behind a Go string.
func TestMemoryViewWithDelve(t *testing.T) {
	daptest.UseAdapter(t, "go")
	ws := examplesDir(t)
	main := filepath.Join(ws, "go", "main.go")
	cfg := launch.Config{"name": "Go", "type": "go", "request": "launch", "program": "${workspaceFolder}/go"}
	d := newDriver(t, newModel(t, ws, []launch.Config{cfg}, filepath.Join(t.TempDir(), "s.json")))

	d.m.openFile(main, 10)
	d.m.setFocus(paneEditor)
	d.key("b", "f5")
	d.until("locals", func() bool { return strings.Contains(d.screen(), `message: "Hello, debugger 0!"`) })

	var withMemory *varNode
	for _, n := range flattenVars(d.m.scopes) {
		if n.memRef != "" {
			withMemory = n
			break
		}
	}
	if withMemory == nil {
		t.Skip("Delve gave no memory references")
	}
	d.key("ctrl+h")
	nodes := flattenVars(d.m.scopes)
	d.m.lists[paneVariables].cursor = slices.Index(nodes, withMemory)
	d.key("m")
	d.until("memory", func() bool { return len(d.m.memory.data) > 0 })
	// name and message both hold "...debugger 0".
	if !strings.Contains(d.screen(), "debugger 0") {
		t.Fatalf("memory of %s does not show its bytes:\n%s", withMemory.name, d.screen())
	}

	d.key("esc", "shift+f5")
	d.until("end", func() bool { return d.m.sess == nil })
}

// TestBackgroundPreLaunchTask waits for a background task to be ready before
// launching, reuses it for the next session and stops it on quit.
func TestBackgroundPreLaunchTask(t *testing.T) {
	ws := tempWorkspace(t)
	tasks := `{
  "version": "2.0.0",
  "tasks": [
    {"label": "serve", "type": "shell", "isBackground": true,
     "command": "echo starting; sleep 0.3; echo listening on 9000; exec sleep 60",
     "problemMatcher": {"pattern": {"regexp": "^never$"}, "background": {"beginsPattern": "^starting", "endsPattern": "^listening on \\d+"}}}
  ]
}`
	if err := os.MkdirAll(filepath.Join(ws, ".vscode"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".vscode", "tasks.json"), []byte(tasks), 0o644); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(ws, "main.py")
	cfg := launch.Config{"name": "Fake", "type": "fake", "request": "launch", "program": program, "preLaunchTask": "serve"}
	m := newModel(t, ws, []launch.Config{cfg}, filepath.Join(t.TempDir(), "s.json"))
	m.adapters = fakeAdapters()
	d := newDriver(t, m)

	d.key("f5")
	d.until("session after task ready", func() bool { return strings.Contains(d.programOutput(), "fake program") })
	out := d.programOutput()
	if strings.Index(out, "listening on 9000") > strings.Index(out, "fake program") {
		t.Fatalf("launched before the task was ready:\n%s", out)
	}
	d.until("session end", func() bool { return d.m.sess == nil && d.m.task == nil })
	if d.m.background["serve"] == nil {
		t.Fatal("background task not left running")
	}

	d.key("f5")
	d.until("second session end", func() bool {
		return d.m.sess == nil && strings.Count(d.programOutput(), "fake program") == 2
	})
	if n := strings.Count(d.programOutput(), "> Executing task: serve"); n != 1 {
		t.Fatalf("background task started %d times:\n%s", n, d.programOutput())
	}

	d.m.Shutdown()
	d.until("background task stopped", func() bool { return len(d.m.background) == 0 })
}

// TestBreakpointInAdapterSource sets a breakpoint in source the adapter
// provides by reference, which has no local file.
func TestBreakpointInAdapterSource(t *testing.T) {
	ws := tempWorkspace(t)
	cfg := launch.Config{"name": "Fake", "type": "fake", "request": "launch", "program": "<generated>", "generated": true, "stopOnEntry": true}
	m := newModel(t, ws, []launch.Config{cfg}, filepath.Join(t.TempDir(), "s.json"))
	m.adapters = fakeAdapters()
	d := newDriver(t, m)

	d.key("f5")
	d.until("adapter source", func() bool {
		return d.m.status == statusStopped && strings.Contains(d.screen(), "generated line 2") && d.m.execLoc.ref == 7
	})
	if !strings.Contains(d.screen(), "from debug adapter") {
		t.Fatalf("not marked as adapter source:\n%s", d.screen())
	}

	d.key("ctrl+l", "down", "b")
	d.until("verified breakpoint", func() bool {
		r, ok := d.m.refRecords[7][2]
		return ok && r.Verified
	})
	if len(d.m.store.Breakpoints) != 0 {
		t.Fatalf("adapter source breakpoint was persisted: %+v", d.m.store.Breakpoints)
	}

	d.key("f9")
	d.until("end", func() bool { return d.m.sess == nil })
}

// TestRunWithoutDebugging launches with noDebug on ctrl+F5, and F5 then debugs again.
func TestRunWithoutDebugging(t *testing.T) {
	ws := tempWorkspace(t)
	cfg := launch.Config{"name": "Fake", "type": "fake", "request": "launch", "program": filepath.Join(ws, "main.py")}
	m := newModel(t, ws, []launch.Config{cfg}, filepath.Join(t.TempDir(), "s.json"))
	m.adapters = fakeAdapters()
	d := newDriver(t, m)

	// The fake program ends at once, so the session is checked by its name.
	d.key("ctrl+f5")
	d.until("end", func() bool { return d.m.sess == nil && strings.Contains(d.programOutput(), "ended") })
	if !strings.Contains(d.programOutput(), `"Fake (without debugging)" ended`) {
		t.Fatalf("console:\n%s", d.programOutput())
	}

	d.key("f5")
	d.until("end", func() bool { return d.m.sess == nil && strings.Contains(d.programOutput(), `"Fake" ended`) })
}
