// Package session runs one debug session: it starts or connects to the adapter,
// performs the DAP initialization handshake and tears everything down again.
package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	godap "github.com/google/go-dap"

	"github.com/creack/pty"

	"github.com/simonrw/dap-debugger/internal/dap"
	"github.com/simonrw/dap-debugger/internal/launch"
)

// Event is a DAP event, or one the session makes itself: "output" for adapter
// process output (a DAP output body), "terminal" for the debuggee's terminal
// output (TerminalData) and "startDebugging" for a child session request.
type Event struct {
	Name string
	Body json.RawMessage
}

// Options tune timeouts.
type Options struct {
	RequestTimeout time.Duration
	StartupTimeout time.Duration
	// Trace appends every DAP message to this file as JSON lines.
	Trace string
}

// Session is one connection to a debug adapter. It cannot be restarted.
type Session struct {
	Plan *launch.Plan

	opts    Options
	events  chan Event
	started atomic.Bool
	// restarting tells the adapter, when closing, that a new session follows.
	restarting atomic.Bool

	mu          sync.Mutex
	client      *dap.Client
	conn        io.ReadWriteCloser
	proc        *exec.Cmd
	caps        godap.Capabilities
	initialized chan struct{}
	initOnce    sync.Once
	// terminated is closed when the adapter reports the debuggee terminated.
	terminated     chan struct{}
	terminatedOnce sync.Once
	bpLocks        map[string]*sync.Mutex
	terminals      []*terminal
	ttys           []*os.File
	traceFile      *os.File
	address        string
	// input is where WriteStdin sends the debuggee's input.
	input        io.Writer
	bpVersions   map[string]int
	closing      bool
	closeOnce    sync.Once
	finished     chan struct{}
	finishErr    error
	drainers     sync.WaitGroup
	cancelStart  context.CancelFunc
	startContext context.Context
}

// New creates a session without starting it, so the caller can consume events first.
func New(plan *launch.Plan, opts Options) *Session {
	if opts.RequestTimeout == 0 {
		opts.RequestTimeout = 15 * time.Second
	}
	if opts.StartupTimeout == 0 {
		opts.StartupTimeout = 60 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Session{
		Plan:         plan,
		opts:         opts,
		events:       make(chan Event, 4096),
		initialized:  make(chan struct{}),
		terminated:   make(chan struct{}),
		bpLocks:      map[string]*sync.Mutex{},
		bpVersions:   map[string]int{},
		finished:     make(chan struct{}),
		startContext: ctx,
		cancelStart:  cancel,
	}
}

// Next blocks for the next event. It returns false once the session has
// finished and every event has been delivered.
func (s *Session) Next() (Event, bool) {
	select {
	case e := <-s.events:
		return e, true
	case <-s.finished:
		select {
		case e := <-s.events:
			return e, true
		default:
			return Event{}, false
		}
	}
}

// Finished is closed once the session has been torn down.
func (s *Session) Finished() <-chan struct{} {
	return s.finished
}

// Capabilities returns what the adapter reported during initialization.
func (s *Session) Capabilities() godap.Capabilities {
	s.mu.Lock()
	defer s.mu.Unlock()
	caps := s.caps
	caps.ExceptionBreakpointFilters = slices.Clone(caps.ExceptionBreakpointFilters)
	return caps
}

// Setup is the configuration sent between the initialized event and configurationDone.
type Setup struct {
	Breakpoints map[string][]godap.SourceBreakpoint
	Functions   []godap.FunctionBreakpoint
	// ExceptionFilters chooses enabled filters once the adapter's filters are known.
	ExceptionFilters func([]godap.ExceptionBreakpointsFilter) []string
}

// Started is the result of a successful Start.
type Started struct {
	Breakpoints map[string][]godap.Breakpoint
	Functions   []godap.Breakpoint
}

// Start connects to the adapter and runs the launch or attach handshake. On
// failure the session is closed.
func (s *Session) Start(setup Setup) (*Started, error) {
	ctx, cancel := context.WithTimeout(s.startContext, s.opts.StartupTimeout)
	defer cancel()

	started, err := s.start(ctx, setup)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = fmt.Errorf("startup timed out: %w", err)
		}
		s.Close(s.Plan.Launched())
		return nil, err
	}
	s.started.Store(true)
	return started, nil
}

