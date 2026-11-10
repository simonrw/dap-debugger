package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	godap "github.com/google/go-dap"

	"github.com/simonrw/dap-debugger/internal/dap"
	"github.com/simonrw/dap-debugger/internal/launch"
	"github.com/simonrw/dap-debugger/internal/session"
	"github.com/simonrw/dap-debugger/internal/state"
)

type sessionEventMsg struct {
	sess  *session.Session
	event session.Event
}

type sessionEndedMsg struct{ sess *session.Session }

type startedMsg struct {
	sess    *session.Session
	started *session.Started
	sent    map[string][]state.Breakpoint
	// sentFunctions are the function breakpoints sent during startup.
	sentFunctions []godap.FunctionBreakpoint
	err           error
}

type threadsMsg struct {
	sess      *session.Session
	gen       int
	threads   []godap.Thread
	loadStack bool
	err       error
}

type stackMsg struct {
	sess       *session.Session
	gen        int
	threadID   int
	startFrame int
	frames     []godap.StackFrame
	total      int
	selectTop  bool
	err        error
}

// stackChunk is how many frames are loaded at a time, as in VS Code.
const stackChunk = 20

type scopesMsg struct {
	sess    *session.Session
	gen     int
	frameID int
	scopes  []godap.Scope
	err     error
}

type variablesMsg struct {
	sess *session.Session
	gen  int
	node *varNode
	vars []godap.Variable
	err  error
}

type watchMsg struct {
	sess    *session.Session
	gen     int
	frameID int
	node    *varNode
	body    godap.EvaluateResponseBody
	err     error
}

type evalMsg struct {
	sess *session.Session
	expr string
	body godap.EvaluateResponseBody
	err  error
}

type sourceMsg struct {
	sess    *session.Session
	src     godap.Source
	gen     int
	frameID int
	name    string
	line    int
	content string
	err     error
}

type breakpointsMsg struct {
	sess    *session.Session
	path    string
	version int
	sent    []state.Breakpoint
	records []godap.Breakpoint
	stale   bool
	err     error
}

type refBreakpointsMsg struct {
	sess    *session.Session
	ref     int
	lines   []int
	records []godap.Breakpoint
	err     error
}

type functionsMsg struct {
	sess    *session.Session
	version int
	sent    []godap.FunctionBreakpoint
	records []godap.Breakpoint
	err     error
}

type setVariableMsg struct {
	sess *session.Session
	gen  int
	node *varNode
	body godap.SetVariableResponseBody
	err  error
}

type exceptionMsg struct {
	sess *session.Session
	body godap.ExceptionInfoResponseBody
}

// executeErrMsg reports a step or continue the adapter rejected, which leaves
// the program paused.
type executeErrMsg struct {
	sess    *session.Session
	gen     int
	command string
	thread  int
	reason  string
	err     error
}

type requestErrMsg struct {
	sess    *session.Session
	command string
	err     error
}

type filesMsg struct {
	files []string
}

func waitEvent(s *session.Session) tea.Cmd {
	return func() tea.Msg {
		e, ok := s.Next()
		if !ok {
			return sessionEndedMsg{s}
		}
		return sessionEventMsg{s, e}
	}
}

func (m *Model) handleDebugMsg(msg tea.Msg) tea.Cmd {
	if cmd, ok := m.handleTaskMsg(msg); ok {
		return cmd
	}

	// Each session's messages are handled with that session's state.
	if s := msgSession(msg); s != nil {
		r := m.runFor(s)
		if r == nil {
			// Keep draining a finished session's events until it ends.
			if _, ok := msg.(sessionEventMsg); ok {
				return waitEvent(s)
			}
			return nil
		}
		return m.withRun(r, func() tea.Cmd { return m.handleSessionMsg(msg) })
	}
	return m.handleSessionMsg(msg)
}

// msgSession returns the session a message belongs to.
func msgSession(msg tea.Msg) *session.Session {
	switch msg := msg.(type) {
	case sessionEventMsg:
		return msg.sess
	case sessionEndedMsg:
		return msg.sess
	case startedMsg:
		return msg.sess
	case threadsMsg:
		return msg.sess
	case stackMsg:
		return msg.sess
	case scopesMsg:
		return msg.sess
	case variablesMsg:
		return msg.sess
	case watchMsg:
		return msg.sess
	case evalMsg:
		return msg.sess
	case sourceMsg:
		return msg.sess
	case breakpointsMsg:
		return msg.sess
	case functionsMsg:
		return msg.sess
	case setVariableMsg:
		return msg.sess
	case exceptionMsg:
		return msg.sess
	case executeErrMsg:
		return msg.sess
	case requestErrMsg:
		return msg.sess
	case dataInfoMsg:
		return msg.sess
	case dataBreakpointsMsg:
		return msg.sess
	case disasmMsg:
		return msg.sess
	case instructionBreakpointsMsg:
		return msg.sess
	case memoryMsg:
		return msg.sess
	case memoryWrittenMsg:
		return msg.sess
	case locationsMsg:
		return msg.sess
	case refBreakpointsMsg:
		return msg.sess
	}
	return nil
}

