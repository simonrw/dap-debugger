package ui

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/simonrw/dap-debugger/internal/launch"
	"github.com/simonrw/dap-debugger/internal/state"
)

var updateVSCode = flag.Bool("update-vscode", false, "record VS Code's requests for each scenario with tools/vscode-probe (opens VS Code)")

// scenario is a scripted debug session run in both VS Code and dap-debugger;
// tools/vscode-probe/extension.js documents the fields.
type scenario struct {
	Config launch.Config `json:"config"`
	// Configs start together, as a compound does, in place of Config.
	Configs     []launch.Config `json:"configs"`
	Breakpoints []struct {
		File         string `json:"file"`
		Line         int    `json:"line"`
		Condition    string `json:"condition"`
		HitCondition string `json:"hitCondition"`
		LogMessage   string `json:"logMessage"`
	} `json:"breakpoints"`
	FunctionBreakpoints []struct {
		Name      string `json:"name"`
		Condition string `json:"condition"`
	} `json:"functionBreakpoints"`
	Steps  []string `json:"steps"`
	Server *struct {
		Command string `json:"command"`
		Ready   string `json:"ready"`
	} `json:"server"`
	// Recorded are VS Code's requests between initialized and stopped events,
	// for each session in the order they started.
	Recorded [][][]string `json:"recorded"`
}

type traceEntry struct {
	Session   string `json:"session"`
	Direction string `json:"direction"`
	Message   struct {
		Type      string         `json:"type"`
		Command   string         `json:"command"`
		Event     string         `json:"event"`
		Arguments map[string]any `json:"arguments"`
	} `json:"message"`
}

// signature is a request with the arguments that should match between clients.
func signature(command string, args map[string]any) string {
	list := func(key string) []any {
		l, _ := args[key].([]any)
		return l
	}
	str := func(m map[string]any, key string) string {
		s, _ := m[key].(string)
		return s
	}

	switch command {
	case "setBreakpoints":
		source, _ := args["source"].(map[string]any)
		name := filepath.Base(str(source, "path"))
		if name == "." || name == "" {
			name = str(source, "name")
		}
		var bps []string
		for _, item := range list("breakpoints") {
			bp, _ := item.(map[string]any)
			s := fmt.Sprint(bp["line"])
			for _, k := range []string{"column", "condition", "hitCondition", "logMessage"} {
				if v, ok := bp[k]; ok && v != "" {
					s += fmt.Sprintf(" %s=%v", k, v)
				}
			}
			bps = append(bps, s)
		}
		return fmt.Sprintf("setBreakpoints %s [%s]", name, strings.Join(bps, ", "))
	case "setFunctionBreakpoints":
		var names []string
		for _, item := range list("breakpoints") {
			bp, _ := item.(map[string]any)
			names = append(names, str(bp, "name")+condition(bp))
		}
		return fmt.Sprintf("setFunctionBreakpoints [%s]", strings.Join(names, ", "))
	case "setExceptionBreakpoints":
		var filters []string
		for _, f := range list("filters") {
			filters = append(filters, fmt.Sprint(f))
		}
		sort.Strings(filters)
		return fmt.Sprintf("setExceptionBreakpoints [%s]", strings.Join(filters, ", "))
	case "setInstructionBreakpoints", "setDataBreakpoints":
		return fmt.Sprintf("%s %d", command, len(list("breakpoints")))
	case "disconnect", "terminate":
		s := command
		for _, k := range []string{"restart", "terminateDebuggee", "suspendDebuggee"} {
			if v, ok := args[k]; ok {
				s += fmt.Sprintf(" %s=%v", k, v)
			}
		}
		return s
	}
	return command
}

func condition(bp map[string]any) string {
	if c, _ := bp["condition"].(string); c != "" {
		return " if " + c
	}
	return ""
}

// sessionSegments groups each session's request signatures between its
// initialized and stopped events, with sessions ordered by name. Digits are
// ignored, as a subprocess session is named after its process ID.
func sessionSegments(entries []traceEntry) [][][]string {
	var order []string
	bySession := map[string][]traceEntry{}
	for _, e := range entries {
		name := regexp.MustCompile(`\d+`).ReplaceAllString(e.Session, "N")
		if _, ok := bySession[name]; !ok {
			order = append(order, name)
		}
		bySession[name] = append(bySession[name], e)
	}
	sort.Strings(order)
	out := make([][][]string, len(order))
	for i, name := range order {
		out[i] = segments(bySession[name])
	}
	return out
}

// segments groups request signatures between initialized and stopped events.
func segments(entries []traceEntry) [][]string {
	out := [][]string{{}}
	for _, e := range entries {
		m := e.Message
		switch {
		case e.Direction == "adapter" && m.Type == "event" && (m.Event == "initialized" || m.Event == "stopped"):
			out = append(out, []string{})
		case e.Direction == "client" && m.Type == "request":
			out[len(out)-1] = append(out[len(out)-1], signature(m.Command, m.Arguments))
		}
	}
	return out
}