func (s *Session) start(ctx context.Context, setup Setup) (*Started, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}

	var opts []dap.Option
	if s.opts.Trace != "" {
		trace, err := os.OpenFile(s.opts.Trace, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return nil, fmt.Errorf("open trace file: %w", err)
		}
		s.traceFile = trace
		opts = append(opts, dap.WithTrace(trace, s.Plan.Name))
	}
	client := dap.NewClient(conn, s.onEvent, s.onRequest, opts...)
	s.mu.Lock()
	s.client = client
	s.conn = conn
	closing := s.closing
	s.mu.Unlock()
	if closing {
		client.Close()
		return nil, errors.New("session closed during startup")
	}
	go s.watchClient(client)

	var caps godap.Capabilities
	err = s.request(ctx, "initialize", map[string]any{
		"clientID":        "dap-debugger",
		"clientName":      "dap-debugger",
		"adapterID":       s.Plan.AdapterID,
		"pathFormat":      "path",
		"linesStartAt1":   true,
		"columnsStartAt1": true,
		// The same capabilities VS Code reports.
		"supportsVariableType":                true,
		"supportsVariablePaging":              true,
		"supportsRunInTerminalRequest":        true,
		"supportsStartDebuggingRequest":       true,
		"supportsProgressReporting":           true,
		"supportsInvalidatedEvent":            true,
		"supportsMemoryReferences":            true,
		"supportsMemoryEvent":                 true,
		"supportsArgsCanBeInterpretedByShell": true,
		"supportsANSIStyling":                 true,
		"locale":                              "en",
	}, &caps)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.caps = caps
	s.mu.Unlock()

	// The launch response may only arrive after configurationDone, so it must not be awaited first.
	launchDone := make(chan error, 1)
	go func() {
		launchDone <- s.request(ctx, s.Plan.Request, s.Plan.Arguments, nil)
	}()

	select {
	case <-s.initialized:
	case err := <-launchDone:
		if err != nil {
			return nil, err
		}
		select {
		case <-s.initialized:
		case <-ctx.Done():
			return nil, errors.New("adapter never sent the initialized event")
		}
		launchDone <- nil
	case <-ctx.Done():
		return nil, errors.New("adapter never sent the initialized event")
	}

	started, err := s.configure(ctx, caps, setup)
	if err != nil {
		return nil, err
	}

	if err := <-launchDone; err != nil {
		return nil, err
	}
	return started, nil
}

func (s *Session) connect(ctx context.Context) (io.ReadWriteCloser, error) {
	plan := s.Plan
	var dialer net.Dialer

	switch plan.Transport {
	case launch.Dial:
		conn, err := dialer.DialContext(ctx, "tcp", plan.Address)
		if err != nil {
			return nil, fmt.Errorf("connect to debug adapter at %s: %w", plan.Address, err)
		}
		s.setAddress(plan.Address)
		return conn, nil

	case launch.Stdio:
		cmd := s.command(plan.Command)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			return nil, err
		}
		if err := s.spawn(cmd); err != nil {
			return nil, err
		}
		// The adapter's stderr is its own logging; program output arrives as
		// output events. It only helps to diagnose a failed start.
		s.drain(stderr, "stderr", true)
		return &pipeConn{ReadCloser: stdout, WriteCloser: stdin}, nil

	case launch.Listen:
		return s.listen(ctx)

	case launch.Reverse:
		var lc net.ListenConfig
		listener, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		defer listener.Close()

		cmd := s.command(substitute(plan.Command, launch.AddressPlaceholder, listener.Addr().String()))

		var outDone, errDone <-chan struct{}
		if plan.Stdin {
			// The debuggee inherits the adapter's terminal, as when VS Code
			// runs the adapter in its integrated terminal.
			cmd.SysProcAttr = nil
			tty, err := s.startPTY(cmd)
			if err != nil {
				return nil, err
			}
			outDone = s.drain(tty, "terminal", false)
			errDone = outDone
		} else {
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				return nil, err
			}
			stderr, err := cmd.StderrPipe()
			if err != nil {
				return nil, err
			}
			if err := s.spawn(cmd); err != nil {
				return nil, err
			}
			outDone = s.drain(stdout, "stdout", false)
			errDone = s.drain(stderr, "stderr", false)
		}

		accepted := make(chan net.Conn)
		abandoned := make(chan struct{})
		defer close(abandoned)
		go func() {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			select {
			case accepted <- conn:
			case <-abandoned:
				conn.Close()
			}
		}()

		exited := make(chan struct{})
		go func() {
			<-outDone
			<-errDone
			close(exited)
		}()

		select {
		case conn := <-accepted:
			return conn, nil
		case <-exited:
			return nil, fmt.Errorf("%s exited before connecting", plan.Command[0])
		case <-ctx.Done():
			return nil, fmt.Errorf("%s did not connect: %w", plan.Command[0], ctx.Err())
		}
	}
	return nil, fmt.Errorf("unknown transport %d", plan.Transport)
}

