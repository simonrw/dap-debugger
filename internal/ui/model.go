// Package ui is the terminal interface: a VS Code style debugger built with Bubble Tea.
package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	godap "github.com/google/go-dap"

	"github.com/simonrw/dap-debugger/internal/launch"
	"github.com/simonrw/dap-debugger/internal/session"
	"github.com/simonrw/dap-debugger/internal/state"
)

type pane int

const (
	paneExplorer pane = iota
	paneVariables
	paneWatch
	paneStack
	paneBreakpoints
	paneEditor
	paneConsole
)

var debugSections = []pane{paneVariables, paneWatch, paneStack, paneBreakpoints}

type sidebarView int

const (
	viewExplorer sidebarView = iota
	viewDebug
)

type status int

const (
	statusInactive status = iota
	statusStarting
	statusRunning
	statusStopped
	statusStopping
)

// location is a line in a local file, or in a source the adapter provides
// by reference when ref is set.
type location struct {
	path string
	ref  int
	line int
}

// Options configure a new Model.
type Options struct {
	File  *launch.File
	Store *state.Store
	// Configuration selects a launch configuration by name.
	Configuration string
	AutoStart     bool
	Session       session.Options
	// Adapters are user debug adapter definitions keyed by configuration type.
	Adapters map[string]launch.Adapter
	// Matchers are user background problem matchers for tasks.
	Matchers launch.ProblemMatchers
}

// Model is the root Bubble Tea model.
type Model struct {
	file  *launch.File
	store *state.Store
	keys  keyMap
	st    styles

	width, height int
	focus         pane
	lastDebug     pane
	view          sidebarView
	sidebarHidden bool

	explorer explorer
	editor   editor
	disasm   disasm
	memory   memoryView
	hints    hints
	console  console
	panel    panel
	// term is the terminal shown in the Terminal tab, which outlives its session.
	term     *terminalView
	lists    map[pane]*list
	modal    *modal
	showHelp bool

	configIndex int
	autoStart   bool
	sessOpts    session.Options
	adapters    map[string]launch.Adapter
	matchers    launch.ProblemMatchers

	// run is the session being handled: the active one, except while a
	// message from another session is processed (see withRun).
	*run
	// active is the session the debug keys and panes act on.
	active *run
	// runs are the live sessions, in start order.
	runs []*run

	filters  []godap.ExceptionBreakpointsFilter
	quitting bool
	// noDebug makes the sessions being started run without debugging.
	noDebug bool
	// task runs a preLaunchTask or postDebugTask; pending waits for it.
	task    *taskRun
	pending *launch.Plan
	// background are background tasks left running, by label.
	background map[string]*taskRun
	// queue holds compound members still to start, from compound.
	queue    []launch.Config
	compound *launch.Compound

	watches      []*varNode
	expandedVars map[string]bool

	fnVersion     int
	filterVersion int
	bpVersions    map[string]int
}

// run is the state of one debug session.
type run struct {
	sess       *session.Session
	name       string
	status     status
	caps       godap.Capabilities
	configured bool
	restarting bool
	// compound is the compound this session was started from, if any.
	compound *launch.Compound
	parent   *run

	// gen changes whenever execution resumes so stale responses can be dropped.
	gen        int
	threadID   int
	frameID    int
	stopReason string
	threads    []godap.Thread
	frames     map[int][]godap.StackFrame
	// totals are the adapter's total frame counts, when it reports them.
	totals map[int]int
	// progress is the adapter's long-running work, by progress ID.
	progress map[string]progress
	expanded map[int]bool
	execLoc  location
	frameLoc location
	// frameIP is the selected frame's instruction pointer, for disassembly.
	frameIP string
	scopes  []*varNode
	// instBps are instruction breakpoint addresses, which last for one session.
	instBps []string
	// refBps are breakpoint lines in adapter-provided sources, by source
	// reference; they last for one session, as references do.
	refBps     map[int][]int
	refRecords map[int]map[int]godap.Breakpoint
	refVersion int
	term       *terminalView

	records   map[string]map[bpKey]godap.Breakpoint
	fnRecords map[string]godap.Breakpoint
	// dataBps last for one session, as in VS Code.
	dataBps        []dataBreakpoint
	startFnVersion int
	startVersions  map[string]int
}