func (m *Model) handleSessionMsg(msg tea.Msg) tea.Cmd {
	if cmd, ok := m.handleDataMsg(msg); ok {
		return cmd
	}
	if cmd, ok := m.handleDisasmMsg(msg); ok {
		return cmd
	}
	if cmd, ok := m.handleMemoryMsg(msg); ok {
		return cmd
	}
	if cmd, ok := m.handleHintsMsg(msg); ok {
		return cmd
	}
	switch msg := msg.(type) {
	case sessionEventMsg:
		cmd := m.handleEvent(msg.sess, msg.event)
		return tea.Batch(cmd, waitEvent(msg.sess))

	case sessionEndedMsg:
		return m.sessionEnded(msg.sess)

	case startedMsg:
		return m.started(msg)

	case threadsMsg:
		if msg.sess != m.sess || msg.gen != m.gen {
			return nil
		}
		if msg.err != nil {
			// Background refreshes race with the program exiting.
			if msg.loadStack {
				m.console.println("stderr", msg.err.Error())
			}
			return nil
		}
		m.threads = msg.threads
		if len(m.threads) > 0 && !slices.ContainsFunc(m.threads, func(t godap.Thread) bool { return t.Id == m.threadID }) {
			m.threadID = m.threads[0].Id
		}
		if msg.loadStack && m.status == statusStopped && m.threadID != 0 {
			m.expanded[m.threadID] = true
			return m.fetchStack(m.threadID, true)
		}
		return nil

	case stackMsg:
		if msg.sess != m.sess || msg.gen != m.gen {
			return nil
		}
		if msg.err != nil {
			m.console.println("stderr", msg.err.Error())
			return nil
		}
		if msg.startFrame == 0 {
			m.frames[msg.threadID] = msg.frames
		} else {
			m.frames[msg.threadID] = append(m.frames[msg.threadID][:msg.startFrame], msg.frames...)
		}
		m.totals[msg.threadID] = msg.total
		frames := m.frames[msg.threadID]

		// After the top frame, load the rest of the first chunk.
		var rest tea.Cmd
		if msg.startFrame == 0 && len(msg.frames) == 1 && m.moreFrames(msg.threadID) {
			hasSource := frames[0].Source != nil && frames[0].PresentationHint != "subtle"
			rest = m.fetchFrames(msg.threadID, 1, stackChunk-1, msg.selectTop && !hasSource)
			if !hasSource {
				return rest
			}
		}
		if msg.selectTop && len(frames) > 0 {
			return tea.Batch(m.selectFrame(msg.threadID, bestFrame(frames)), rest)
		}
		return rest

	case scopesMsg:
		if msg.sess != m.sess || msg.gen != m.gen || msg.frameID != m.frameID {
			return nil
		}
		if msg.err != nil {
			m.console.println("stderr", msg.err.Error())
			return nil
		}
		return m.showScopes(msg.scopes)

	case variablesMsg:
		if msg.sess != m.sess || msg.gen != m.gen {
			return nil
		}
		return m.showVariables(msg.node, msg.vars, msg.err)

	case watchMsg:
		if msg.sess != m.sess || msg.gen != m.gen || msg.frameID != m.frameID {
			return nil
		}
		n := msg.node
		n.loading = false
		if msg.err != nil {
			n.err = shortError(msg.err)
			n.value, n.ref = "", 0
			return nil
		}
		n.err = ""
		n.value = msg.body.Result
		n.typ = msg.body.Type
		n.ref = msg.body.VariablesReference
		n.memRef = msg.body.MemoryReference
		n.children = nil
		if n.ref > 0 && m.expandedVars[n.key] {
			return m.fetchVariables(n)
		}
		n.expanded = false
		return nil

	case evalMsg:
		if msg.err != nil {
			m.console.println("stderr", shortError(msg.err))
		} else {
			m.console.println("result", msg.body.Result)
		}
		return nil

	case sourceMsg:
		if msg.sess != m.sess || msg.gen != m.gen || msg.frameID != m.frameID {
			return nil
		}
		if msg.err != nil {
			m.editor.clear(fmt.Sprintf("Source unavailable for %s: %s", msg.name, shortError(msg.err)))
			return nil
		}
		m.editor.load("", msg.name, msg.content)
		m.editor.src = msg.src
		m.editor.center(msg.line-1, m.layout().editor.h)
		return nil

	case breakpointsMsg:
		if msg.sess != m.sess || msg.stale || msg.version != m.bpVersions[msg.path] {
			return nil
		}
		if msg.err != nil {
			m.console.println("stderr", msg.err.Error())
			return nil
		}
		m.records[msg.path] = recordsByLine(msg.sent, msg.records)
		return nil

	case refBreakpointsMsg:
		if msg.sess != m.sess {
			return nil
		}
		if msg.err != nil {
			m.console.println("stderr", msg.err.Error())
			return nil
		}
		records := map[int]godap.Breakpoint{}
		for i, l := range msg.lines {
			if i < len(msg.records) {
				records[l] = msg.records[i]
			}
		}
		m.refRecords[msg.ref] = records
		return nil

	case functionsMsg:
		if msg.sess != m.sess || msg.version != m.fnVersion {
			return nil
		}
		if msg.err != nil {
			m.console.println("stderr", msg.err.Error())
			return nil
		}
		m.fnRecords = functionRecords(msg.sent, msg.records)
		return nil

	case setVariableMsg:
		if msg.sess != m.sess || msg.gen != m.gen {
			return nil
		}
		if msg.err != nil {
			m.console.println("stderr", shortError(msg.err))
			return nil
		}
		n := msg.node
		n.value, n.ref = msg.body.Value, msg.body.VariablesReference
		if msg.body.Type != "" {
			n.typ = msg.body.Type
		}
		n.children, n.expanded = nil, false
		return m.evaluateWatches()

	case exceptionMsg:
		if msg.sess == m.sess {
			text := msg.body.ExceptionId
			if msg.body.Description != "" {
				text += ": " + msg.body.Description
			}
			m.console.println("stderr", "Exception "+text)
			if msg.body.Details != nil && msg.body.Details.StackTrace != "" {
				m.console.println("stderr", msg.body.Details.StackTrace)
			}
		}
		return nil

	case executeErrMsg:
		if msg.sess != m.sess {
			return nil
		}
		m.console.println("stderr", msg.err.Error())
		if msg.gen == m.gen && m.status == statusRunning {
			return m.stopped(godap.StoppedEventBody{Reason: msg.reason, ThreadId: msg.thread})
		}
		return nil

	case requestErrMsg:
		if msg.sess == m.sess {
			m.console.println("stderr", msg.err.Error())
		}
		return nil

	case filesMsg:
		m.openQuickOpen(msg.files)
		return nil
	}
	return nil
}

// shortError drops the command name from adapter errors.
func shortError(err error) string {
	var dapErr *dap.Error
	if errors.As(err, &dapErr) {
		return dapErr.Message
	}
	return err.Error()
}

