// Package dap implements Debug Adapter Protocol framing and a client that
// correlates requests with responses.
package dap

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

const (
	maxHeader = 8 * 1024
	maxBody   = 64 * 1024 * 1024
)

// Message is any incoming DAP message: a request, response or event.
type Message struct {
	Seq        int             `json:"seq"`
	Type       string          `json:"type"`
	Command    string          `json:"command,omitempty"`
	Event      string          `json:"event,omitempty"`
	RequestSeq int             `json:"request_seq,omitempty"`
	Success    bool            `json:"success,omitempty"`
	Message    string          `json:"message,omitempty"`
	Arguments  json.RawMessage `json:"arguments,omitempty"`
	Body       json.RawMessage `json:"body,omitempty"`
}

type request struct {
	Seq       int    `json:"seq"`
	Type      string `json:"type"`
	Command   string `json:"command"`
	Arguments any    `json:"arguments,omitempty"`
}

type response struct {
	Seq        int    `json:"seq"`
	Type       string `json:"type"`
	RequestSeq int    `json:"request_seq"`
	Command    string `json:"command"`
	Success    bool   `json:"success"`
	Message    string `json:"message,omitempty"`
	Body       any    `json:"body,omitempty"`
}

// ReverseHandler answers a request sent by the adapter. It returns the
// response body, or an error to reject the request.
type ReverseHandler func(Message) (any, error)

// Error is a failed request, either rejected by the adapter or lost in transport.
type Error struct {
	Command string
	Message string
}

func (e *Error) Error() string {
	return e.Command + ": " + e.Message
}

// ErrClosed is returned for requests made after the connection has closed.
var ErrClosed = errors.New("connection closed")

// ReadMessage reads one framed message.
func ReadMessage(r *bufio.Reader) (Message, error) {
	length := -1
	headerSize := 0

	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return Message{}, err
		}

		headerSize += len(line)
		if headerSize > maxHeader {
			return Message{}, errors.New("DAP header too large")
		}

		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}

		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return Message{}, fmt.Errorf("malformed DAP header %q", line)
		}
		if strings.EqualFold(strings.TrimSpace(key), "Content-Length") {
			n, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || n <= 0 || n > maxBody {
				return Message{}, fmt.Errorf("invalid Content-Length %q", value)
			}
			length = n
		}
	}

	if length < 0 {
		return Message{}, errors.New("missing Content-Length")
	}

	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return Message{}, err
	}

	var msg Message
	if err := json.Unmarshal(body, &msg); err != nil {
		return Message{}, fmt.Errorf("invalid DAP message: %w", err)
	}
	return msg, nil
}

// WriteMessage frames and writes one message.
func WriteMessage(w io.Writer, msg any) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

// Client sends requests to a debug adapter and dispatches its events.
type Client struct {
	conn      io.ReadWriteCloser
	onEvent   func(Message)
	onRequest ReverseHandler

	traceMu    sync.Mutex
	trace      io.Writer
	traceLabel string

	writeMu sync.Mutex

	mu      sync.Mutex
	seq     int
	pending map[int]chan Message
	err     error

	done chan struct{}
}

// NewClient starts reading from conn. onEvent is called from the reader
// goroutine for every event, in arrival order. onRequest answers reverse
// requests such as runInTerminal; when nil they are all rejected.
func NewClient(conn io.ReadWriteCloser, onEvent func(Message), onRequest ReverseHandler, opts ...Option) *Client {
	c := &Client{
		conn:      conn,
		onEvent:   onEvent,
		onRequest: onRequest,
		pending:   map[int]chan Message{},
		done:      make(chan struct{}),
	}
	for _, opt := range opts {
		opt(c)
	}
	go c.read()
	return c
}

// Option configures a Client.
type Option func(*Client)

// WithTrace logs every message as a JSON line {"session": label,
// "direction": "client" or "adapter", "message": ...}, like VS Code's
// adapter trace.
func WithTrace(w io.Writer, label string) Option {
	return func(c *Client) { c.trace, c.traceLabel = w, label }
}