func (s *Session) command(args []string) *exec.Cmd {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = s.Plan.Dir
	if len(s.Plan.Env) > 0 {
		cmd.Env = append(os.Environ(), s.Plan.Env...)
	}
	detach(cmd)
	return cmd
}

func (s *Session) setAddress(address string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.address = address
}

// Address is where the adapter listens, for adapters reached over TCP.
// Child sessions of such adapters connect to the same address.
func (s *Session) Address() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.address
}

func substitute(args []string, placeholder, value string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = strings.ReplaceAll(a, placeholder, value)
	}
	return out
}

// listen starts an adapter that listens on a port we choose, and connects
// once it accepts connections.
func (s *Session) listen(ctx context.Context) (io.ReadWriteCloser, error) {
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	port := reservation.Addr().(*net.TCPAddr).Port
	reservation.Close()

	cmd := s.command(substitute(s.Plan.Command, launch.PortPlaceholder, strconv.Itoa(port)))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := s.spawn(cmd); err != nil {
		return nil, err
	}
	outDone := s.drain(stdout, "stdout", false)
	s.drain(stderr, "stderr", false)

	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	var dialer net.Dialer
	for {
		conn, err := dialer.DialContext(ctx, "tcp", address)
		if err == nil {
			s.setAddress(address)
			return conn, nil
		}
		select {
		case <-outDone:
			return nil, fmt.Errorf("%s exited before accepting connections", s.Plan.Command[0])
		case <-ctx.Done():
			return nil, fmt.Errorf("%s did not listen on port %d: %w", s.Plan.Command[0], port, ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (s *Session) spawn(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start debug adapter: %w", err)
	}
	return s.adopt(cmd)
}

// startPTY starts the adapter on a pseudo-terminal that becomes the
// debuggee's input and output.
func (s *Session) startPTY(cmd *exec.Cmd) (*os.File, error) {
	tty, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		return nil, fmt.Errorf("start debug adapter: %w", err)
	}
	if err := s.adopt(cmd); err != nil {
		tty.Close()
		return nil, err
	}
	s.mu.Lock()
	s.input = tty
	s.ttys = append(s.ttys, tty)
	s.mu.Unlock()
	return tty, nil
}

// adopt records a started adapter process so Close reaps it.
func (s *Session) adopt(cmd *exec.Cmd) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		signalGroup(cmd, true)
		_ = cmd.Wait()
		return errors.New("session closed during startup")
	}
	s.proc = cmd
	return nil
}