// startSession resolves the selected configuration and starts a new session.
func (m *Model) startSession() tea.Cmd {
	if m.sess != nil || m.task != nil {
		return nil
	}

	c := m.selectedCompound()
	if c == nil {
		m.compound, m.queue = nil, nil
		return m.startConfig(m.file.Configurations[m.configIndex])
	}

	m.compound, m.queue = c, nil
	for _, name := range c.Members() {
		cfg, _ := m.file.Find(name)
		m.queue = append(m.queue, cfg)
	}
	m.console.println("info", fmt.Sprintf("Starting compound %q", c.Name))
	if c.PreLaunchTask != "" {
		cmd := m.runTask(c.PreLaunchTask, m.vars())
		if cmd == nil {
			m.compound, m.queue, m.noDebug = nil, nil, false
		}
		return cmd
	}
	return m.startNext()
}

// startNext starts the next member of a compound.
func (m *Model) startNext() tea.Cmd {
	if len(m.queue) == 0 {
		m.compound = nil
		m.noDebug = false
		return nil
	}
	cfg := m.queue[0]
	m.queue = m.queue[1:]
	return m.startConfig(cfg)
}

// startConfig resolves a configuration and starts it, after its preLaunchTask.
func (m *Model) startConfig(cfg launch.Config) tea.Cmd {
	vars := m.vars()
	plan, err := launch.Prepare(cfg, vars)
	if err != nil {
		m.console.println("stderr", err.Error())
		m.compound, m.queue, m.noDebug = nil, nil, false
		return nil
	}

	m.console.println("info", fmt.Sprintf("Starting %q (%s %s)", plan.Name, plan.Type, plan.Request))
	for _, w := range plan.Warnings {
		m.console.println("console", "Note: "+w)
	}

	if m.sess == nil {
		m.status = statusStarting
	}
	if m.focus == paneExplorer {
		m.setFocus(paneVariables)
	} else {
		m.view = viewDebug
	}

	if plan.PreLaunchTask != "" {
		cmd := m.runTask(plan.PreLaunchTask, vars)
		if cmd == nil {
			if m.sess == nil {
				m.status = statusInactive
			}
			m.compound, m.queue, m.noDebug = nil, nil, false
			return nil
		}
		m.pending = plan
		return cmd
	}
	return m.launchPlan(plan)
}

func (m *Model) vars() launch.Vars {
	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	return launch.Vars{Workspace: m.file.Workspace, File: m.editor.path, Cwd: cwd, Home: home, Env: os.Getenv, Adapters: m.adapters, Matchers: m.matchers}
}

// setup collects the breakpoints and exception filters to configure a
// session with, and notes the versions sent so later changes are re-sent.
func (m *Model) setup() (session.Setup, map[string][]state.Breakpoint, []godap.FunctionBreakpoint) {
	sent := map[string][]state.Breakpoint{}
	setup := session.Setup{Breakpoints: map[string][]godap.SourceBreakpoint{}}
	for _, path := range m.store.Files() {
		bps := enabled(m.store.BreakpointsIn(path))
		sent[path] = bps
		setup.Breakpoints[path] = sourceBreakpoints(bps)
	}
	m.startVersions = maps.Clone(m.bpVersions)
	setup.Functions = enabledFunctions(m.store.Functions)
	m.startFnVersion = m.fnVersion

	choices := maps.Clone(m.store.ExceptionFilters)
	setup.ExceptionFilters = func(filters []godap.ExceptionBreakpointsFilter) []string {
		return enabledFilters(filters, choices)
	}
	return setup, sent, setup.Functions
}

// startChild starts a session the adapter asked for with startDebugging, such
// as one for a subprocess.
func (m *Model) startChild(body json.RawMessage) tea.Cmd {
	var args struct {
		Configuration launch.Config `json:"configuration"`
		Request       string        `json:"request"`
	}
	if err := json.Unmarshal(body, &args); err != nil || args.Configuration == nil {
		m.console.println("stderr", "Ignoring an invalid startDebugging request")
		return nil
	}

	parent := m.run
	cfg := args.Configuration
	cfg["request"] = args.Request
	if cfg.String("type") == "" {
		cfg["type"] = parent.sess.Plan.Type
	}
	if cfg.Name() == "" {
		cfg["name"] = parent.name + " (child)"
	}

	plan, err := launch.Prepare(cfg, m.vars())
	if err != nil {
		m.console.println("stderr", "Could not start child session: "+err.Error())
		return nil
	}
	// A child of an adapter running as a server connects to that server,
	// unless the configuration says where to connect.
	if pt := parent.sess.Plan.Transport; (pt == launch.Dial || pt == launch.Listen) && plan.Transport != launch.Dial {
		plan.Transport, plan.Address, plan.Command = launch.Dial, parent.sess.Address(), nil
	}

	m.console.println("info", fmt.Sprintf("Starting child session %q", plan.Name))
	cmd := m.launchPlan(plan)
	m.run.parent = parent
	m.run.compound = parent.compound
	return cmd
}

// launchPlan starts a session for a prepared configuration.
func (m *Model) launchPlan(plan *launch.Plan) tea.Cmd {
	if m.noDebug && plan.Launched() {
		plan.Arguments["noDebug"] = true
		plan.Name += " (without debugging)"
	}
	s := session.New(plan, m.sessOpts)
	r := newRun()
	r.name = plan.Name
	r.compound = m.compound
	m.runs = append(m.runs, r)
	m.activate(r)
	m.sess = s
	m.status = statusStarting
	m.configured = false
	m.records = map[string]map[bpKey]godap.Breakpoint{}
	m.gen++
	m.clearInspection()

	setup, sent, sentFunctions := m.setup()
	start := func() tea.Msg {
		started, err := s.Start(setup)
		return startedMsg{sess: s, started: started, sent: sent, sentFunctions: sentFunctions, err: err}
	}
	return tea.Batch(waitEvent(s), start, m.startNext())
}