// progress is reported by the adapter with progress events, such as loading symbols.
type progress struct {
	title, message string
}

func newRun() *run {
	return &run{
		frames:   map[int][]godap.StackFrame{},
		totals:   map[int]int{},
		progress: map[string]progress{},
		expanded: map[int]bool{},
		records:  map[string]map[bpKey]godap.Breakpoint{},
	}
}

// runFor finds the live run for a session.
func (m *Model) runFor(s *session.Session) *run {
	for _, r := range m.runs {
		if r.sess == s {
			return r
		}
	}
	return nil
}

// withRun handles something for one session, then returns to the active one.
func (m *Model) withRun(r *run, f func() tea.Cmd) tea.Cmd {
	m.run = r
	cmd := f()
	m.run = m.active
	return cmd
}

// activate makes a session the one the debug keys and panes act on.
func (m *Model) activate(r *run) {
	m.active = r
	m.run = r
	if r.term != nil {
		m.term = r.term
	}
}

// New creates the model for a launch.json file.
func New(opts Options) *Model {
	m := &Model{
		file:         opts.File,
		store:        opts.Store,
		keys:         newKeyMap(),
		st:           newStyles(true),
		focus:        paneExplorer,
		lastDebug:    paneVariables,
		explorer:     newExplorer(opts.File.Workspace),
		console:      newConsole(),
		autoStart:    opts.AutoStart,
		sessOpts:     opts.Session,
		adapters:     opts.Adapters,
		matchers:     opts.Matchers,
		expandedVars: map[string]bool{},
		bpVersions:   map[string]int{},
		background:   map[string]*taskRun{},
		lists:        map[pane]*list{},
	}
	for _, p := range debugSections {
		m.lists[p] = &list{}
	}
	m.activate(newRun())
	m.editor.setPalette(true)

	name := opts.Configuration
	if name == "" {
		name = m.store.Configuration
	}
	for i, n := range m.selectionNames() {
		if n == name {
			m.configIndex = i
		}
	}

	m.editor.clear("Open a file from the Explorer or with ctrl+p to set breakpoints. F5 starts debugging.")
	if last := m.store.LastFile; last != "" {
		if err := m.editor.openFile(last); err == nil {
			m.explorer.reveal(last, 1000)
			m.focus = paneEditor
		}
	}
	m.rebuildWatches()
	return m
}

// selectionNames lists configurations, then compounds, as the picker shows them.
func (m *Model) selectionNames() []string {
	var names []string
	for _, c := range m.file.Configurations {
		names = append(names, c.Name())
	}
	for _, c := range m.file.Compounds {
		names = append(names, c.Name)
	}
	return names
}

// selectionName is the name of the selected configuration or compound.
func (m *Model) selectionName() string {
	return m.selectionNames()[m.configIndex]
}

// selectedCompound returns the selected compound, or nil for a configuration.
func (m *Model) selectedCompound() *launch.Compound {
	if i := m.configIndex - len(m.file.Configurations); i >= 0 {
		return m.file.Compounds[i]
	}
	return nil
}

// displayName names the active session, or the selection when there is none.
func (m *Model) displayName() string {
	if m.active.sess != nil {
		return m.active.name
	}
	return m.selectionName()
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{tea.RequestBackgroundColor}
	if m.autoStart {
		cmds = append(cmds, func() tea.Msg { return autoStartMsg{} })
	}
	return tea.Batch(cmds...)
}

type autoStartMsg struct{}

