package session_test

import (
	"encoding/json"
	"net"
	"os"
	"strings"
	"testing"

	godap "github.com/google/go-dap"

	"github.com/simonrw/dap-debugger/internal/daptest"
	"github.com/simonrw/dap-debugger/internal/launch"
	"github.com/simonrw/dap-debugger/internal/session"
)

func TestMain(m *testing.M) {
	daptest.Main()
	os.Exit(m.Run())
}

// fakeAdapter defines the test binary as an adapter in the given mode.
func fakeAdapter(mode string, args ...string) launch.Adapter {
	return launch.Adapter{Command: append([]string{os.Args[0]}, args...), Env: map[string]string{daptest.Env: mode}}
}

// TestGenericAdapter runs a session against an adapter that is only known by
// its definition, over each transport.
func TestGenericAdapter(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go daptest.Serve(conn)
		}
	}()
	serverPort := float64(listener.Addr().(*net.TCPAddr).Port)

	for name, tc := range map[string]struct {
		cfg      launch.Config
		adapters map[string]launch.Adapter
	}{
		"stdio":            {adapters: map[string]launch.Adapter{"fake": fakeAdapter("stdio")}},
		"adapter listens":  {adapters: map[string]launch.Adapter{"fake": fakeAdapter("listen", "{port}")}},
		"adapter connects": {adapters: map[string]launch.Adapter{"fake": fakeAdapter("connect", "{address}")}},
		"debugAdapter in config": {cfg: launch.Config{"debugAdapter": map[string]any{
			"command": []any{os.Args[0]}, "env": map[string]any{daptest.Env: "stdio"},
		}}},
		"debugServer": {cfg: launch.Config{"debugServer": serverPort}},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := launch.Config{"name": "fake", "type": "fake", "request": "launch", "program": "/src/app.fake"}
			for k, v := range tc.cfg {
				cfg[k] = v
			}
			plan, err := launch.Prepare(cfg, launch.Vars{Workspace: t.TempDir(), Adapters: tc.adapters})
			if err != nil {
				t.Fatal(err)
			}

			s := session.New(plan, session.Options{})
			rec := record(t, s)
			started, err := s.Start(session.Setup{
				Breakpoints:      map[string][]godap.SourceBreakpoint{"/src/app.fake": {{Line: 3}}},
				ExceptionFilters: func(f []godap.ExceptionBreakpointsFilter) []string { return []string{f[0].Filter} },
			})
			if err != nil {
				t.Fatalf("start: %v\n%s", err, rec.drain())
			}
			if r := started.Breakpoints["/src/app.fake"]; len(r) != 1 || !r[0].Verified {
				t.Fatalf("breakpoints %+v", r)
			}

			var stopped godap.StoppedEventBody
			_ = json.Unmarshal(rec.waitFor("stopped"), &stopped)
			var stack godap.StackTraceResponseBody
			if err := s.Request("stackTrace", map[string]any{"threadId": stopped.ThreadId}, &stack); err != nil {
				t.Fatal(err)
			}
			if f := stack.StackFrames[0]; f.Line != 3 || f.Source.Path != "/src/app.fake" {
				t.Fatalf("frame %+v", f)
			}

			if err := s.Request("setVariable", map[string]any{"variablesReference": 10, "name": "answer", "value": "7"}, nil); err != nil {
				t.Fatal(err)
			}
			if err := s.Request("continue", map[string]any{"threadId": stopped.ThreadId}, nil); err != nil {
				t.Fatal(err)
			}
			rec.waitFor("terminated")
			<-s.Finished()

			if out := rec.drain(); !strings.Contains(out, "answer is 7") {
				t.Fatalf("output:\n%s", out)
			}
		})
	}
}

func TestUnknownAdapterType(t *testing.T) {
	_, err := launch.Prepare(launch.Config{"name": "x", "type": "mystery", "request": "launch"}, launch.Vars{Workspace: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "adapters.json") {
		t.Fatalf("got %v", err)
	}
}
