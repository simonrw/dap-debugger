package session_test

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	godap "github.com/google/go-dap"

	"github.com/simonrw/dap-debugger/internal/daptest"
	"github.com/simonrw/dap-debugger/internal/launch"
	"github.com/simonrw/dap-debugger/internal/session"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// startTarget runs a debug server in its own process group and waits until the
// target prints a tick. Probing the port instead would use up debugpy's listener.
func startTarget(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	log, err := os.Create(filepath.Join(t.TempDir(), "target.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start %s: %v", args[0], err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		// debugpy --listen starts its adapter in a process group of its own.
		for i, a := range args {
			if a == "--listen" && i+1 < len(args) {
				_, port, _ := strings.Cut(args[i+1], ":")
				_ = exec.Command("pkill", "-f", "debugpy/adapter.*--port "+port).Run()
			}
		}
		if t.Failed() {
			data, _ := os.ReadFile(log.Name())
			t.Logf("debug server output:\n%s", data)
		}
	})

	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(log.Name())
		if strings.Contains(string(data), "tick 1") {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	data, _ := os.ReadFile(log.Name())
	t.Fatalf("target did not start:\n%s", data)
}

// attachAndBreak attaches to a running target, sets a breakpoint while it runs
// and checks the target stops there, then detaches and checks it keeps running.
func attachAndBreak(t *testing.T, cfg launch.Config, source string, line int) {
	t.Helper()
	plan := prepare(t, cfg)

	s := session.New(plan, session.Options{})
	rec := record(t, s)
	if _, err := s.Start(session.Setup{}); err != nil {
		t.Fatalf("attach: %v\n%s", err, rec.drain())
	}

	records, _, err := s.SetBreakpoints(source, []godap.SourceBreakpoint{{Line: line}}, 1)
	if err != nil {
		t.Fatalf("set breakpoint while running: %v", err)
	}
	if len(records) != 1 || !records[0].Verified {
		t.Fatalf("breakpoint not verified: %+v", records)
	}

	var stopped godap.StoppedEventBody
	_ = json.Unmarshal(rec.waitFor("stopped"), &stopped)
	var stack godap.StackTraceResponseBody
	if err := s.Request("stackTrace", map[string]any{"threadId": stopped.ThreadId, "levels": 5}, &stack); err != nil {
		t.Fatal(err)
	}
	if top := stack.StackFrames[0]; top.Source == nil || top.Source.Path != source || top.Line != line {
		t.Fatalf("stopped at %+v, want %s:%d", stack.StackFrames[0], source, line)
	}

	if _, _, err := s.SetBreakpoints(source, nil, 2); err != nil {
		t.Fatal(err)
	}
	if err := s.Request("continue", map[string]any{"threadId": stopped.ThreadId}, nil); err != nil {
		t.Fatal(err)
	}
	s.Close(false)
	rec.drain()

	// The target and its debug server must survive the detach so it can be reattached.
	again := session.New(prepare(t, cfg), session.Options{})
	rec = record(t, again)
	if _, err := again.Start(session.Setup{}); err != nil {
		t.Fatalf("reattach: %v\n%s", err, rec.drain())
	}
	var threads godap.ThreadsResponseBody
	if err := again.Request("threads", nil, &threads); err != nil || len(threads.Threads) == 0 {
		t.Fatalf("threads after reattach: %v %+v", err, threads)
	}
	again.Close(false)
	rec.drain()
}

func TestPythonAttach(t *testing.T) {
	python := daptest.Python(t)
	port := freePort(t)
	script, _ := filepath.Abs(filepath.Join("testdata", "loop.py"))
	startTarget(t, python, "-m", "debugpy", "--listen", fmt.Sprintf("127.0.0.1:%d", port), script)

	attachAndBreak(t, launch.Config{
		"name": "py", "type": "debugpy", "request": "attach",
		"connect": map[string]any{"host": "127.0.0.1", "port": float64(port)},
	}, script, 8)
}

func TestGoAttach(t *testing.T) {
	dlv, err := exec.LookPath("dlv")
	if err != nil {
		t.Skip("dlv not installed")
	}
	port := freePort(t)
	dir, _ := filepath.Abs(filepath.Join("testdata", "loop"))
	binary := filepath.Join(t.TempDir(), "loop")
	build := exec.Command("go", "build", "-gcflags=all=-N -l", "-o", binary, ".")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	startTarget(t, dlv, "exec", binary, "--headless", "--accept-multiclient", "--continue",
		fmt.Sprintf("--listen=127.0.0.1:%d", port))

	attachAndBreak(t, launch.Config{
		"name": "go", "type": "go", "request": "attach", "mode": "remote",
		"host": "127.0.0.1", "port": float64(port),
	}, filepath.Join(dir, "main.go"), 11)
}