func enabled(bps []state.Breakpoint) []state.Breakpoint {
	var out []state.Breakpoint
	for _, bp := range bps {
		if bp.Enabled {
			out = append(out, bp)
		}
	}
	return out
}

func sourceBreakpoints(bps []state.Breakpoint) []godap.SourceBreakpoint {
	out := make([]godap.SourceBreakpoint, len(bps))
	for i, bp := range bps {
		out[i] = godap.SourceBreakpoint{Line: bp.Line, Column: bp.Column, Condition: bp.Condition, HitCondition: bp.HitCondition, LogMessage: bp.LogMessage}
	}
	return out
}

func enabledFilters(filters []godap.ExceptionBreakpointsFilter, choices map[string]bool) []string {
	out := []string{}
	for _, f := range filters {
		on, ok := choices[f.Filter]
		if !ok {
			on = f.Default
		}
		if on {
			out = append(out, f.Filter)
		}
	}
	return out
}

// bpKey identifies a breakpoint within a file.
type bpKey struct{ line, column int }

func keyOf(bp state.Breakpoint) bpKey {
	return bpKey{bp.Line, bp.Column}
}

// recordsByLine pairs adapter records with the breakpoints sent, which share an order.
func recordsByLine(sent []state.Breakpoint, records []godap.Breakpoint) map[bpKey]godap.Breakpoint {
	out := map[bpKey]godap.Breakpoint{}
	for i, bp := range sent {
		if i < len(records) {
			out[keyOf(bp)] = records[i]
		}
	}
	return out
}

func (m *Model) started(msg startedMsg) tea.Cmd {
	if msg.sess != m.sess {
		return nil
	}
	if msg.err != nil {
		// Stopping during startup makes Start fail; that is not worth reporting.
		if m.status != statusStopping {
			m.console.println("stderr", "Could not start debugging: "+msg.err.Error())
		}
		return nil
	}

	m.configured = true
	m.caps = msg.sess.Capabilities()
	m.filters = m.caps.ExceptionBreakpointFilters
	for path, records := range msg.started.Breakpoints {
		m.records[path] = recordsByLine(msg.sent[path], records)
	}
	m.fnRecords = functionRecords(msg.sentFunctions, msg.started.Functions)
	if msg.sess.HasTerminal() {
		m.console.println("console", "The program runs in the Terminal tab (ctrl+t in the panel switches to it)")
	}
	if m.status == statusStarting {
		m.status = statusRunning
	}

	var cmds []tea.Cmd
	for path, v := range m.bpVersions {
		if m.startVersions[path] != v {
			cmds = append(cmds, m.sendBreakpointsTo(m.run, path))
		}
	}
	if m.fnVersion != m.startFnVersion {
		cmds = append(cmds, m.sendFunctionBreakpointsTo(m.run))
	}
	m.hints = hints{}
	cmds = append(cmds, m.fetchHints())
	if m.status == statusRunning {
		cmds = append(cmds, m.fetchThreads(false))
	}
	return tea.Batch(cmds...)
}

func (m *Model) handleEvent(s *session.Session, e session.Event) tea.Cmd {
	if s != m.sess {
		return nil
	}

	switch e.Name {
	case "output":
		var body godap.OutputEventBody
		if json.Unmarshal(e.Body, &body) == nil && body.Category != "telemetry" {
			category := body.Category
			if category == "" {
				category = "console"
			}
			m.console.write(category, body.Output)
		}

	case "stopped":
		var body godap.StoppedEventBody
		_ = json.Unmarshal(e.Body, &body)
		return m.stopped(body)

	case "continued":
		var body godap.ContinuedEventBody
		_ = json.Unmarshal(e.Body, &body)
		if m.status == statusStopped && (body.AllThreadsContinued || body.ThreadId == m.threadID) {
			m.resumed()
		}

	case "terminated":
		if m.status != statusInactive {
			m.status = statusStopping
		}

	case "exited":
		var body godap.ExitedEventBody
		if json.Unmarshal(e.Body, &body) == nil {
			m.console.println("info", fmt.Sprintf("Process exited with code %d", body.ExitCode))
		}

	case "breakpoint":
		var body godap.BreakpointEventBody
		if json.Unmarshal(e.Body, &body) == nil && body.Reason == "changed" && body.Breakpoint.Id != 0 {
			for _, records := range m.records {
				for k, r := range records {
					if r.Id == body.Breakpoint.Id {
						records[k] = body.Breakpoint
					}
				}
			}
		}

	case "thread":
		if m.status == statusRunning || m.status == statusStopped {
			return m.fetchThreads(false)
		}

	case "capabilities":
		m.caps = s.Capabilities()

	case "startDebugging":
		return m.startChild(e.Body)

	case "initialized":
		// A second initialized event, after a restart request, asks for the
		// configuration again; the first is handled while starting.
		if !m.configured {
			return nil
		}
		setup, sent, sentFunctions := m.setup()
		return func() tea.Msg {
			started, err := s.Reconfigure(setup)
			return startedMsg{sess: s, started: started, sent: sent, sentFunctions: sentFunctions, err: err}
		}

	case "progressStart", "progressUpdate", "progressEnd":
		var body struct {
			ProgressID string `json:"progressId"`
			Title      string `json:"title"`
			Message    string `json:"message"`
		}
		if json.Unmarshal(e.Body, &body) != nil {
			return nil
		}
		if e.Name == "progressEnd" {
			delete(m.progress, body.ProgressID)
			return nil
		}
		p := m.progress[body.ProgressID]
		if body.Title != "" {
			p.title = body.Title
		}
		p.message = body.Message
		m.progress[body.ProgressID] = p

	case "memory":
		var body struct {
			MemoryReference string `json:"memoryReference"`
		}
		if json.Unmarshal(e.Body, &body) == nil && m.memory.on && body.MemoryReference == m.memory.ref {
			return m.readMemory(m.memory.offset)
		}

	case "terminal":
		var body session.TerminalData
		if json.Unmarshal(e.Body, &body) != nil {
			return nil
		}
		if m.run.term == nil {
			l := m.layout()
			m.run.term = newTerminalView(s, max(1, l.console.w-1), l.console.h+1)
			m.term = m.run.term
			m.panel = panelTerminal
		}
		m.run.term.write(body.Data)

	case "invalidated":
		if m.status == statusStopped && m.threadID != 0 {
			return m.fetchStack(m.threadID, false)
		}
	}
	return nil
}

