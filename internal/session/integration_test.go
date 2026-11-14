package session_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	godap "github.com/google/go-dap"

	"github.com/simonrw/dap-debugger/internal/daptest"
	"github.com/simonrw/dap-debugger/internal/launch"
	"github.com/simonrw/dap-debugger/internal/session"
)

func examples(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	dir, err := filepath.Abs(filepath.Join(filepath.Dir(file), "..", "..", "examples"))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// recorder collects events from a session in the background.
type recorder struct {
	t      *testing.T
	events chan session.Event
	output strings.Builder
}

func record(t *testing.T, s *session.Session) *recorder {
	r := &recorder{t: t, events: make(chan session.Event, 1024)}
	go func() {
		for {
			e, ok := s.Next()
			if !ok {
				close(r.events)
				return
			}
			r.events <- e
		}
	}()
	return r
}

func (r *recorder) collect(e session.Event) {
	switch e.Name {
	case "output":
		var body godap.OutputEventBody
		_ = json.Unmarshal(e.Body, &body)
		r.output.WriteString(body.Output)
	case "terminal":
		var body session.TerminalData
		_ = json.Unmarshal(e.Body, &body)
		r.output.Write(body.Data)
	}
}

// waitFor returns the body of the next event with the given name, collecting output on the way.
func (r *recorder) waitFor(name string) json.RawMessage {
	r.t.Helper()
	timeout := time.After(60 * time.Second)
	for {
		select {
		case e, ok := <-r.events:
			if !ok {
				r.t.Fatalf("session ended waiting for %s; output:\n%s", name, r.output.String())
			}
			r.collect(e)
			if e.Name == name {
				return e.Body
			}
		case <-timeout:
			r.t.Fatalf("timed out waiting for %s; output:\n%s", name, r.output.String())
		}
	}
}

// drain waits for the session to end and returns all output.
func (r *recorder) drain() string {
	r.t.Helper()
	timeout := time.After(30 * time.Second)
	for {
		select {
		case e, ok := <-r.events:
			if !ok {
				return r.output.String()
			}
			r.collect(e)
		case <-timeout:
			r.t.Fatal("session did not end")
		}
	}
}

func prepare(t *testing.T, cfg launch.Config) *launch.Plan {
	t.Helper()
	daptest.UseAdapter(t, cfg.String("type"))

	ws := examples(t)
	plan, err := launch.Prepare(cfg, launch.Vars{Workspace: ws, Env: os.Getenv})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	return plan
}

// exercise stops at a breakpoint, inspects the frame, steps and continues to the end.
func exercise(t *testing.T, plan *launch.Plan, source string, line int, local string) {
	t.Helper()
	s := session.New(plan, session.Options{})
	rec := record(t, s)

	// A remembered breakpoint in a missing file must not stop the session starting.
	started, err := s.Start(session.Setup{
		Breakpoints: map[string][]godap.SourceBreakpoint{
			source:                                 {{Line: line}},
			filepath.Join(t.TempDir(), "gone.txt"): {{Line: 1}},
		},
	})
	if err != nil {
		t.Fatalf("start: %v; output:\n%s", err, rec.drain())
	}
	if records := started.Breakpoints[source]; len(records) != 1 || !records[0].Verified {
		t.Fatalf("breakpoint not verified: %+v", records)
	}

	var stopped godap.StoppedEventBody
	_ = json.Unmarshal(rec.waitFor("stopped"), &stopped)
	if stopped.Reason != "breakpoint" {
		t.Fatalf("stopped for %q", stopped.Reason)
	}

	var stack godap.StackTraceResponseBody
	if err := s.Request("stackTrace", map[string]any{"threadId": stopped.ThreadId, "levels": 20}, &stack); err != nil {
		t.Fatal(err)
	}
	top := stack.StackFrames[0]
	if top.Source == nil || top.Source.Path != source || top.Line != line {
		t.Fatalf("top frame %+v, want %s:%d", top, source, line)
	}

	var scopes godap.ScopesResponseBody
	if err := s.Request("scopes", map[string]any{"frameId": top.Id}, &scopes); err != nil {
		t.Fatal(err)
	}
	var vars godap.VariablesResponseBody
	if err := s.Request("variables", map[string]any{"variablesReference": scopes.Scopes[0].VariablesReference}, &vars); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, v := range vars.Variables {
		found = found || v.Name == local
	}
	if !found {
		t.Fatalf("local %q missing from %+v", local, vars.Variables)
	}

	var eval godap.EvaluateResponseBody
	if err := s.Request("evaluate", map[string]any{"expression": local, "frameId": top.Id, "context": "repl"}, &eval); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(eval.Result, "debugger 0") {
		t.Fatalf("evaluate %s = %q", local, eval.Result)
	}

	if err := s.Request("next", map[string]any{"threadId": stopped.ThreadId}, nil); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(rec.waitFor("stopped"), &stopped)
	if stopped.Reason != "step" {
		t.Fatalf("stopped for %q after step", stopped.Reason)
	}

	records, stale, err := s.SetBreakpoints(source, nil, 1)
	if err != nil || stale || len(records) != 0 {
		t.Fatalf("clear breakpoints: %v %v %v", records, stale, err)
	}

	if err := s.Request("continue", map[string]any{"threadId": stopped.ThreadId}, nil); err != nil {
		t.Fatal(err)
	}
	rec.waitFor("terminated")

	select {
	case <-s.Finished():
	case <-time.After(30 * time.Second):
		t.Fatal("session did not finish after termination")
	}
	output := rec.drain()
	if !strings.Contains(output, "Hello, debugger 4!") {
		t.Fatalf("program output missing:\n%s", output)
	}
	if strings.Contains(output, "Traceback") {
		t.Fatalf("adapter reported an error:\n%s", output)
	}
}

func TestPythonLaunch(t *testing.T) {
	ws := examples(t)
	plan := prepare(t, launch.Config{
		"name": "py", "type": "debugpy", "request": "launch", "program": "${workspaceFolder}/hello.py",
	})
	exercise(t, plan, filepath.Join(ws, "hello.py"), 8, "message")
}

func TestGoLaunch(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not installed")
	}
	ws := examples(t)
	plan := prepare(t, launch.Config{
		"name": "go", "type": "go", "request": "launch", "mode": "debug", "program": "${workspaceFolder}/go",
	})
	exercise(t, plan, filepath.Join(ws, "go", "main.go"), 10, "message")
}

// TestStopWhilePaused disconnects from a paused launch, as Shift+F5 does.
func TestStopWhilePaused(t *testing.T) {
	ws := examples(t)
	for _, cfg := range []launch.Config{
		{"name": "py", "type": "debugpy", "request": "launch", "program": "${workspaceFolder}/hello.py"},
		{"name": "go", "type": "go", "request": "launch", "program": "${workspaceFolder}/go"},
	} {
		t.Run(cfg.String("type"), func(t *testing.T) {
			plan := prepare(t, cfg)
			source := filepath.Join(ws, "hello.py")
			line := 8
			if cfg.String("type") == "go" {
				source, line = filepath.Join(ws, "go", "main.go"), 10
			}

			s := session.New(plan, session.Options{})
			rec := record(t, s)
			if _, err := s.Start(session.Setup{Breakpoints: map[string][]godap.SourceBreakpoint{source: {{Line: line}}}}); err != nil {
				t.Fatalf("start: %v\n%s", err, rec.drain())
			}
			rec.waitFor("stopped")

			s.Close(true)
			output := rec.drain()
			if strings.Contains(output, "Traceback") || strings.Contains(output, "panic") {
				t.Fatalf("adapter reported an error on disconnect:\n%s", output)
			}
		})
	}
}
