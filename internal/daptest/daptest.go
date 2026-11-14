// Package daptest is a minimal debug adapter for tests. It fakes a program
// that stops at the first breakpoint it is given, so the client can be tested
// against the protocol alone, without a real debugger.
//
// With a "child" configuration in the launch arguments, it asks the client
// to start that configuration as a child session with startDebugging.
//
// A test binary becomes the adapter when it calls Main from TestMain and the
// environment variable DAPTEST_ADAPTER is set:
//
//   - "stdio": DAP over stdin and stdout
//   - "listen": listen on the port given as the first argument
//   - "connect": connect to the address given as the first argument
//
// UseAdapter sets up a real adapter, debugpy or Delve, for integration tests.
package daptest

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"sync"

	"github.com/simonrw/dap-debugger/internal/dap"
)

// Env is the environment variable that turns a test binary into the adapter.
const Env = "DAPTEST_ADAPTER"

// Main runs the adapter and exits when Env is set, and returns otherwise.
func Main() {
	mode := os.Getenv(Env)
	if mode == "" {
		return
	}

	var err error
	switch mode {
	case "stdio":
		Serve(struct {
			io.Reader
			io.Writer
		}{os.Stdin, os.Stdout})
	case "listen":
		var l net.Listener
		if l, err = net.Listen("tcp", "127.0.0.1:"+os.Args[1]); err == nil {
			var conn net.Conn
			if conn, err = l.Accept(); err == nil {
				Serve(conn)
			}
		}
	case "connect":
		var conn net.Conn
		if conn, err = net.Dial("tcp", os.Args[1]); err == nil {
			Serve(conn)
		}
	default:
		err = fmt.Errorf("unknown mode %q", mode)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "daptest:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

type breakpoint struct {
	path string
	line int
}

type adapter struct {
	w   io.Writer
	mu  sync.Mutex
	seq int

	program string
	child   map[string]any
	// generated makes the program's source available only by reference.
	generated   bool
	stopOnEntry bool
	memory      []byte
	bps         []breakpoint
	value       int
}

// Serve speaks DAP on conn until the client disconnects.
func Serve(conn io.ReadWriter) {
	a := &adapter{w: conn, value: 42, memory: []byte("answer=42 lives here, at 0x1000.")}
	r := bufio.NewReader(conn)
	for {
		msg, err := dap.ReadMessage(r)
		if err != nil {
			return
		}
		if msg.Type != "request" {
			continue
		}
		if done := a.handle(msg); done {
			return
		}
	}
}

func (a *adapter) send(v map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seq++
	v["seq"] = a.seq
	_ = dap.WriteMessage(a.w, v)
}

func (a *adapter) event(name string, body any) {
	a.send(map[string]any{"type": "event", "event": name, "body": body})
}

func (a *adapter) reply(req dap.Message, body any) {
	a.send(map[string]any{"type": "response", "request_seq": req.Seq, "command": req.Command, "success": true, "body": body})
}

func (a *adapter) fail(req dap.Message, message string) {
	a.send(map[string]any{"type": "response", "request_seq": req.Seq, "command": req.Command, "success": false, "message": message})
}

func (a *adapter) stopAt() breakpoint {
	if len(a.bps) == 0 {
		return breakpoint{path: a.program, line: 1}
	}
	return a.bps[0]
}

func (a *adapter) handle(req dap.Message) (done bool) {
	var args map[string]any
	_ = json.Unmarshal(req.Arguments, &args)

	switch req.Command {
	case "initialize":
		a.reply(req, map[string]any{
			"supportsConfigurationDoneRequest":   true,
			"supportsSetVariable":                true,
			"supportsReadMemoryRequest":          true,
			"supportsWriteMemoryRequest":         true,
			"supportsBreakpointLocationsRequest": true,
			"exceptionBreakpointFilters":         []map[string]any{{"filter": "raised", "label": "Raised Exceptions", "default": false}},
		})
		a.event("initialized", map[string]any{})

	case "launch", "attach":
		a.program, _ = args["program"].(string)
		a.child, _ = args["child"].(map[string]any)
		a.generated, _ = args["generated"].(bool)
		a.stopOnEntry, _ = args["stopOnEntry"].(bool)
		a.reply(req, nil)

	case "setBreakpoints":
		source, _ := args["source"].(map[string]any)
		path, _ := source["path"].(string)
		kept := a.bps[:0]
		for _, bp := range a.bps {
			if bp.path != path {
				kept = append(kept, bp)
			}
		}
		a.bps = kept

		var records []map[string]any
		list, _ := args["breakpoints"].([]any)
		for i, item := range list {
			bp := item.(map[string]any)
			line := int(bp["line"].(float64))
			a.bps = append(a.bps, breakpoint{path: path, line: line})
			record := map[string]any{"id": i + 1, "verified": true, "line": line}
			if column, ok := bp["column"].(float64); ok {
				record["column"] = int(column)
			}
			records = append(records, record)
		}
		a.reply(req, map[string]any{"breakpoints": records})

	case "setExceptionBreakpoints":
		a.reply(req, nil)

	case "configurationDone":
		a.reply(req, nil)
		a.event("output", map[string]any{"category": "stdout", "output": "fake program " + a.program + " started\n"})
		if a.child != nil {
			// Ask the client for a child session, as adapters do for subprocesses.
			a.send(map[string]any{"type": "request", "command": "startDebugging",
				"arguments": map[string]any{"request": "launch", "configuration": a.child}})
		}
		switch {
		case a.stopOnEntry:
			a.event("stopped", map[string]any{"reason": "entry", "threadId": 1, "allThreadsStopped": true})
		case len(a.bps) == 0:
			a.finish()
		default:
			a.event("stopped", map[string]any{"reason": "breakpoint", "threadId": 1, "allThreadsStopped": true})
		}

	case "threads":
		a.reply(req, map[string]any{"threads": []map[string]any{{"id": 1, "name": "main"}}})

	case "stackTrace":
		at := a.stopAt()
		source := map[string]any{"path": at.path, "name": "main"}
		if a.generated {
			source = map[string]any{"name": "generated.fake", "sourceReference": 7}
		}
		a.reply(req, map[string]any{"stackFrames": []map[string]any{
			{"id": 1, "name": "main", "line": at.line, "column": 1, "source": source},
		}, "totalFrames": 1})

	case "source":
		a.reply(req, map[string]any{"content": "generated line 1\ngenerated line 2\ngenerated line 3\n"})

	case "scopes":
		a.reply(req, map[string]any{"scopes": []map[string]any{{"name": "Locals", "variablesReference": 10}}})

	case "variables":
		a.reply(req, map[string]any{"variables": []map[string]any{
			{"name": "answer", "value": fmt.Sprint(a.value), "type": "int", "variablesReference": 0, "memoryReference": "0x1000"},
		}})

	case "setVariable":
		var v int
		if _, err := fmt.Sscan(fmt.Sprint(args["value"]), &v); err != nil {
			a.fail(req, "not a number")
			return false
		}
		a.value = v
		a.reply(req, map[string]any{"value": fmt.Sprint(v)})

	case "readMemory":
		offset := int(args["offset"].(float64))
		count := int(args["count"].(float64))
		start := min(max(offset, 0), len(a.memory))
		end := min(start+count, len(a.memory))
		a.reply(req, map[string]any{
			"address":         fmt.Sprintf("0x%x", 0x1000+start),
			"data":            base64.StdEncoding.EncodeToString(a.memory[start:end]),
			"unreadableBytes": count - (end - start),
		})

	case "writeMemory":
		offset := int(args["offset"].(float64))
		data, err := base64.StdEncoding.DecodeString(args["data"].(string))
		if err != nil || offset < 0 || offset+len(data) > len(a.memory) {
			a.fail(req, "write out of range")
			return false
		}
		copy(a.memory[offset:], data)
		a.reply(req, map[string]any{"bytesWritten": len(data)})

	case "breakpointLocations":
		line := int(args["line"].(float64))
		a.reply(req, map[string]any{"breakpoints": []map[string]any{{"line": line, "column": 1}, {"line": line, "column": 5}}})

	case "evaluate":
		a.reply(req, map[string]any{"result": fmt.Sprintf("evaluated %v", args["expression"]), "variablesReference": 0})

	case "continue":
		a.reply(req, map[string]any{"allThreadsContinued": true})
		a.finish()

	case "next", "stepIn", "stepOut":
		a.reply(req, nil)
		a.event("stopped", map[string]any{"reason": "step", "threadId": 1})

	case "disconnect":
		a.reply(req, nil)
		return true

	default:
		a.fail(req, "unsupported request "+req.Command)
	}
	return false
}

func (a *adapter) finish() {
	a.event("output", map[string]any{"category": "stdout", "output": fmt.Sprintf("answer is %d\n", a.value)})
	a.event("exited", map[string]any{"exitCode": 0})
	a.event("terminated", map[string]any{})
}