// drain forwards adapter process output to the debug console, or only until
// the session has started when startupOnly is set.
func (s *Session) drain(r io.Reader, category string, startupOnly bool) <-chan struct{} {
	done := make(chan struct{})
	// Adding after Close has started waiting would misuse the WaitGroup.
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		close(done)
		return done
	}
	s.drainers.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.drainers.Done()
		defer close(done)
		// Chunks rather than lines, so prompts without a newline still show.
		buf := make([]byte, 32*1024)
		for {
			n, err := r.Read(buf)
			if n > 0 && !(startupOnly && s.started.Load()) {
				if category == "terminal" {
					s.terminalOutput(buf[:n])
				} else {
					s.output(category, string(buf[:n]))
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return done
}

// TerminalData is the body of a "terminal" event: raw output from the
// debuggee's pseudo-terminal, escape sequences included.
type TerminalData struct {
	Data []byte `json:"data"`
}

func (s *Session) terminalOutput(data []byte) {
	body, _ := json.Marshal(TerminalData{Data: data})
	s.emit(Event{Name: "terminal", Body: body})
}

func (s *Session) output(category, text string) {
	body, _ := json.Marshal(map[string]string{"category": category, "output": text})
	s.emit(Event{Name: "output", Body: body})
}

func (s *Session) emit(e Event) {
	select {
	case s.events <- e:
	case <-s.finished:
	}
}

func (s *Session) onEvent(msg dap.Message) {
	switch msg.Event {
	case "initialized":
		s.initOnce.Do(func() { close(s.initialized) })
	case "capabilities":
		s.mu.Lock()
		mergeCapabilities(&s.caps, msg.Body)
		s.mu.Unlock()
	}

	s.emit(Event{Name: msg.Event, Body: msg.Body})

	if msg.Event == "terminated" {
		s.terminatedOnce.Do(func() { close(s.terminated) })
		go s.Close(s.Plan.Launched())
	}
}

func mergeCapabilities(caps *godap.Capabilities, body json.RawMessage) {
	var update struct {
		Capabilities json.RawMessage `json:"capabilities"`
	}
	if json.Unmarshal(body, &update) == nil && len(update.Capabilities) > 0 {
		// Decoding reuses a slice's backing array, which copies may share.
		caps.ExceptionBreakpointFilters = slices.Clone(caps.ExceptionBreakpointFilters)
		_ = json.Unmarshal(update.Capabilities, caps)
	}
}

func (s *Session) watchClient(client *dap.Client) {
	<-client.Done()
	s.mu.Lock()
	closing := s.closing
	s.mu.Unlock()
	if !closing {
		s.output("stderr", fmt.Sprintf("Debug adapter connection lost: %v\n", client.Err()))
		go s.Close(false)
	}
}

func (s *Session) request(ctx context.Context, command string, args any, result any) error {
	s.mu.Lock()
	client := s.client
	s.mu.Unlock()
	if client == nil {
		return &dap.Error{Command: command, Message: "not connected"}
	}
	return client.Request(ctx, command, args, result)
}

// Request sends a request with the standard timeout.
func (s *Session) Request(command string, args any, result any) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.opts.RequestTimeout)
	defer cancel()
	return s.request(ctx, command, args, result)
}

// SetBreakpoints replaces the breakpoints in one source file. Calls for the
// same path are serialized, and a call whose version is older than one already
// sent is dropped and reports stale.
func (s *Session) SetBreakpoints(path string, bps []godap.SourceBreakpoint, version int) (records []godap.Breakpoint, stale bool, err error) {
	stale, err = s.latest("source:"+path, version, func(ctx context.Context) error {
		var err error
		records, err = s.setBreakpoints(ctx, path, bps)
		return err
	})
	return records, stale, err
}

// Reconfigure repeats the configuration handshake, for an adapter that sends
// the initialized event again, as Delve does after a restart request.
func (s *Session) Reconfigure(setup Setup) (*Started, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.opts.StartupTimeout)
	defer cancel()
	return s.configure(ctx, s.Capabilities(), setup)
}