type quitTimeoutMsg struct{}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.BackgroundColorMsg:
		m.st = newStyles(msg.IsDark())
		m.editor.setPalette(msg.IsDark())
		return m, nil

	case tea.KeyPressMsg:
		return m, m.handleKey(msg)

	case tea.MouseClickMsg:
		return m, m.handleClick(msg.Mouse())

	case tea.MouseWheelMsg:
		m.handleWheel(msg.Mouse())
		return m, nil

	case tea.PasteMsg:
		if m.modal != nil {
			var cmd tea.Cmd
			m.modal.input, cmd = m.modal.input.Update(msg)
			if m.modal.isPicker() {
				m.modal.filter()
			}
			return m, cmd
		}
		if m.focus == paneConsole && m.panel == panelTerminal && m.term != nil {
			m.term.paste(msg.Content)
			return m, nil
		}
		if m.focus == paneConsole {
			var cmd tea.Cmd
			m.console.input, cmd = m.console.input.Update(msg)
			return m, cmd
		}
		return m, nil

	case autoStartMsg:
		return m, m.startSession()

	case quitTimeoutMsg:
		return m, tea.Quit
	}

	return m, m.handleDebugMsg(msg)
}

// layout positions every region for the current size.
type rect struct{ x, y, w, h int }

func (r rect) contains(x, y int) bool {
	return x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}

type layout struct {
	sidebar  rect
	tabs     rect
	config   rect
	sections map[pane]rect
	headers  map[pane]rect
	editor   rect
	edHeader rect
	console  rect
	conInput rect
	conHead  rect
	status   rect
}

func (m *Model) layout() layout {
	w, h := max(m.width, 20), max(m.height, 8)
	body := h - 1
	l := layout{
		sections: map[pane]rect{},
		headers:  map[pane]rect{},
		status:   rect{0, h - 1, w, 1},
	}

	sw := 0
	if !m.sidebarHidden && w >= 60 {
		sw = max(24, min(w*3/10, 48))
	}
	l.sidebar = rect{0, 0, sw, body}
	mainX := 0
	if sw > 0 {
		mainX = sw + 1
	}
	mw := w - mainX

	consoleH := max(5, body*3/10)
	editorH := body - consoleH
	l.edHeader = rect{mainX, 0, mw, 1}
	l.editor = rect{mainX, 1, mw, max(1, editorH-1)}
	l.conHead = rect{mainX, editorH, mw, 1}
	l.console = rect{mainX, editorH + 1, mw, max(1, consoleH-2)}
	l.conInput = rect{mainX, body - 1, mw, 1}

	if sw == 0 {
		return l
	}

	l.tabs = rect{0, 0, sw, 1}
	if m.view == viewExplorer {
		l.headers[paneExplorer] = rect{0, 1, sw, 1}
		l.sections[paneExplorer] = rect{0, 2, sw, max(1, body-2)}
		return l
	}

	l.config = rect{0, 1, sw, 1}
	avail := max(0, body-2-len(debugSections))
	weights := map[pane]int{paneVariables: 4, paneWatch: 2, paneStack: 3, paneBreakpoints: 2}
	weights[m.focus] += 3
	total := 0
	for _, p := range debugSections {
		total += weights[p]
	}

	// Rows are rounded down so they never exceed the space; the focused
	// section takes the remainder. On short terminals a section can shrink to
	// just its header.
	rows := map[pane]int{}
	used := 0
	for _, p := range debugSections {
		rows[p] = avail * weights[p] / total
		used += rows[p]
	}
	rows[m.sidebarPane()] += avail - used

	y := 2
	for _, p := range debugSections {
		l.headers[p] = rect{0, y, sw, 1}
		l.sections[p] = rect{0, y + 1, sw, rows[p]}
		y += rows[p] + 1
	}
	return l
}

func (m *Model) visiblePanes() []pane {
	var panes []pane
	if !m.sidebarHidden && m.layout().sidebar.w > 0 {
		if m.view == viewExplorer {
			panes = append(panes, paneExplorer)
		} else {
			panes = append(panes, debugSections...)
		}
	}
	return append(panes, paneEditor, paneConsole)
}

func (m *Model) setFocus(p pane) {
	if p == paneExplorer {
		m.view = viewExplorer
	} else if p != paneEditor && p != paneConsole {
		m.view = viewDebug
	}
	if p != paneEditor && p != paneConsole {
		m.sidebarHidden = false
	}
	if m.view == viewDebug && p != paneEditor && p != paneConsole {
		m.lastDebug = p
	}
	m.focus = p
	if p == paneConsole {
		m.console.input.Focus()
	} else {
		m.console.input.Blur()
	}
}