func readTrace(t *testing.T, path string) []traceEntry {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var entries []traceEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(nil, 16*1024*1024)
	for scanner.Scan() {
		var e traceEntry
		if json.Unmarshal(scanner.Bytes(), &e) == nil {
			entries = append(entries, e)
		}
	}
	return entries
}

func stopCount(entries []traceEntry) int {
	n := 0
	for _, e := range entries {
		if e.Direction == "adapter" && e.Message.Type == "event" && e.Message.Event == "stopped" {
			n++
		}
	}
	return n
}

// startServer runs a scenario's debug server and waits until it is ready.
func startServer(t *testing.T, ws, command, ready string) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = ws
	cmd.Env = append(os.Environ(), "PROBE_TMP="+t.TempDir())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	log := filepath.Join(t.TempDir(), "server.log")
	f, err := os.Create(log)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = f, f
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		killDebugpyAdapters(command)
	})

	re := regexp.MustCompile(ready)
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if data, _ := os.ReadFile(log); re.Match(data) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	data, _ := os.ReadFile(log)
	t.Fatalf("server not ready:\n%s", data)
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// killDebugpyAdapters stops the adapter debugpy --listen starts in a process
// group of its own, which outlives the server otherwise.
func killDebugpyAdapters(command string) {
	if m := regexp.MustCompile(`--listen \S+:(\d+)`).FindStringSubmatch(command); m != nil {
		_ = exec.Command("pkill", "-f", "debugpy/adapter.*--port "+m[1]).Run()
	}
}

// pump handles messages for a while, as time passes for a user.
func (d *driver) pump(dur time.Duration) {
	deadline := time.After(dur)
	for {
		select {
		case msg := <-d.msgs:
			d.send(msg)
		case <-deadline:
			return
		}
	}
}