// configure sends the breakpoints and exception filters, then configurationDone.
func (s *Session) configure(ctx context.Context, caps godap.Capabilities, setup Setup) (*Started, error) {
	started := &Started{Breakpoints: map[string][]godap.Breakpoint{}}
	// A file the adapter rejects, such as one in another language, must not
	// stop the session from starting.
	for path, bps := range setup.Breakpoints {
		records, err := s.setBreakpoints(ctx, path, bps)
		if err != nil {
			if ctx.Err() != nil {
				return nil, err
			}
			s.output("stderr", fmt.Sprintf("Breakpoints in %s: %v\n", path, err))
			continue
		}
		started.Breakpoints[path] = records
	}

	// Like VS Code, set every kind of breakpoint the adapter supports, even
	// when empty, so a debug server reused from an earlier client is reset.
	if caps.SupportsFunctionBreakpoints {
		records, _, err := s.SetFunctionBreakpoints(setup.Functions, 0)
		if err != nil {
			s.output("stderr", fmt.Sprintf("setFunctionBreakpoints: %v\n", err))
		}
		started.Functions = records
	}
	if caps.SupportsInstructionBreakpoints {
		_ = s.request(ctx, "setInstructionBreakpoints", map[string]any{"breakpoints": []any{}}, nil)
	}

	if len(caps.ExceptionBreakpointFilters) > 0 && setup.ExceptionFilters != nil {
		filters := setup.ExceptionFilters(caps.ExceptionBreakpointFilters)
		if err := s.request(ctx, "setExceptionBreakpoints", map[string]any{"filters": filters}, nil); err != nil {
			s.output("stderr", fmt.Sprintf("setExceptionBreakpoints: %v\n", err))
		}
	}

	if caps.SupportsDataBreakpoints {
		_ = s.request(ctx, "setDataBreakpoints", map[string]any{"breakpoints": []any{}}, nil)
	}

	if caps.SupportsConfigurationDoneRequest {
		if err := s.request(ctx, "configurationDone", nil, nil); err != nil {
			return nil, err
		}
	}
	return started, nil
}

// SetFunctionBreakpoints replaces all function breakpoints, with the same
// ordering guarantee as SetBreakpoints.
func (s *Session) SetFunctionBreakpoints(bps []godap.FunctionBreakpoint, version int) (records []godap.Breakpoint, stale bool, err error) {
	if bps == nil {
		bps = []godap.FunctionBreakpoint{}
	}
	stale, err = s.latest("functions", version, func(ctx context.Context) error {
		var body godap.SetFunctionBreakpointsResponseBody
		err := s.request(ctx, "setFunctionBreakpoints", map[string]any{"breakpoints": bps}, &body)
		records = body.Breakpoints
		return err
	})
	return records, stale, err
}

// SetExceptionFilters replaces the enabled exception filters, with the same
// ordering guarantee as SetBreakpoints.
func (s *Session) SetExceptionFilters(filters []string, version int) (stale bool, err error) {
	return s.latest("exceptions", version, func(ctx context.Context) error {
		return s.request(ctx, "setExceptionBreakpoints", map[string]any{"filters": filters}, nil)
	})
}

// latest serializes updates with the same key and drops one whose version is
// older than an update already sent, so the adapter ends up with the newest.
func (s *Session) latest(key string, version int, send func(context.Context) error) (stale bool, err error) {
	s.mu.Lock()
	lock := s.bpLocks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		s.bpLocks[key] = lock
	}
	s.mu.Unlock()

	lock.Lock()
	defer lock.Unlock()

	s.mu.Lock()
	if version < s.bpVersions[key] {
		s.mu.Unlock()
		return true, nil
	}
	s.bpVersions[key] = version
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), s.opts.RequestTimeout)
	defer cancel()
	return false, send(ctx)
}

// SetSourceBreakpoints replaces the breakpoints in a source the adapter
// provided by reference, with the same ordering guarantee as SetBreakpoints.
func (s *Session) SetSourceBreakpoints(src godap.Source, bps []godap.SourceBreakpoint, version int) (records []godap.Breakpoint, stale bool, err error) {
	stale, err = s.latest(fmt.Sprintf("ref:%d", src.SourceReference), version, func(ctx context.Context) error {
		var err error
		records, err = s.sendBreakpoints(ctx, src, bps)
		return err
	})
	return records, stale, err
}

func (s *Session) setBreakpoints(ctx context.Context, path string, bps []godap.SourceBreakpoint) ([]godap.Breakpoint, error) {
	return s.sendBreakpoints(ctx, godap.Source{Name: filepath.Base(path), Path: path}, bps)
}

func (s *Session) sendBreakpoints(ctx context.Context, src godap.Source, bps []godap.SourceBreakpoint) ([]godap.Breakpoint, error) {
	if bps == nil {
		bps = []godap.SourceBreakpoint{}
	}
	// lines is deprecated but still sent, as VS Code does, for older adapters.
	lines := make([]int, len(bps))
	for i, bp := range bps {
		lines[i] = bp.Line
	}
	var body godap.SetBreakpointsResponseBody
	err := s.request(ctx, "setBreakpoints", map[string]any{
		"source":         src,
		"lines":          lines,
		"breakpoints":    bps,
		"sourceModified": false,
	}, &body)
	return body.Breakpoints, err
}