// sidebarPane is the pane to focus when moving into the sidebar.
func (m *Model) sidebarPane() pane {
	if m.view == viewExplorer {
		return paneExplorer
	}
	return m.lastDebug
}

func (m *Model) cyclePane(delta int) {
	panes := m.visiblePanes()
	i := 0
	for j, p := range panes {
		if p == m.focus {
			i = j
		}
	}
	m.setFocus(panes[(i+delta+len(panes))%len(panes)])
}

func (m *Model) navigate(dir string) {
	sidebar := m.focus != paneEditor && m.focus != paneConsole
	switch dir {
	case "left":
		if !sidebar && m.layout().sidebar.w > 0 {
			m.setFocus(m.sidebarPane())
		}
	case "right":
		if sidebar {
			m.setFocus(paneEditor)
		}
	case "down", "up":
		delta := 1
		if dir == "up" {
			delta = -1
		}
		if !sidebar {
			if dir == "down" {
				m.setFocus(paneConsole)
			} else {
				m.setFocus(paneEditor)
			}
			return
		}
		for i, p := range debugSections {
			if p == m.focus {
				j := max(0, min(i+delta, len(debugSections)-1))
				m.setFocus(debugSections[j])
				return
			}
		}
	}
}

func (m *Model) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	if m.showHelp {
		m.showHelp = false
		return nil
	}

	if md := m.modal; md != nil {
		closed, cmd := md.update(m, msg)
		// onSubmit may have replaced the modal with another one.
		if closed && m.modal == md {
			m.modal = nil
		}
		return cmd
	}

	// The terminal takes every key except the debug keys, like VS Code's
	// integrated terminal. ctrl+t goes back to the Debug Console.
	if m.focus == paneConsole && m.panel == panelTerminal {
		if msg.String() == "ctrl+t" {
			m.panel = panelConsole
			return nil
		}
		if m.term != nil && !m.term.closed && !m.isDebugKey(msg) {
			m.term.sendKey(msg)
			return nil
		}
	}

	k := m.keys
	switch {
	case key.Matches(msg, k.Quit):
		return m.quit()
	case key.Matches(msg, k.Help):
		m.showHelp = true
		return nil
	case key.Matches(msg, k.Start):
		if m.sess == nil && m.task == nil {
			return m.startSession()
		}
		return nil
	case key.Matches(msg, k.RunNoDebug):
		if m.sess == nil && m.task == nil {
			m.noDebug = true
			return m.startSession()
		}
		return nil
	case key.Matches(msg, k.Continue):
		return m.execute("continue")
	case key.Matches(msg, k.StepOver):
		return m.execute("next")
	case key.Matches(msg, k.StepInto):
		return m.execute("stepIn")
	case key.Matches(msg, k.StepOut):
		return m.execute("stepOut")
	case key.Matches(msg, k.Pause):
		return m.pause()
	case key.Matches(msg, k.Stop):
		return m.stop()
	case key.Matches(msg, k.Restart):
		return m.restart()
	case key.Matches(msg, k.QuickOpen):
		return listFiles(m.file.Workspace)
	case key.Matches(msg, k.PickConfig):
		m.openConfigPicker()
		return nil
	case key.Matches(msg, k.Explorer):
		m.setFocus(paneExplorer)
		return nil
	case key.Matches(msg, k.DebugView):
		if m.view == viewDebug && m.focus != paneEditor && m.focus != paneConsole {
			return nil
		}
		m.view = viewDebug
		m.setFocus(m.lastDebug)
		return nil
	case key.Matches(msg, k.Disassembly):
		return m.toggleDisassembly()
	case key.Matches(msg, k.ToggleSidebar):
		m.sidebarHidden = !m.sidebarHidden
		if m.sidebarHidden && m.focus != paneConsole {
			m.setFocus(paneEditor)
		}
		return nil
	case key.Matches(msg, k.NavLeft):
		m.navigate("left")
		return nil
	case key.Matches(msg, k.NavRight):
		m.navigate("right")
		return nil
	case key.Matches(msg, k.NavDown):
		m.navigate("down")
		return nil
	case key.Matches(msg, k.NavUp):
		m.navigate("up")
		return nil
	case key.Matches(msg, k.NextPane):
		m.cyclePane(1)
		return nil
	case key.Matches(msg, k.PrevPane):
		m.cyclePane(-1)
		return nil
	}

	switch m.focus {
	case paneExplorer:
		return m.explorerKey(msg)
	case paneEditor:
		if m.memory.on {
			return m.memoryKey(msg)
		}
		if m.disasm.on {
			return m.disasmKey(msg)
		}
		return m.editorKey(msg)
	case paneConsole:
		return m.consoleKey(msg)
	case paneVariables:
		return m.varsKey(msg, paneVariables)
	case paneWatch:
		return m.varsKey(msg, paneWatch)
	case paneStack:
		return m.stackKey(msg)
	case paneBreakpoints:
		return m.breakpointsKey(msg)
	}
	return nil
}