func (m *Model) stopped(body godap.StoppedEventBody) tea.Cmd {
	// The session that stopped takes over the debug panes, as in VS Code.
	m.activate(m.run)
	// This can arrive before startup finishes, e.g. with stopOnEntry.
	m.caps = m.sess.Capabilities()
	m.gen++
	m.status = statusStopped
	m.stopReason = body.Reason
	if body.Description != "" {
		m.stopReason = body.Description
	}
	if body.ThreadId != 0 {
		m.threadID = body.ThreadId
	}
	m.clearInspection()
	if body.Text != "" {
		m.console.println("stderr", body.Text)
	}
	if m.focus == paneExplorer {
		m.setFocus(paneEditor)
	}

	cmds := []tea.Cmd{m.fetchThreads(true)}
	if body.Reason == "exception" && m.caps.SupportsExceptionInfoRequest && m.threadID != 0 {
		s, thread := m.sess, m.threadID
		cmds = append(cmds, func() tea.Msg {
			var info godap.ExceptionInfoResponseBody
			if err := s.Request("exceptionInfo", map[string]any{"threadId": thread}, &info); err != nil {
				return nil
			}
			return exceptionMsg{sess: s, body: info}
		})
	}
	return tea.Batch(cmds...)
}

func (m *Model) resumed() {
	m.gen++
	m.status = statusRunning
	m.stopReason = ""
	m.clearInspection()
}

func (m *Model) clearInspection() {
	m.frames = map[int][]godap.StackFrame{}
	m.totals = map[int]int{}
	m.expanded = map[int]bool{}
	m.frameID = 0
	m.frameIP = ""
	m.execLoc = location{}
	m.frameLoc = location{}
	m.scopes = nil
	for _, w := range m.watches {
		w.value, w.err, w.ref, w.children, w.expanded, w.loading = "", "", 0, nil, false, false
	}
}

func (m *Model) sessionEnded(s *session.Session) tea.Cmd {
	if s != m.sess {
		return nil
	}
	ended := m.run
	m.runs = slices.DeleteFunc(m.runs, func(r *run) bool { return r == ended })
	defer func() {
		// Another session takes over the debug panes, or an idle one.
		if m.active == ended {
			if len(m.runs) > 0 {
				m.active = m.runs[len(m.runs)-1]
			} else {
				m.active = newRun()
			}
		}
	}()

	m.sess = nil
	m.status = statusInactive
	m.configured = false
	m.records = map[string]map[bpKey]godap.Breakpoint{}
	m.fnRecords = nil
	m.dataBps = nil
	m.threads = nil
	m.threadID = 0
	m.gen++
	m.clearInspection()
	m.console.println("info", fmt.Sprintf("Debug session %q ended", ended.name))
	if m.run.term != nil {
		m.run.term.close()
	}

	if m.quitting {
		if len(m.runs) == 0 {
			return tea.Quit
		}
		return nil
	}
	if m.restarting {
		m.restarting = false
		m.active = newRun()
		m.run = m.active
		cmd := m.startSession()
		ended = nil
		return cmd
	}
	if label := s.Plan.PostDebugTask; label != "" && m.task == nil {
		return m.runTask(label, m.vars())
	}
	return nil
}

func (m *Model) fetchThreads(loadStack bool) tea.Cmd {
	s, gen := m.sess, m.gen
	return func() tea.Msg {
		var body godap.ThreadsResponseBody
		err := s.Request("threads", nil, &body)
		return threadsMsg{sess: s, gen: gen, threads: body.Threads, loadStack: loadStack, err: err}
	}
}

// fetchStack loads a thread's stack. Like VS Code, with an adapter that
// supports delayed loading it loads the top frame first, then the rest of
// the first chunk; deeper frames load on request.
func (m *Model) fetchStack(threadID int, selectTop bool) tea.Cmd {
	levels := stackChunk
	if m.caps.SupportsDelayedStackTraceLoading {
		levels = 1
	}
	return m.fetchFrames(threadID, 0, levels, selectTop)
}

func (m *Model) fetchFrames(threadID, start, levels int, selectTop bool) tea.Cmd {
	s, gen := m.sess, m.gen
	return func() tea.Msg {
		var body godap.StackTraceResponseBody
		err := s.Request("stackTrace", map[string]any{"threadId": threadID, "startFrame": start, "levels": levels}, &body)
		return stackMsg{sess: s, gen: gen, threadID: threadID, startFrame: start, frames: body.StackFrames,
			total: body.TotalFrames, selectTop: selectTop, err: err}
	}
}

// moreFrames reports whether a thread has frames that are not loaded yet.
func (r *run) moreFrames(threadID int) bool {
	n := len(r.frames[threadID])
	if total := r.totals[threadID]; total > 0 {
		return n < total
	}
	return n > 0 && n%stackChunk == 0
}

// bestFrame picks the first frame with source, as VS Code does when focusing a stopped thread.
func bestFrame(frames []godap.StackFrame) godap.StackFrame {
	for _, f := range frames {
		if f.Source != nil && (f.Source.Path != "" || f.Source.SourceReference > 0) && f.PresentationHint != "subtle" {
			return f
		}
	}
	return frames[0]
}

func frameLocation(f godap.StackFrame) location {
	if f.Source == nil {
		return location{}
	}
	if f.Source.SourceReference > 0 && !fileExists(f.Source.Path) {
		return location{ref: f.Source.SourceReference, line: f.Line}
	}
	return location{path: f.Source.Path, line: f.Line}
}

