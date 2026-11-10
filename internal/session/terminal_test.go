package session_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/simonrw/dap-debugger/internal/launch"
	"github.com/simonrw/dap-debugger/internal/session"
)

// TestIntegratedTerminal runs programs with console integratedTerminal and
// answers their input prompt through the session.
func TestIntegratedTerminal(t *testing.T) {
	echo, _ := filepath.Abs("testdata")
	for _, cfg := range []launch.Config{
		{"name": "py", "type": "debugpy", "request": "launch", "program": filepath.Join(echo, "echo.py"), "console": "integratedTerminal"},
		{"name": "go", "type": "go", "request": "launch", "program": filepath.Join(echo, "echo"), "console": "integratedTerminal"},
	} {
		t.Run(cfg.String("type"), func(t *testing.T) {
			s := session.New(prepare(t, cfg), session.Options{})
			rec := record(t, s)
			if _, err := s.Start(session.Setup{}); err != nil {
				t.Fatalf("start: %v\n%s", err, rec.drain())
			}

			deadline := time.Now().Add(60 * time.Second)
			for !strings.Contains(rec.output.String(), "name? ") {
				if time.Now().After(deadline) {
					t.Fatalf("no prompt:\n%s", rec.output.String())
				}
				select {
				case e := <-rec.events:
					rec.collect(e)
				case <-time.After(100 * time.Millisecond):
				}
			}

			if !s.HasTerminal() {
				t.Fatal("program was not started with runInTerminal")
			}
			if err := s.WriteStdin("gopher"); err != nil {
				t.Fatal(err)
			}
			if cfg.String("type") == "debugpy" {
				// getpass reads from the controlling terminal and turns off echo.
				for !strings.Contains(rec.output.String(), "secret? ") {
					rec.collect(<-rec.events)
				}
				if err := s.WriteStdin("hunter"); err != nil {
					t.Fatal(err)
				}
			}
			rec.waitFor("terminated")

			out := rec.drain()
			if !strings.Contains(out, "hello gopher") {
				t.Fatalf("program did not read its input:\n%s", out)
			}
			if !strings.Contains(out, "secret has 6 characters, tty: True") && !strings.Contains(out, "secret has 6 characters, tty: true") {
				t.Fatalf("program did not get a terminal:\n%s", out)
			}
			if strings.Contains(out, "hunter") {
				t.Fatalf("password was echoed:\n%s", out)
			}
		})
	}
}