func (m *Model) openModal(md *modal) {
	m.modal = md
}

func (m *Model) quit() tea.Cmd {
	m.saveState()
	m.stopTasks()
	if len(m.runs) == 0 {
		return tea.Quit
	}
	if m.quitting {
		return tea.Quit
	}
	m.quitting = true
	m.console.println("info", "Stopping debug sessions before quitting…")
	cmds := []tea.Cmd{tea.Tick(6*time.Second, func(time.Time) tea.Msg { return quitTimeoutMsg{} })}
	for _, r := range m.runs {
		cmds = append(cmds, m.withRun(r, m.closeSession))
	}
	return tea.Batch(cmds...)
}

// Shutdown stops anything still running after the program exits, such as a
// session left behind by a forced quit. It blocks until it has finished.
func (m *Model) Shutdown() {
	m.stopTasks()
	for _, r := range m.runs {
		r.sess.Close(r.sess.Plan.Launched())
	}
}

func (m *Model) saveState() {
	if err := m.store.Save(); err != nil {
		m.console.println("stderr", "Could not save state: "+err.Error())
	}
}

func (m *Model) openConfigPicker() {
	var names []string
	for _, c := range m.file.Configurations {
		names = append(names, fmt.Sprintf("%s  (%s %s)", c.Name(), c.String("type"), c.String("request")))
	}
	for _, c := range m.file.Compounds {
		names = append(names, fmt.Sprintf("%s  (compound: %s)", c.Name, strings.Join(c.Members(), ", ")))
	}
	m.openModal(newPicker("Select launch configuration", names, m.configIndex, false, func(m *Model, index int) tea.Cmd {
		m.configIndex = index
		m.store.Configuration = m.selectionName()
		m.saveState()
		return nil
	}))
}

// openFile shows a local file, optionally centred on a line.
func (m *Model) openFile(path string, line int) {
	if err := m.editor.openFile(path); err != nil {
		m.editor.clear(err.Error())
		return
	}
	if line > 0 {
		m.editor.center(line-1, m.layout().editor.h)
	}
	m.store.LastFile = path
	m.saveState()
	m.explorer.reveal(path, m.layout().sections[paneExplorer].h)
}

func (m *Model) explorerKey(msg tea.KeyPressMsg) tea.Cmd {
	height := m.layout().sections[paneExplorer].h
	e := &m.explorer
	if e.list.handleKey(msg.String(), len(e.rows), height) {
		return nil
	}

	n := e.selected()
	switch msg.String() {
	case "enter", "space", "o":
		if n == nil {
			return nil
		}
		if n.dir {
			if n.expanded {
				e.collapse(n)
			} else {
				e.expand(n)
			}
			return nil
		}
		m.openFile(n.path, 0)
		if msg.String() == "enter" {
			m.setFocus(paneEditor)
		}
	case "right", "l":
		if n != nil && n.dir {
			e.expand(n)
		}
	case "left", "h":
		if n == nil {
			return nil
		}
		if n.dir && n.expanded {
			e.collapse(n)
		} else if p := e.parent(n); p >= 0 {
			e.list.cursor = p
			e.list.clamp(len(e.rows), height)
		}
	case "r":
		e.refresh()
	case "-", "backspace":
		if parent := filepath.Dir(e.root.path); parent != e.root.path {
			e.setRoot(parent)
		}
	case ".":
		if n != nil && n.dir {
			e.setRoot(n.path)
		}
	case "~":
		e.setRoot(m.file.Workspace)
	}
	return nil
}