// recordVSCode runs a scenario in VS Code and returns its request segments.
func recordVSCode(t *testing.T, path string) [][][]string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "vscode.json")
	cmd := exec.Command(filepath.Join("..", "..", "tools", "vscode-probe", "run.sh"), path, out)
	cmd.Env = os.Environ()
	if dlv, err := exec.LookPath("dlv"); err == nil {
		cmd.Env = append(cmd.Env, "PATH="+filepath.Dir(dlv)+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	if venv, _ := filepath.Abs(filepath.Join("..", "..", ".venv", "bin", "python")); fileExists(venv) {
		cmd.Env = append(cmd.Env, "PROBE_PYTHON="+venv)
	}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("probe: %v\n%s", err, output)
	}

	var result struct {
		Error *string           `json:"error"`
		Log   []json.RawMessage `json:"log"`
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Error != nil {
		t.Fatalf("VS Code session failed: %s", *result.Error)
	}
	// Tracker errors are plain strings and are skipped.
	var entries []traceEntry
	for _, raw := range result.Log {
		var e traceEntry
		if json.Unmarshal(raw, &e) == nil {
			entries = append(entries, e)
		}
	}
	return sessionSegments(entries)
}

// saveRecording stores recorded segments in a scenario file, keeping its other fields.
func saveRecording(t *testing.T, path string, recorded [][][]string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(recorded)
	fields["recorded"] = raw

	var keys []string
	for k := range fields {
		keys = append(keys, k)
	}
	order := []string{"server", "config", "configs", "vscode", "breakpoints", "functionBreakpoints", "steps", "recorded"}
	sort.Slice(keys, func(i, j int) bool { return slices.Index(order, keys[i]) < slices.Index(order, keys[j]) })

	var b strings.Builder
	b.WriteString("{\n")
	for i, k := range keys {
		var v any
		_ = json.Unmarshal(fields[k], &v)
		var value strings.Builder
		enc := json.NewEncoder(&value)
		enc.SetEscapeHTML(false)
		enc.SetIndent(" ", " ")
		_ = enc.Encode(v)
		fmt.Fprintf(&b, " %q: %s", k, strings.TrimSuffix(value.String(), "\n"))
		if i < len(keys)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("}\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// runScenario plays a scenario in dap-debugger, driven by keys, and returns its trace.
func runScenario(t *testing.T, ws string, sc scenario) []traceEntry {
	t.Helper()
	if sc.Server != nil {
		startServer(t, ws, sc.Server.Command, sc.Server.Ready)
	}

	trace := filepath.Join(t.TempDir(), "trace.jsonl")
	configs := []launch.Config{sc.Config}
	if sc.Configs != nil {
		configs = sc.Configs
	}
	m := newModel(t, ws, configs, filepath.Join(t.TempDir(), "s.json"))
	if sc.Configs != nil {
		var members []any
		for _, c := range configs {
			members = append(members, c.Name())
		}
		m.file.Compounds = []*launch.Compound{{Name: "All", Configurations: members}}
		m.configIndex = len(configs)
	}
	m.sessOpts.Trace = trace
	for _, bp := range sc.Breakpoints {
		path, _ := filepath.Abs(filepath.Join(ws, bp.File))
		m.store.Breakpoints = append(m.store.Breakpoints, state.Breakpoint{
			Path: path, Line: bp.Line, Enabled: true, Condition: bp.Condition, HitCondition: bp.HitCondition, LogMessage: bp.LogMessage,
		})
	}
	for _, fn := range sc.FunctionBreakpoints {
		m.store.Functions = append(m.store.Functions, state.FunctionBreakpoint{Name: fn.Name, Enabled: true, Condition: fn.Condition})
	}
	d := newDriver(t, m)

	stops := func() int {
		if !fileExists(trace) {
			return 0
		}
		return stopCount(readTrace(t, trace))
	}
	keys := map[string]string{
		"stepOver": "f8", "stepInto": "f7", "stepOut": "shift+f11", "continue": "f9",
		"restart": "ctrl+shift+f5", "stop": "shift+f5",
	}

	d.key("f5")
	for i, step := range sc.Steps {
		d.until(fmt.Sprintf("stop %d", i+1), func() bool { return stops() > i && d.m.status == statusStopped })
		// Wait as VS Code's probe does, so the stack and variables load first.
		d.pump(1500 * time.Millisecond)
		if step == "continueThenPause" {
			d.key("f9")
			d.pump(1500 * time.Millisecond)
			d.key("f6")
			continue
		}
		d.key(keys[step])
	}
	d.until("all sessions end", func() bool { return len(d.m.runs) == 0 && d.m.sess == nil })
	return readTrace(t, trace)
}

// TestRequestsMatchVSCode plays each scenario in testdata/vscode in
// dap-debugger and checks it sends the same requests VS Code sent between
// each pair of initialized and stopped events, recorded with
// tools/vscode-probe (go test -run RequestsMatchVSCode -update-vscode).
//
// Requests within a segment may be ordered differently, as both clients send
// some concurrently, and dap-debugger may fetch scopes and variables on a stop
// where VS Code's Variables view was not on screen. The threads request after
// configurationDone is not compared, as a quick stop can overtake it.
func TestRequestsMatchVSCode(t *testing.T) {
	ws := examplesDir(t)
	files, _ := filepath.Glob(filepath.Join("testdata", "vscode", "*.json"))
	if len(files) == 0 {
		t.Fatal("no scenarios")
	}

	for _, path := range files {
		name := strings.TrimSuffix(filepath.Base(path), ".json")
		t.Run(name, func(t *testing.T) {
			var sc scenario
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			port := strconv.Itoa(freePort(t))
			if err := json.Unmarshal([]byte(strings.ReplaceAll(string(data), "{port}", port)), &sc); err != nil {
				t.Fatal(err)
			}

			for _, c := range append([]launch.Config{sc.Config}, sc.Configs...) {
				if c == nil {
					continue
				}
				if c.String("type") == "go" {
					if _, err := exec.LookPath("dlv"); err != nil {
						t.Skip("dlv not installed")
					}
				} else if _, err := launch.Prepare(c, launch.Vars{Workspace: ws}); err != nil {
					t.Skipf("adapter unavailable: %v", err)
				}
			}

			if *updateVSCode {
				sc.Recorded = recordVSCode(t, path)
				saveRecording(t, path, sc.Recorded)
			}
			if sc.Recorded == nil {
				t.Fatalf("%s has no recording; run with -update-vscode", path)
			}

			sessions := sessionSegments(runScenario(t, ws, sc))
			if len(sessions) != len(sc.Recorded) {
				t.Fatalf("%d sessions, VS Code %d", len(sessions), len(sc.Recorded))
			}
			for s, recorded := range sc.Recorded {
				compareSegments(t, s, sessions[s], recorded)
			}
		})
	}
}

// compareSegments checks one session's requests against VS Code's.
func compareSegments(t *testing.T, session int, ours, recorded [][]string) {
	t.Helper()
	if len(ours) != len(recorded) {
		t.Fatalf("session %d: %d segments, VS Code %d:\nours   %q\nvscode %q", session, len(ours), len(recorded), ours, recorded)
	}
	for i, want := range recorded {
		got := slices.DeleteFunc(distinct(ours[i]), func(c string) bool {
			return (c == "variables" || c == "scopes") && !slices.Contains(want, c)
		})
		want = distinct(want)
		// Both clients fetch threads after configurationDone, but a program
		// that stops at once can stop before the request is sent.
		if i == 1 {
			got = slices.DeleteFunc(got, func(c string) bool { return c == "threads" })
			want = slices.DeleteFunc(want, func(c string) bool { return c == "threads" })
		}
		if !slices.Equal(got, want) {
			t.Errorf("session %d segment %d:\nours   %q\nvscode %q", session, i, distinct(ours[i]), want)
		}
	}
}

func distinct(commands []string) []string {
	out := slices.Clone(commands)
	slices.Sort(out)
	return slices.Compact(out)
}