func (c *Client) traceMessage(direction string, msg any) {
	if c.trace == nil {
		return
	}
	line, err := json.Marshal(map[string]any{"session": c.traceLabel, "direction": direction, "message": msg})
	if err != nil {
		return
	}
	c.traceMu.Lock()
	defer c.traceMu.Unlock()
	_, _ = c.trace.Write(append(line, '\n'))
}

// Done is closed once the connection has stopped reading.
func (c *Client) Done() <-chan struct{} {
	return c.done
}

// Err reports why the connection stopped.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Request sends a request and decodes the response body into result, which may be nil.
func (c *Client) Request(ctx context.Context, command string, args any, result any) error {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return &Error{command, c.err.Error()}
	}
	c.seq++
	seq := c.seq
	ch := make(chan Message, 1)
	c.pending[seq] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, seq)
		c.mu.Unlock()
	}()

	if err := c.send(request{Seq: seq, Type: "request", Command: command, Arguments: args}); err != nil {
		c.fail(err)
		return &Error{command, err.Error()}
	}

	select {
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return &Error{command, "request timed out"}
		}
		return &Error{command, ctx.Err().Error()}
	case <-c.done:
		// The reader delivers a response before closing done, so one may be waiting.
		select {
		case msg := <-ch:
			return decodeResponse(command, msg, result)
		default:
		}
		return &Error{command, c.Err().Error()}
	case msg := <-ch:
		return decodeResponse(command, msg, result)
	}
}

func decodeResponse(command string, msg Message, result any) error {
	if !msg.Success {
		return &Error{command, responseError(msg)}
	}
	if result != nil && len(msg.Body) > 0 {
		if err := json.Unmarshal(msg.Body, result); err != nil {
			return &Error{command, fmt.Sprintf("invalid response body: %v", err)}
		}
	}
	return nil
}

// Close closes the connection and fails pending requests.
func (c *Client) Close() error {
	c.fail(ErrClosed)
	return c.conn.Close()
}

func (c *Client) nextSeq() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	return c.seq
}

func (c *Client) send(msg any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.traceMessage("client", msg)
	return WriteMessage(c.conn, msg)
}

func (c *Client) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err == nil {
		c.err = err
	}
}

func (c *Client) read() {
	defer close(c.done)
	r := bufio.NewReaderSize(c.conn, 64*1024)

	for {
		msg, err := ReadMessage(r)
		if err == nil {
			c.traceMessage("adapter", msg)
		}
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				err = errors.New("debug adapter closed the connection")
			}
			c.fail(err)
			return
		}

		switch msg.Type {
		case "event":
			c.onEvent(msg)
		case "response":
			c.mu.Lock()
			ch := c.pending[msg.RequestSeq]
			c.mu.Unlock()
			if ch != nil {
				ch <- msg
			}
		case "request":
			go c.answer(msg)
		}
	}
}

func (c *Client) answer(req Message) {
	resp := response{Seq: c.nextSeq(), Type: "response", RequestSeq: req.Seq, Command: req.Command, Success: true}
	var body any
	err := fmt.Errorf("client does not support %s", req.Command)
	if c.onRequest != nil {
		body, err = c.onRequest(req)
	}
	if err != nil {
		resp.Success = false
		resp.Message = err.Error()
	} else {
		resp.Body = body
	}
	_ = c.send(resp)
}

func responseError(msg Message) string {
	var body struct {
		Error *struct {
			Format    string            `json:"format"`
			Variables map[string]string `json:"variables"`
		} `json:"error"`
	}
	if len(msg.Body) > 0 && json.Unmarshal(msg.Body, &body) == nil && body.Error != nil && body.Error.Format != "" {
		text := body.Error.Format
		for k, v := range body.Error.Variables {
			text = strings.ReplaceAll(text, "{"+k+"}", v)
		}
		return text
	}
	if msg.Message != "" {
		return msg.Message
	}
	return "request failed"
}