func (m *Model) editorKey(msg tea.KeyPressMsg) tea.Cmd {
	cmd := m.editorKeyInner(msg)
	return tea.Batch(cmd, m.fetchHints())
}

func (m *Model) editorKeyInner(msg tea.KeyPressMsg) tea.Cmd {
	height := m.layout().editor.h
	e := &m.editor
	k := m.keys

	switch msg.String() {
	case "up", "k":
		e.move(-1, height)
	case "down", "j":
		e.move(1, height)
	case "pgup":
		e.move(-max(1, height-1), height)
	case "pgdown":
		e.move(max(1, height-1), height)
	case "ctrl+u":
		e.move(-max(1, height/2), height)
	case "ctrl+d":
		e.move(max(1, height/2), height)
	case "home", "g":
		e.setCursor(0, height)
	case "end", "G":
		e.setCursor(len(e.lines)-1, height)
	case "left", "h":
		e.moveColumn(-1, m.layout().editor.w)
	case "right", "l":
		e.moveColumn(1, m.layout().editor.w)
	case "0":
		e.moveColumn(-e.col, m.layout().editor.w)
	case "$":
		e.moveColumn(1<<30, m.layout().editor.w)
	case "esc":
		e.search = ""
	default:
		switch {
		case key.Matches(msg, k.ToggleBreakpoint):
			return m.toggleBreakpointAt(e.path, e.cursor+1)
		case key.Matches(msg, k.InlineBreakpoint):
			e.col = m.snapToHint(e.col)
			return m.toggleInlineBreakpoint(e.path, e.cursor+1, e.col+1)
		case key.Matches(msg, k.ToggleEnabled):
			return m.toggleEnabledAt(e.path, e.cursor+1)
		case key.Matches(msg, k.Condition):
			m.editBreakpoint(e.path, e.cursor+1, conditionField)
		case key.Matches(msg, k.HitCount):
			m.editBreakpoint(e.path, e.cursor+1, hitField)
		case key.Matches(msg, k.LogPoint):
			m.editBreakpoint(e.path, e.cursor+1, logField)
		case key.Matches(msg, k.GotoLine):
			m.openModal(newPrompt("Go to line", "", "Line number", func(m *Model, value string) tea.Cmd {
				if n, err := strconv.Atoi(value); err == nil {
					m.editor.center(n-1, m.layout().editor.h)
				}
				return nil
			}))
		case key.Matches(msg, k.Search):
			m.openModal(newPrompt("Search in file", e.search, "Text to find", func(m *Model, value string) tea.Cmd {
				m.editor.search = value
				m.editor.find(true, m.layout().editor.h)
				return nil
			}))
		case key.Matches(msg, k.SearchNext):
			e.find(true, height)
		case key.Matches(msg, k.SearchPrev):
			e.find(false, height)
		}
	}
	return nil
}

func (m *Model) consoleKey(msg tea.KeyPressMsg) tea.Cmd {
	height := m.layout().console.h
	switch msg.String() {
	case "enter":
		text := m.console.input.Value()
		if text == "" {
			return nil
		}
		m.console.input.SetValue("")
		m.console.remember(text)
		return m.evaluateInConsole(text)
	case "ctrl+t":
		if m.term != nil {
			m.panel = panelTerminal
		}
		return nil
	case "up":
		m.console.historyMove(-1)
		return nil
	case "down":
		m.console.historyMove(1)
		return nil
	case "pgup":
		m.console.scroll += max(1, height-1)
		return nil
	case "pgdown":
		m.console.scroll = max(0, m.console.scroll-max(1, height-1))
		return nil
	case "esc":
		m.setFocus(paneEditor)
		return nil
	}
	var cmd tea.Cmd
	m.console.input, cmd = m.console.input.Update(msg)
	return cmd
}