// Close disconnects from the adapter, asking it to terminate the debuggee when
// terminate is set, and reaps any adapter process this session started. It is
// safe to call more than once and from any goroutine; it returns once the
// session has finished.
func (s *Session) Close(terminate bool) {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closing = true
		client := s.client
		conn := s.conn
		proc := s.proc
		s.mu.Unlock()

		s.cancelStart()

		if client != nil {
			select {
			case <-client.Done():
			default:
				// As VS Code does, ask the debuggee to terminate gracefully first
				// when the adapter can, then disconnect.
				if terminate && s.Capabilities().SupportsTerminateRequest {
					select {
					case <-s.terminated:
					default:
						ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
						if client.Request(ctx, "terminate", map[string]any{"restart": s.restarting.Load()}, nil) == nil {
							select {
							case <-s.terminated:
							case <-ctx.Done():
							}
						}
						cancel()
					}
				}

				// Like VS Code: a stop leaves terminateDebuggee to the adapter's
				// default, while a disconnect says not to terminate, to
				// adapters that support the argument.
				args := map[string]any{"restart": false}
				if s.Capabilities().SupportTerminateDebuggee {
					if !terminate {
						args["terminateDebuggee"] = false
					} else if !s.Plan.Launched() {
						args["terminateDebuggee"] = true
					}
				}
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				_ = client.Request(ctx, "disconnect", args, nil)
				cancel()
			}

			// Closing only our side lets an adapter we own finish writing and
			// exit; it fails noisily when the whole connection closes under it.
			// A shared debug server must not be half-closed: debugpy then stops
			// accepting new clients.
			if conn, ok := conn.(interface{ CloseWrite() error }); ok && proc != nil {
				_ = conn.CloseWrite()
				select {
				case <-client.Done():
				case <-time.After(2 * time.Second):
				}
			}
			client.Close()
		}

		if proc != nil {
			reap(proc)
		}

		s.mu.Lock()
		terminals := s.terminals
		s.mu.Unlock()
		// A debuggee started with runInTerminal is ours, and its output pipes
		// would otherwise keep the session open.
		for _, t := range terminals {
			stopTerminal(t)
		}

		s.mu.Lock()
		ttys := s.ttys
		s.mu.Unlock()
		for _, tty := range ttys {
			tty.Close()
		}

		s.drainers.Wait()
		if s.traceFile != nil {
			s.traceFile.Close()
		}
		close(s.finished)
	})
	<-s.finished
}

// CloseForRestart closes the session like Close, telling the adapter a new
// session follows, as VS Code does when restarting without a restart request.
func (s *Session) CloseForRestart() {
	s.restarting.Store(true)
	s.Close(s.Plan.Launched())
}

// Restart asks the adapter to restart the debuggee with new launch or attach
// arguments, keeping the session, for adapters that support it.
func (s *Session) Restart(arguments map[string]any) error {
	return s.Request("restart", map[string]any{"arguments": arguments}, nil)
}

// reap waits briefly for the adapter to exit by itself, then terminates its process group.
func reap(cmd *exec.Cmd) {
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()

	select {
	case <-done:
		return
	case <-time.After(2 * time.Second):
	}

	signalGroup(cmd, false)
	select {
	case <-done:
		return
	case <-time.After(2 * time.Second):
	}

	signalGroup(cmd, true)
	<-done
}

// pipeConn joins an adapter's stdout and stdin into one connection.
type pipeConn struct {
	io.ReadCloser
	io.WriteCloser
}

func (p *pipeConn) CloseWrite() error {
	return p.WriteCloser.Close()
}

func (p *pipeConn) Close() error {
	p.WriteCloser.Close()
	return p.ReadCloser.Close()
}