// selectFrame shows a frame's source and loads its scopes and watch values.
func (m *Model) selectFrame(threadID int, f godap.StackFrame) tea.Cmd {
	m.threadID = threadID
	m.frameID = f.Id
	m.scopes = nil

	if frames := m.frames[threadID]; len(frames) > 0 {
		m.execLoc = frameLocation(frames[0])
	}
	m.frameLoc = frameLocation(f)
	m.frameIP = f.InstructionPointerReference

	cmds := []tea.Cmd{m.fetchScopes(f.Id), m.evaluateWatches()}
	if m.disasm.on {
		cmds = append(cmds, m.fetchDisassembly())
	}

	src := f.Source
	switch {
	case src == nil:
		m.editor.clear(fmt.Sprintf("No source for %s", f.Name))
	case src.Path != "" && fileExists(src.Path):
		m.openFile(src.Path, f.Line)
	case src.SourceReference > 0:
		cmds = append(cmds, m.fetchSource(*src, f.Line))
	default:
		m.editor.clear(fmt.Sprintf("Source unavailable: %s", src.Path))
	}
	return tea.Batch(cmds...)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func (m *Model) fetchSource(src godap.Source, line int) tea.Cmd {
	s, gen, frame := m.sess, m.gen, m.frameID
	name := src.Name
	if name == "" {
		name = src.Path
	}
	return func() tea.Msg {
		var body godap.SourceResponseBody
		err := s.Request("source", map[string]any{"source": src, "sourceReference": src.SourceReference}, &body)
		return sourceMsg{sess: s, gen: gen, frameID: frame, src: src, name: name, line: line, content: body.Content, err: err}
	}
}

func (m *Model) fetchScopes(frameID int) tea.Cmd {
	s, gen := m.sess, m.gen
	return func() tea.Msg {
		var body godap.ScopesResponseBody
		err := s.Request("scopes", map[string]any{"frameId": frameID}, &body)
		return scopesMsg{sess: s, gen: gen, frameID: frameID, scopes: body.Scopes, err: err}
	}
}

func (m *Model) showScopes(scopes []godap.Scope) tea.Cmd {
	m.scopes = nil
	var cmds []tea.Cmd
	for i, sc := range scopes {
		n := &varNode{key: sc.Name, name: sc.Name, ref: sc.VariablesReference}
		m.scopes = append(m.scopes, n)

		open, remembered := m.expandedVars[n.key]
		if !remembered {
			open = i == 0 && !sc.Expensive
		}
		if open && n.ref > 0 {
			cmds = append(cmds, m.fetchVariables(n))
		}
	}
	return tea.Batch(cmds...)
}

func (m *Model) fetchVariables(n *varNode) tea.Cmd {
	n.loading = true
	s, gen, ref := m.sess, m.gen, n.ref
	return func() tea.Msg {
		var body godap.VariablesResponseBody
		err := s.Request("variables", map[string]any{"variablesReference": ref}, &body)
		return variablesMsg{sess: s, gen: gen, node: n, vars: body.Variables, err: err}
	}
}

func (m *Model) showVariables(n *varNode, vars []godap.Variable, err error) tea.Cmd {
	n.loading = false
	if err != nil {
		n.children = []*varNode{{key: n.childKey("error"), depth: n.depth + 1, err: shortError(err)}}
		n.expanded = true
		return nil
	}

	n.children = make([]*varNode, len(vars))
	var cmds []tea.Cmd
	for i, v := range vars {
		child := &varNode{
			key:   n.childKey(v.Name),
			name:  v.Name,
			value: v.Value,
			typ:   v.Type,
			ref:   v.VariablesReference,
			depth: n.depth + 1,

			parentRef: n.ref,
			evalName:  v.EvaluateName,
			memRef:    v.MemoryReference,
		}
		n.children[i] = child
		if child.ref > 0 && m.expandedVars[child.key] {
			cmds = append(cmds, m.fetchVariables(child))
		}
	}
	n.expanded = true
	return tea.Batch(cmds...)
}

func (m *Model) rebuildWatches() {
	old := map[string]*varNode{}
	for _, w := range m.watches {
		old[w.name] = w
	}
	m.watches = nil
	for _, expr := range m.store.Watch {
		n := old[expr]
		if n == nil {
			n = &varNode{key: "watch\x00" + expr, name: expr, watch: true}
		}
		m.watches = append(m.watches, n)
	}
}

func (m *Model) evaluateWatches() tea.Cmd {
	var cmds []tea.Cmd
	for _, w := range m.watches {
		cmds = append(cmds, m.evaluateWatch(w))
	}
	return tea.Batch(cmds...)
}

func (m *Model) evaluateWatch(n *varNode) tea.Cmd {
	if m.sess == nil || m.status != statusStopped {
		return nil
	}
	n.loading = true
	s, gen, frame, expr := m.sess, m.gen, m.frameID, n.name
	return func() tea.Msg {
		var body godap.EvaluateResponseBody
		args := map[string]any{"expression": expr, "context": "watch"}
		if frame != 0 {
			args["frameId"] = frame
		}
		err := s.Request("evaluate", args, &body)
		return watchMsg{sess: s, gen: gen, frameID: frame, node: n, body: body, err: err}
	}
}

func (m *Model) evaluateInConsole(expr string) tea.Cmd {
	m.console.println("input", "› "+expr)
	if m.sess == nil || m.status == statusStarting && m.frameID == 0 {
		m.console.println("stderr", "No active debug session")
		return nil
	}
	s := m.sess
	args := map[string]any{"expression": expr, "context": "repl"}
	if m.status == statusStopped && m.frameID != 0 {
		args["frameId"] = m.frameID
	}
	return func() tea.Msg {
		var body godap.EvaluateResponseBody
		err := s.Request("evaluate", args, &body)
		return evalMsg{sess: s, expr: expr, body: body, err: err}
	}
}

// execute resumes a stopped thread with continue, next, stepIn or stepOut.
func (m *Model) execute(command string) tea.Cmd {
	if m.sess == nil || m.status != statusStopped || m.threadID == 0 {
		return nil
	}
	s, thread, gen := m.sess, m.threadID, m.gen+1
	reason := m.stopReason
	m.resumed()
	args := map[string]any{"threadId": thread}
	// Steps go one instruction at a time while the disassembly is shown, as in VS Code.
	if m.disasm.on && command != "continue" && m.caps.SupportsSteppingGranularity {
		args["granularity"] = "instruction"
	}
	return func() tea.Msg {
		if err := s.Request(command, args, nil); err != nil {
			return executeErrMsg{sess: s, gen: gen, command: command, thread: thread, reason: reason, err: err}
		}
		return nil
	}
}

func (m *Model) pause() tea.Cmd {
	if m.sess == nil || m.status != statusRunning {
		return nil
	}
	s, thread := m.sess, m.threadID
	return func() tea.Msg {
		if thread == 0 {
			var body godap.ThreadsResponseBody
			if err := s.Request("threads", nil, &body); err == nil && len(body.Threads) > 0 {
				thread = body.Threads[0].Id
			}
		}
		if err := s.Request("pause", map[string]any{"threadId": thread}, nil); err != nil {
			return requestErrMsg{s, "pause", err}
		}
		return nil
	}
}

func (m *Model) stop() tea.Cmd {
	m.restarting = false
	if m.task != nil && m.pending != nil {
		m.task.cancel()
		return nil
	}
	if m.sess == nil || m.status == statusStopping {
		return nil
	}

	// Child sessions stop with their parent, and a compound with stopAll
	// stops together.
	stopping := m.run
	var cmds []tea.Cmd
	for _, r := range slices.Clone(m.runs) {
		related := r == stopping || r.parent == stopping ||
			stopping.compound != nil && stopping.compound.StopAll && r.compound == stopping.compound
		if related && r.status != statusStopping {
			cmds = append(cmds, m.withRun(r, func() tea.Cmd {
				m.status = statusStopping
				return m.closeSession()
			}))
		}
	}
	return tea.Batch(cmds...)
}

func (m *Model) closeSession() tea.Cmd {
	s := m.sess
	if s == nil {
		return nil
	}
	terminate := s.Plan.Launched()
	return func() tea.Msg {
		s.Close(terminate)
		return nil
	}
}

// restart restarts the active session: in place with a restart request when
// the adapter supports one, otherwise by stopping it and starting a new one,
// as VS Code does.
func (m *Model) restart() tea.Cmd {
	if m.sess == nil {
		return m.startSession()
	}
	s := m.sess

	if m.caps.SupportsRestartRequest {
		cfg := m.configFor(s.Plan.Name)
		if cfg != nil {
			if plan, err := launch.Prepare(cfg, m.vars()); err == nil {
				m.resumed()
				m.console.println("info", fmt.Sprintf("Restarting %q", plan.Name))
				return func() tea.Msg {
					if err := s.Restart(plan.Arguments); err != nil {
						return requestErrMsg{s, "restart", err}
					}
					return nil
				}
			}
		}
	}

	m.restarting = true
	m.status = statusStopping
	return func() tea.Msg {
		s.CloseForRestart()
		return nil
	}
}

// configFor finds the launch configuration a session was started from.
func (m *Model) configFor(name string) launch.Config {
	cfg, err := m.file.Find(name)
	if err != nil {
		return nil
	}
	return cfg
}

// toggleBreakpointAt adds or removes a breakpoint on a line of a local file.
func (m *Model) toggleBreakpointAt(path string, line int) tea.Cmd {
	if path == "" && m.editor.src.SourceReference > 0 && line >= 1 {
		return m.toggleRefBreakpoint(m.editor.src, line)
	}
	if path == "" || line < 1 {
		return nil
	}
	m.store.Toggle(path, m.storedLine(path, line))
	m.saveState()
	return m.sendBreakpoints(path)
}

// storedLine maps a line shown in the gutter to the line a breakpoint was set
// on: a breakpoint the adapter moved is drawn at the line it verified.
func (m *Model) storedLine(path string, line int) int {
	if m.store.Find(path, line) >= 0 {
		return line
	}
	for k, r := range m.records[path] {
		if k.column == 0 && r.Verified && r.Line == line && k.line != line {
			return k.line
		}
	}
	return line
}

// toggleRefBreakpoint toggles a breakpoint in an adapter-provided source,
// for the current session only.
func (m *Model) toggleRefBreakpoint(src godap.Source, line int) tea.Cmd {
	if m.sess == nil || !m.configured {
		m.console.println("stderr", "Breakpoints in sources from the debug adapter need a running session")
		return nil
	}
	if m.refBps == nil {
		m.refBps, m.refRecords = map[int][]int{}, map[int]map[int]godap.Breakpoint{}
	}
	ref := src.SourceReference
	if i := slices.Index(m.refBps[ref], line); i >= 0 {
		m.refBps[ref] = slices.Delete(m.refBps[ref], i, i+1)
	} else {
		m.refBps[ref] = append(m.refBps[ref], line)
		slices.Sort(m.refBps[ref])
	}

	m.refVersion++
	s, version, lines := m.sess, m.refVersion, slices.Clone(m.refBps[ref])
	bps := make([]godap.SourceBreakpoint, len(lines))
	for i, l := range lines {
		bps[i] = godap.SourceBreakpoint{Line: l}
	}
	return func() tea.Msg {
		records, stale, err := s.SetSourceBreakpoints(src, bps, version)
		if stale {
			return nil
		}
		return refBreakpointsMsg{sess: s, ref: ref, lines: lines, records: records, err: err}
	}
}

// toggleInlineBreakpoint adds or removes a breakpoint at a 1-based column.
func (m *Model) toggleInlineBreakpoint(path string, line, column int) tea.Cmd {
	if path == "" || line < 1 || column < 1 {
		return nil
	}
	m.store.ToggleAt(path, line, column)
	m.saveState()
	return m.sendBreakpoints(path)
}

func (m *Model) toggleEnabledAt(path string, line int) tea.Cmd {
	i := m.store.Find(path, m.storedLine(path, line))
	if i < 0 {
		return nil
	}
	m.store.Breakpoints[i].Enabled = !m.store.Breakpoints[i].Enabled
	m.saveState()
	return m.sendBreakpoints(path)
}

// breakpointField is a property of a line breakpoint edited through a prompt.
type breakpointField struct {
	title       string
	placeholder string
	get         func(*state.Breakpoint) *string
}

var (
	conditionField = breakpointField{"Condition", "Expression; empty for an unconditional breakpoint",
		func(bp *state.Breakpoint) *string { return &bp.Condition }}
	hitField = breakpointField{"Hit count", "Hit count condition such as 3 or >= 3; empty for none",
		func(bp *state.Breakpoint) *string { return &bp.HitCondition }}
	logField = breakpointField{"Log message", "Message with {expressions}; empty to stop instead of logging",
		func(bp *state.Breakpoint) *string { return &bp.LogMessage }}
)

// editBreakpoint prompts for a breakpoint property, adding the breakpoint if needed.
func (m *Model) editBreakpoint(path string, line int, field breakpointField) {
	if path == "" {
		return
	}
	line = m.storedLine(path, line)
	current := ""
	if i := m.store.Find(path, line); i >= 0 {
		current = *field.get(&m.store.Breakpoints[i])
	}
	title := fmt.Sprintf("%s for %s:%d", field.title, filepath.Base(path), line)
	m.openModal(newPrompt(title, current, field.placeholder, func(m *Model, value string) tea.Cmd {
		i := m.store.Find(path, line)
		if i < 0 {
			m.store.Toggle(path, line)
			i = m.store.Find(path, line)
		}
		*field.get(&m.store.Breakpoints[i]) = strings.TrimSpace(value)
		m.saveState()
		return m.sendBreakpoints(path)
	}))
}

func enabledFunctions(fns []state.FunctionBreakpoint) []godap.FunctionBreakpoint {
	var out []godap.FunctionBreakpoint
	for _, f := range fns {
		if f.Enabled {
			out = append(out, godap.FunctionBreakpoint{Name: f.Name, Condition: f.Condition})
		}
	}
	return out
}

// functionRecords pairs adapter records with the function breakpoints sent.
func functionRecords(sent []godap.FunctionBreakpoint, records []godap.Breakpoint) map[string]godap.Breakpoint {
	out := map[string]godap.Breakpoint{}
	for i, f := range sent {
		if i < len(records) {
			out[f.Name] = records[i]
		}
	}
	return out
}

func (m *Model) sendFunctionBreakpoints() tea.Cmd {
	m.fnVersion++
	var cmds []tea.Cmd
	for _, r := range m.runs {
		cmds = append(cmds, m.sendFunctionBreakpointsTo(r))
	}
	return tea.Batch(cmds...)
}

func (m *Model) sendFunctionBreakpointsTo(r *run) tea.Cmd {
	if r.sess == nil || !r.configured || !r.caps.SupportsFunctionBreakpoints {
		return nil
	}
	s, version := r.sess, m.fnVersion
	sent := enabledFunctions(m.store.Functions)
	return func() tea.Msg {
		records, stale, err := s.SetFunctionBreakpoints(sent, version)
		if stale {
			return nil
		}
		return functionsMsg{sess: s, version: version, sent: sent, records: records, err: err}
	}
}

// sendBreakpoints records a change to a file's breakpoints and sends them if a session is configured.
func (m *Model) sendBreakpoints(path string) tea.Cmd {
	m.bpVersions[path]++
	var cmds []tea.Cmd
	for _, r := range m.runs {
		cmds = append(cmds, m.sendBreakpointsTo(r, path))
	}
	return tea.Batch(cmds...)
}

// sendBreakpointsTo sends a file's current breakpoints to one session.
func (m *Model) sendBreakpointsTo(r *run, path string) tea.Cmd {
	version := m.bpVersions[path]
	if r.sess == nil || !r.configured {
		return nil
	}

	s := r.sess
	sent := enabled(m.store.BreakpointsIn(path))
	bps := sourceBreakpoints(sent)
	return func() tea.Msg {
		records, stale, err := s.SetBreakpoints(path, bps, version)
		return breakpointsMsg{sess: s, path: path, version: version, sent: sent, records: records, stale: stale, err: err}
	}
}

func (m *Model) sendExceptionFilters() tea.Cmd {
	m.filterVersion++
	var cmds []tea.Cmd
	for _, r := range m.runs {
		if r.sess == nil || !r.configured {
			continue
		}
		s, version := r.sess, m.filterVersion
		filters := enabledFilters(r.caps.ExceptionBreakpointFilters, m.store.ExceptionFilters)
		cmds = append(cmds, func() tea.Msg {
			if _, err := s.SetExceptionFilters(filters, version); err != nil {
				return requestErrMsg{s, "setExceptionBreakpoints", err}
			}
			return nil
		})
	}
	return tea.Batch(cmds...)
}

// listFiles lists workspace files for quick open, honouring .gitignore when possible.
func listFiles(root string) tea.Cmd {
	return func() tea.Msg {
		out, err := exec.Command("git", "-C", root, "ls-files", "--cached", "--others", "--exclude-standard").Output()
		if err == nil {
			var files []string
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				if line != "" && fileExists(filepath.Join(root, line)) {
					files = append(files, line)
				}
			}
			return filesMsg{files}
		}

		var files []string
		skip := []string{".git", "node_modules", ".venv", "venv", "__pycache__", "target", "dist", "build"}
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() && path != root && slices.Contains(skip, d.Name()) {
				return filepath.SkipDir
			}
			if !d.IsDir() {
				if rel, err := filepath.Rel(root, path); err == nil {
					files = append(files, rel)
				}
			}
			if len(files) > 50000 {
				return filepath.SkipAll
			}
			return nil
		})
		return filesMsg{files}
	}
}

func (m *Model) openQuickOpen(files []string) {
	m.openModal(newPicker("Go to file", files, 0, true, func(m *Model, index int) tea.Cmd {
		m.openFile(filepath.Join(m.file.Workspace, files[index]), 0)
		m.setFocus(paneEditor)
		return nil
	}))
}