func (m *Model) handleWheel(mouse tea.Mouse) {
	l := m.layout()
	delta := 3
	if mouse.Button == tea.MouseWheelUp {
		delta = -3
	}

	switch {
	case l.editor.contains(mouse.X, mouse.Y):
		m.editor.scroll(delta, l.editor.h)
	case l.console.contains(mouse.X, mouse.Y):
		m.console.scroll = max(0, m.console.scroll-delta)
	default:
		for p, r := range l.sections {
			if r.contains(mouse.X, mouse.Y) {
				n := m.rowCount(p)
				m.listFor(p).scroll(delta, n, r.h)
			}
		}
	}
}

func (m *Model) handleClick(mouse tea.Mouse) tea.Cmd {
	if m.modal != nil || m.showHelp {
		return nil
	}
	if mouse.Button != tea.MouseLeft {
		return nil
	}
	l := m.layout()
	x, y := mouse.X, mouse.Y

	switch {
	case l.tabs.contains(x, y):
		// The tab line reads " EXPLORER   DEBUG ".
		if x < 11 {
			m.setFocus(paneExplorer)
		} else {
			m.setFocus(paneVariables)
		}
		return nil
	case l.config.contains(x, y):
		m.openConfigPicker()
		return nil
	case l.editor.contains(x, y):
		m.setFocus(paneEditor)
		line := m.editor.offset + y - l.editor.y
		if line >= len(m.editor.lines) {
			return nil
		}
		m.editor.cursor = line
		if x-l.editor.x < m.editor.gutterWidth() {
			return m.toggleBreakpointAt(m.editor.path, line+1)
		}
		return nil
	case l.conHead.contains(x, y):
		m.setFocus(paneConsole)
		// The tabs read "  DEBUG CONSOLE   TERMINAL".
		if x-l.conHead.x >= 17 && m.term != nil {
			m.panel = panelTerminal
		} else {
			m.panel = panelConsole
		}
		return nil
	case l.console.contains(x, y), l.conInput.contains(x, y):
		m.setFocus(paneConsole)
		return nil
	case l.edHeader.contains(x, y):
		m.setFocus(paneEditor)
		return nil
	}

	for p, r := range l.headers {
		if r.contains(x, y) {
			m.setFocus(p)
			return nil
		}
	}
	for p, r := range l.sections {
		if !r.contains(x, y) {
			continue
		}
		m.setFocus(p)
		i := m.listFor(p).click(y-r.y, m.rowCount(p), r.h)
		if i < 0 {
			return nil
		}
		return m.activateRow(p, x-r.x)
	}
	return nil
}

func (m *Model) listFor(p pane) *list {
	if p == paneExplorer {
		return &m.explorer.list
	}
	return m.lists[p]
}

func (m *Model) rowCount(p pane) int {
	switch p {
	case paneExplorer:
		return len(m.explorer.rows)
	case paneVariables:
		return len(flattenVars(m.scopes))
	case paneWatch:
		return len(flattenVars(m.watches))
	case paneStack:
		return len(m.stackItems())
	case paneBreakpoints:
		return len(m.breakpointItems())
	}
	return 0
}

// activateRow handles a click on the selected row of a sidebar pane.
func (m *Model) activateRow(p pane, x int) tea.Cmd {
	switch p {
	case paneExplorer:
		if n := m.explorer.selected(); n != nil {
			if n.dir {
				if n.expanded {
					m.explorer.collapse(n)
				} else {
					m.explorer.expand(n)
				}
			} else {
				m.openFile(n.path, 0)
			}
		}
	case paneVariables, paneWatch:
		return m.toggleVar(p)
	case paneStack:
		return m.activateStackRow()
	case paneBreakpoints:
		if x < 5 {
			return m.toggleBreakpointRow()
		}
		m.gotoBreakpointRow()
	}
	return nil
}

// relPath shortens a path relative to the workspace, or to the home folder.
func relPath(workspace, path string) string {
	inside := func(base string) (string, bool) {
		rel, err := filepath.Rel(base, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", false
		}
		return rel, true
	}
	if rel, ok := inside(workspace); ok {
		return rel
	}
	if home, err := os.UserHomeDir(); err == nil {
		if rel, ok := inside(home); ok {
			return "~/" + rel
		}
	}
	return path
}
