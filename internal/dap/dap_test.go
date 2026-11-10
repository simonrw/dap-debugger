package dap

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestFraming(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteMessage(&buf, map[string]any{"seq": 1, "type": "event", "event": "output", "body": map[string]string{"output": "héllo"}}); err != nil {
		t.Fatal(err)
	}

	msg, err := ReadMessage(bufio.NewReader(&buf))
	if err != nil {
		t.Fatal(err)
	}
	if msg.Type != "event" || msg.Event != "output" || !strings.Contains(string(msg.Body), "héllo") {
		t.Fatalf("got %+v", msg)
	}
}

func TestReadMessageRejectsBadFrames(t *testing.T) {
	for name, frame := range map[string]string{
		"no length":   "X-Other: 1\r\n\r\n{}",
		"bad length":  "Content-Length: abc\r\n\r\n{}",
		"bad json":    "Content-Length: 3\r\n\r\n{x}",
		"short body":  "Content-Length: 10\r\n\r\n{}",
		"bad header":  "garbage\r\n\r\n{}",
		"zero length": "Content-Length: 0\r\n\r\n",
	} {
		if _, err := ReadMessage(bufio.NewReader(strings.NewReader(frame))); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// fakeAdapter answers requests on the other end of a pipe.
func fakeAdapter(t *testing.T, conn net.Conn, handle func(req Message, reply func(any))) {
	t.Helper()
	go func() {
		r := bufio.NewReader(conn)
		for {
			msg, err := ReadMessage(r)
			if err != nil {
				return
			}
			handle(msg, func(v any) { _ = WriteMessage(conn, v) })
		}
	}()
}

func TestClientCorrelatesResponses(t *testing.T) {
	client, server := net.Pipe()
	events := make(chan Message, 10)
	c := NewClient(client, func(m Message) { events <- m }, nil)
	defer c.Close()

	fakeAdapter(t, server, func(req Message, reply func(any)) {
		switch req.Type {
		case "request":
			var args struct{ Value int }
			_ = json.Unmarshal(req.Arguments, &args)
			reply(map[string]any{"seq": 100, "type": "event", "event": "ping", "body": map[string]int{"n": args.Value}})
			if req.Command == "fail" {
				reply(map[string]any{"seq": 101, "type": "response", "request_seq": req.Seq, "command": req.Command, "success": false,
					"body": map[string]any{"error": map[string]any{"format": "bad {thing}", "variables": map[string]string{"thing": "input"}}}})
				return
			}
			reply(map[string]any{"seq": 101, "type": "response", "request_seq": req.Seq, "command": req.Command, "success": true, "body": map[string]int{"double": args.Value * 2}})
		case "response":
			if req.Success {
				t.Errorf("reverse request should be rejected: %+v", req)
			}
			reply(map[string]any{"seq": 102, "type": "event", "event": "rejected"})
		}
	})

	ctx := context.Background()
	var body struct{ Double int }
	if err := c.Request(ctx, "double", map[string]int{"value": 21}, &body); err != nil {
		t.Fatal(err)
	}
	if body.Double != 42 {
		t.Fatalf("got %d", body.Double)
	}

	if e := <-events; e.Event != "ping" {
		t.Fatalf("event %+v", e)
	}

	err := c.Request(ctx, "fail", nil, nil)
	var dapErr *Error
	if !errors.As(err, &dapErr) || dapErr.Message != "bad input" {
		t.Fatalf("got %v", err)
	}
	<-events

	// A reverse request from the adapter is answered with a failure.
	_ = WriteMessage(server, map[string]any{"seq": 5, "type": "request", "command": "runInTerminal"})
	select {
	case e := <-events:
		if e.Event != "rejected" {
			t.Fatalf("event %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reverse request not answered")
	}
}

func TestClientFailsPendingRequestsOnClose(t *testing.T) {
	client, server := net.Pipe()
	c := NewClient(client, func(Message) {}, nil)
	fakeAdapter(t, server, func(Message, func(any)) {
		server.Close()
	})

	err := c.Request(context.Background(), "initialize", nil, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	<-c.Done()

	if err := c.Request(context.Background(), "threads", nil, nil); err == nil {
		t.Fatal("expected an error after close")
	}
}

func TestClientTimeout(t *testing.T) {
	client, server := net.Pipe()
	c := NewClient(client, func(Message) {}, nil)
	defer c.Close()
	fakeAdapter(t, server, func(Message, func(any)) {})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := c.Request(ctx, "threads", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("got %v", err)
	}
}

// TestResponseBeforeClose returns a reply even when the adapter closes the
// connection straight after sending it.
func TestResponseBeforeClose(t *testing.T) {
	for i := 0; i < 50; i++ {
		client, server := net.Pipe()
		c := NewClient(client, func(Message) {}, nil)
		fakeAdapter(t, server, func(req Message, reply func(any)) {
			reply(map[string]any{"seq": 1, "type": "response", "request_seq": req.Seq, "command": req.Command, "success": true})
			server.Close()
		})
		if err := c.Request(context.Background(), "configurationDone", nil, nil); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		c.Close()
	}
}

func TestReverseRequestHandler(t *testing.T) {
	client, server := net.Pipe()
	c := NewClient(client, func(Message) {}, func(req Message) (any, error) {
		return map[string]int{"processId": 42}, nil
	})
	defer c.Close()

	_ = WriteMessage(server, map[string]any{"seq": 1, "type": "request", "command": "runInTerminal", "arguments": map[string]any{}})
	msg, err := ReadMessage(bufio.NewReader(server))
	if err != nil {
		t.Fatal(err)
	}
	if !msg.Success || !strings.Contains(string(msg.Body), `"processId":42`) {
		t.Fatalf("got %+v %s", msg, msg.Body)
	}
}