// terminal is a debuggee the adapter asked us to start with runInTerminal.
// Pipes stand in for the terminal: output goes to the debug console and
// WriteStdin feeds its input.
type terminal struct {
	cmd  *exec.Cmd
	done chan struct{}
}

func (s *Session) onRequest(msg dap.Message) (any, error) {
	switch msg.Command {
	case "runInTerminal":
	case "startDebugging":
		// The UI starts the child session; the adapter only needs to know it will.
		s.emit(Event{Name: "startDebugging", Body: msg.Arguments})
		return nil, nil
	default:
		return nil, fmt.Errorf("client does not support %s", msg.Command)
	}

	var args godap.RunInTerminalRequestArguments
	if err := json.Unmarshal(msg.Arguments, &args); err != nil || len(args.Args) == 0 {
		return nil, errors.New("runInTerminal needs a command")
	}

	cmd := exec.Command(args.Args[0], args.Args[1:]...)
	if args.ArgsCanBeInterpretedByShell {
		// The adapter has written the arguments for a shell, quoting included.
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}
		cmd = exec.Command(shell, "-c", strings.Join(args.Args, " "))
	}
	cmd.Dir = args.Cwd
	cmd.Env = mergeEnv(os.Environ(), args.Env)

	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return nil, errors.New("session is closing")
	}
	// A pseudo-terminal, so programs that need a TTY (password prompts,
	// isatty checks) behave as in VS Code's integrated terminal.
	tty, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	t := &terminal{cmd: cmd, done: make(chan struct{})}
	s.terminals = append(s.terminals, t)
	s.ttys = append(s.ttys, tty)
	s.input = tty
	s.mu.Unlock()

	// If Close began meanwhile, drain skips reading and Close stops the
	// process through stopTerminal.
	outDone := s.drain(tty, "terminal", false)
	go func() {
		_ = cmd.Wait()
		// The reader sees an error once the program's side closes; waiting
		// briefly for it keeps the last output.
		select {
		case <-outDone:
		case <-time.After(time.Second):
		}
		close(t.done)
	}()

	return godap.RunInTerminalResponseBody{ProcessId: cmd.Process.Pid}, nil
}

// mergeEnv applies runInTerminal environment changes; a null value unsets a variable.
func mergeEnv(base []string, changes map[string]interface{}) []string {
	env := map[string]string{}
	var order []string
	for _, kv := range base {
		k, v, _ := strings.Cut(kv, "=")
		if _, seen := env[k]; !seen {
			order = append(order, k)
		}
		env[k] = v
	}
	for k, v := range changes {
		if _, seen := env[k]; !seen {
			order = append(order, k)
		}
		switch v := v.(type) {
		case nil:
			delete(env, k)
		case string:
			env[k] = v
		default:
			env[k] = fmt.Sprint(v)
		}
	}

	out := make([]string, 0, len(env))
	for _, k := range order {
		if v, ok := env[k]; ok {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// stopTerminal gives a debuggee time to exit after disconnect, then terminates it.
func stopTerminal(t *terminal) {
	for _, kill := range []bool{false, true} {
		select {
		case <-t.done:
			return
		case <-time.After(2 * time.Second):
		}
		signalGroup(t.cmd, kill)
	}
	<-t.done
}

// HasTerminal reports whether the debuggee reads input from WriteStdin.
func (s *Session) HasTerminal() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.input != nil
}

// WriteInput sends raw bytes, such as encoded key presses, to the debuggee's terminal.
func (s *Session) WriteInput(p []byte) error {
	s.mu.Lock()
	input := s.input
	s.mu.Unlock()
	if input == nil {
		return errors.New("the program has no terminal")
	}
	_, err := input.Write(p)
	return err
}

// ResizeTerminal sets the size of the debuggee's terminal.
func (s *Session) ResizeTerminal(cols, rows int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, tty := range s.ttys {
		_ = pty.Setsize(tty, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	}
}

// WriteStdin sends a line to the debuggee's input.
func (s *Session) WriteStdin(line string) error {
	s.mu.Lock()
	input := s.input
	s.mu.Unlock()
	if input == nil {
		return errors.New("the program has no input; set \"console\": \"integratedTerminal\" in the configuration")
	}
	_, err := io.WriteString(input, line+"\n")
	return err
}
