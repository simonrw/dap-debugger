package ui

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	godap "github.com/google/go-dap"

	"github.com/simonrw/dap-debugger/internal/state"
)

func (m *Model) varRoots(p pane) []*varNode {
	if p == paneWatch {
		return m.watches
	}
	return m.scopes
}

func (m *Model) selectedVar(p pane) *varNode {
	nodes := flattenVars(m.varRoots(p))
	l := m.lists[p]
	if l.cursor < 0 || l.cursor >= len(nodes) {
		return nil
	}
	return nodes[l.cursor]
}

func (m *Model) varsKey(msg tea.KeyPressMsg, p pane) tea.Cmd {
	height := m.layout().sections[p].h
	nodes := flattenVars(m.varRoots(p))
	l := m.lists[p]
	if l.handleKey(msg.String(), len(nodes), height) {
		return nil
	}

	n := m.selectedVar(p)
	switch msg.String() {
	case "enter", "space":
		return m.toggleVar(p)
	case "right", "l":
		if n != nil && n.ref > 0 && !n.expanded {
			return m.toggleVar(p)
		}
	case "left", "h":
		if n == nil {
			return nil
		}
		if n.expanded {
			return m.toggleVar(p)
		}
		for i := l.cursor - 1; i >= 0; i-- {
			if nodes[i].depth < n.depth {
				l.cursor = i
				l.clamp(len(nodes), height)
				break
			}
		}
	}

	if p != paneWatch {
		switch msg.String() {
		case "e", "f2":
			m.editVariable(n)
		case "w":
			return m.addWatch(n)
		case "v":
			return m.addDataBreakpoint(n)
		case "m":
			return m.openMemory(n)
		}
		return nil
	}

	switch {
	case msg.String() == "m":
		return m.openMemory(n)
	case key.Matches(msg, m.keys.AddWatch):
		m.openModal(newPrompt("Add watch expression", "", "Expression", func(m *Model, value string) tea.Cmd {
			value = strings.TrimSpace(value)
			if value == "" {
				return nil
			}
			m.store.Watch = append(m.store.Watch, value)
			m.saveState()
			m.rebuildWatches()
			return m.evaluateWatch(m.watches[len(m.watches)-1])
		}))
	case key.Matches(msg, m.keys.Remove):
		if n != nil && n.watch {
			m.store.Watch = slices.DeleteFunc(m.store.Watch, func(s string) bool { return s == n.name })
			m.saveState()
			m.rebuildWatches()
		}
	case msg.String() == "e":
		if n != nil && n.watch {
			old := n.name
			m.openModal(newPrompt("Edit watch expression", old, "Expression", func(m *Model, value string) tea.Cmd {
				value = strings.TrimSpace(value)
				i := slices.Index(m.store.Watch, old)
				if i < 0 || value == "" {
					return nil
				}
				m.store.Watch[i] = value
				m.saveState()
				m.rebuildWatches()
				return m.evaluateWatch(m.watches[i])
			}))
		}
	}
	return nil
}

// addWatch adds a variable to the watch list, as VS Code's Add to Watch does.
func (m *Model) addWatch(n *varNode) tea.Cmd {
	if n == nil || n.depth == 0 {
		return nil
	}
	expr := n.evalName
	if expr == "" {
		expr = n.name
	}
	if slices.Contains(m.store.Watch, expr) {
		return nil
	}
	m.store.Watch = append(m.store.Watch, expr)
	m.saveState()
	m.rebuildWatches()
	return m.evaluateWatch(m.watches[len(m.watches)-1])
}

// editVariable prompts for a new value and sets it with setVariable.
func (m *Model) editVariable(n *varNode) {
	if n == nil || n.parentRef == 0 || m.sess == nil || m.status != statusStopped || !m.caps.SupportsSetVariable {
		return
	}
	s, gen := m.sess, m.gen
	m.openModal(newPrompt("Set value of "+n.name, n.value, "New value", func(m *Model, value string) tea.Cmd {
		return func() tea.Msg {
			var body godap.SetVariableResponseBody
			err := s.Request("setVariable", map[string]any{"variablesReference": n.parentRef, "name": n.name, "value": value}, &body)
			return setVariableMsg{sess: s, gen: gen, node: n, body: body, err: err}
		}
	}))
}

// toggleVar expands or collapses the selected variable, loading children on demand.
func (m *Model) toggleVar(p pane) tea.Cmd {
	n := m.selectedVar(p)
	if n == nil || n.ref == 0 {
		return nil
	}
	if n.expanded {
		n.expanded = false
		delete(m.expandedVars, n.key)
		return nil
	}
	m.expandedVars[n.key] = true
	if n.children != nil {
		n.expanded = true
		return nil
	}
	if m.sess == nil || m.status != statusStopped {
		return nil
	}
	return m.fetchVariables(n)
}

// stackItem is a row of the call stack: a session (shown when there are
// several), a thread or a frame.
type stackItem struct {
	run      *run
	session  bool
	threadID int
	frame    *godap.StackFrame
	// more is the "load more stack frames" row of a thread.
	more bool
}

func (m *Model) stackItems() []stackItem {
	runs := []*run{m.active}
	if len(m.runs) > 1 {
		runs = m.runs
	}

	var items []stackItem
	for _, r := range runs {
		if len(runs) > 1 {
			items = append(items, stackItem{run: r, session: true})
		}
		for _, t := range r.threads {
			items = append(items, stackItem{run: r, threadID: t.Id})
			if !r.expanded[t.Id] {
				continue
			}
			for i := range r.frames[t.Id] {
				items = append(items, stackItem{run: r, threadID: t.Id, frame: &r.frames[t.Id][i]})
			}
			if r.moreFrames(t.Id) {
				items = append(items, stackItem{run: r, threadID: t.Id, more: true})
			}
		}
	}
	return items
}

func statusLabel(r *run) string {
	switch r.status {
	case statusStarting:
		return "STARTING"
	case statusRunning:
		return "RUNNING"
	case statusStopped:
		return "PAUSED"
	case statusStopping:
		return "STOPPING"
	}
	return ""
}

func (m *Model) stackRows() []string {
	items := m.stackItems()
	rows := make([]string, len(items))
	st := m.st
	for i, it := range items {
		r := it.run
		pad := ""
		if len(m.runs) > 1 {
			pad = "  "
		}

		switch {
		case it.more:
			rows[i] = "    " + pad + st.info.Render("Load more stack frames")

		case it.session:
			marker := "  "
			if r == m.active {
				marker = st.execArrow.Render("▶ ")
			}
			rows[i] = " " + marker + st.bold.Render(sanitize(r.name)) + "  " + st.dim.Render(statusLabel(r))

		case it.frame == nil:
			t := r.threadByID(it.threadID)
			icon := "▸ "
			if r.expanded[it.threadID] {
				icon = "▾ "
			}
			label := " " + pad + st.dim.Render(icon) + st.bold.Render(sanitize(t.Name))
			switch {
			case r.status == statusStopped && it.threadID == r.threadID:
				reason := "PAUSED"
				if r.stopReason != "" {
					reason = "PAUSED ON " + strings.ToUpper(r.stopReason)
				}
				label += "  " + st.info.Render(reason)
			case r.status == statusStopped:
				label += "  " + st.dim.Render("PAUSED")
			case r.status == statusRunning:
				label += "  " + st.dim.Render("RUNNING")
			}
			rows[i] = label

		default:
			f := it.frame
			name := st.base.Render(sanitize(f.Name))
			if f.Id == r.frameID {
				name = st.bold.Render(sanitize(f.Name))
			}
			where := ""
			if f.Source != nil {
				file := f.Source.Name
				if file == "" {
					file = filepath.Base(f.Source.Path)
				}
				where = fmt.Sprintf("%s:%d", file, f.Line)
			}
			row := "    " + pad + name + "  " + st.dim.Render(sanitize(where))
			if f.PresentationHint == "subtle" || f.Source == nil {
				row = "    " + pad + st.dim.Render(sanitize(f.Name)+"  "+sanitize(where))
			}
			rows[i] = row
		}
	}
	return rows
}

func (r *run) threadByID(id int) godap.Thread {
	for _, t := range r.threads {
		if t.Id == id {
			return t
		}
	}
	return godap.Thread{Id: id, Name: fmt.Sprintf("Thread %d", id)}
}

func (m *Model) stackKey(msg tea.KeyPressMsg) tea.Cmd {
	height := m.layout().sections[paneStack].h
	items := m.stackItems()
	l := m.lists[paneStack]
	if l.handleKey(msg.String(), len(items), height) {
		return nil
	}
	switch msg.String() {
	case "enter", "space", "right", "l", "left", "h":
		return m.activateStackRow()
	}
	return nil
}

// activateStackRow focuses a session, expands a thread or selects a frame.
func (m *Model) activateStackRow() tea.Cmd {
	items := m.stackItems()
	l := m.lists[paneStack]
	if l.cursor < 0 || l.cursor >= len(items) {
		return nil
	}
	it := items[l.cursor]
	m.activate(it.run)
	if it.session || m.status != statusStopped {
		return m.refocus()
	}

	if it.more {
		return m.fetchFrames(it.threadID, len(m.frames[it.threadID]), stackChunk, false)
	}
	if it.frame != nil {
		return m.selectFrame(it.threadID, *it.frame)
	}
	if m.expanded[it.threadID] {
		m.expanded[it.threadID] = false
		return nil
	}
	m.expanded[it.threadID] = true
	if _, ok := m.frames[it.threadID]; !ok {
		return m.fetchStack(it.threadID, false)
	}
	return nil
}

// refocus shows the active session's selected frame again after switching sessions.
func (m *Model) refocus() tea.Cmd {
	m.filters = m.caps.ExceptionBreakpointFilters
	if m.status != statusStopped {
		return nil
	}
	for tid, frames := range m.frames {
		for _, f := range frames {
			if f.Id == m.frameID {
				return m.selectFrame(tid, f)
			}
		}
	}
	return nil
}

type bpItem struct {
	filter *godap.ExceptionBreakpointsFilter
	data   *dataBreakpoint
	fn     *state.FunctionBreakpoint
	bp     *state.Breakpoint
}

func (m *Model) breakpointItems() []bpItem {
	var items []bpItem
	for i := range m.filters {
		items = append(items, bpItem{filter: &m.filters[i]})
	}
	for i := range m.dataBps {
		items = append(items, bpItem{data: &m.dataBps[i]})
	}
	for i := range m.store.Functions {
		items = append(items, bpItem{fn: &m.store.Functions[i]})
	}
	for i := range m.store.Breakpoints {
		items = append(items, bpItem{bp: &m.store.Breakpoints[i]})
	}
	return items
}

func (m *Model) filterEnabled(f godap.ExceptionBreakpointsFilter) bool {
	if on, ok := m.store.ExceptionFilters[f.Filter]; ok {
		return on
	}
	return f.Default
}

func (m *Model) breakpointRows() []string {
	st := m.st
	items := m.breakpointItems()
	rows := make([]string, len(items))
	for i, it := range items {
		if it.filter != nil {
			box := "[ ]"
			if m.filterEnabled(*it.filter) {
				box = "[x]"
			}
			rows[i] = " " + st.dim.Render(box) + " " + st.base.Render(it.filter.Label)
			continue
		}

		if db := it.data; db != nil {
			box := "[ ]"
			if db.enabled {
				box = "[x]"
			}
			style := m.st.breakpoint
			if !db.enabled || !db.verified {
				style = m.st.bpDisabled
			}
			rows[i] = " " + st.dim.Render(box) + " " + style.Render("◈") + " " + st.base.Render(sanitize(db.description)) + st.dim.Render("  on change")
			continue
		}

		if fn := it.fn; fn != nil {
			box := "[ ]"
			if fn.Enabled {
				box = "[x]"
			}
			style := m.st.breakpoint
			if r, ok := m.fnRecords[fn.Name]; !fn.Enabled || m.configured && (!ok || !r.Verified) {
				style = m.st.bpDisabled
			}
			row := " " + st.dim.Render(box) + " " + style.Render("ƒ") + " " + st.base.Render(fn.Name)
			if fn.Condition != "" {
				row += st.dim.Render("  if " + fn.Condition)
			}
			rows[i] = row
			continue
		}

		bp := it.bp
		box := "[ ]"
		if bp.Enabled {
			box = "[x]"
		}
		mark := m.gutterMark(*bp)
		line := bp.Line
		if r, ok := m.records[bp.Path][keyOf(*bp)]; ok && r.Verified && r.Line > 0 {
			line = r.Line
		}
		position := fmt.Sprintf(":%d", line)
		if bp.Column > 0 {
			position += fmt.Sprintf(":%d", bp.Column)
		}
		row := " " + st.dim.Render(box) + " " + mark.bpStyle.Render(mark.bp) + " " +
			st.base.Render(filepath.Base(bp.Path)) + st.dim.Render(position)
		if dir := filepath.Dir(relPath(m.file.Workspace, bp.Path)); dir != "." {
			row += st.dim.Render("  " + dir)
		}
		if bp.Condition != "" {
			row += st.dim.Render("  if " + bp.Condition)
		}
		if bp.HitCondition != "" {
			row += st.dim.Render("  hits " + bp.HitCondition)
		}
		if bp.LogMessage != "" {
			row += st.dim.Render("  log " + bp.LogMessage)
		}
		rows[i] = row
	}
	return rows
}

func (m *Model) selectedBreakpoint() *bpItem {
	items := m.breakpointItems()
	l := m.lists[paneBreakpoints]
	if l.cursor < 0 || l.cursor >= len(items) {
		return nil
	}
	return &items[l.cursor]
}

func (m *Model) breakpointsKey(msg tea.KeyPressMsg) tea.Cmd {
	height := m.layout().sections[paneBreakpoints].h
	l := m.lists[paneBreakpoints]
	if l.handleKey(msg.String(), len(m.breakpointItems()), height) {
		return nil
	}

	if key.Matches(msg, m.keys.AddFunction) {
		m.openModal(newPrompt("Add function breakpoint", "", "Function name, such as main.handler", func(m *Model, value string) tea.Cmd {
			value = strings.TrimSpace(value)
			if value == "" || slices.ContainsFunc(m.store.Functions, func(f state.FunctionBreakpoint) bool { return f.Name == value }) {
				return nil
			}
			m.store.Functions = append(m.store.Functions, state.FunctionBreakpoint{Name: value, Enabled: true})
			m.saveState()
			return m.sendFunctionBreakpoints()
		}))
		return nil
	}

	it := m.selectedBreakpoint()
	if it == nil {
		return nil
	}
	if db := it.data; db != nil {
		switch {
		case msg.String() == "space", msg.String() == "x", key.Matches(msg, m.keys.ToggleEnabled):
			return m.toggleBreakpointRow()
		case key.Matches(msg, m.keys.Remove):
			id := db.id
			m.dataBps = slices.DeleteFunc(m.dataBps, func(d dataBreakpoint) bool { return d.id == id })
			return m.sendDataBreakpoints()
		}
		return nil
	}
	if fn := it.fn; fn != nil {
		switch {
		case msg.String() == "space", msg.String() == "x", key.Matches(msg, m.keys.ToggleEnabled):
			return m.toggleBreakpointRow()
		case key.Matches(msg, m.keys.Remove):
			m.store.Functions = slices.DeleteFunc(m.store.Functions, func(f state.FunctionBreakpoint) bool { return f.Name == fn.Name })
			m.saveState()
			return m.sendFunctionBreakpoints()
		case key.Matches(msg, m.keys.Condition):
			name := fn.Name
			m.openModal(newPrompt("Condition for "+name, fn.Condition, "Expression; empty for none", func(m *Model, value string) tea.Cmd {
				for i := range m.store.Functions {
					if m.store.Functions[i].Name == name {
						m.store.Functions[i].Condition = strings.TrimSpace(value)
					}
				}
				m.saveState()
				return m.sendFunctionBreakpoints()
			}))
		}
		return nil
	}
	switch {
	case msg.String() == "space", msg.String() == "x", key.Matches(msg, m.keys.ToggleEnabled):
		return m.toggleBreakpointRow()
	case msg.String() == "enter":
		m.gotoBreakpointRow()
	case key.Matches(msg, m.keys.Remove):
		if it.bp != nil {
			path := it.bp.Path
			m.store.Remove(path, it.bp.Line, it.bp.Column)
			m.saveState()
			return m.sendBreakpoints(path)
		}
	case key.Matches(msg, m.keys.Condition):
		if it.bp != nil {
			m.editBreakpoint(it.bp.Path, it.bp.Line, conditionField)
		}
	case key.Matches(msg, m.keys.HitCount):
		if it.bp != nil {
			m.editBreakpoint(it.bp.Path, it.bp.Line, hitField)
		}
	case key.Matches(msg, m.keys.LogPoint):
		if it.bp != nil {
			m.editBreakpoint(it.bp.Path, it.bp.Line, logField)
		}
	}
	return nil
}

func (m *Model) toggleBreakpointRow() tea.Cmd {
	it := m.selectedBreakpoint()
	if it == nil {
		return nil
	}
	if it.filter != nil {
		m.store.ExceptionFilters[it.filter.Filter] = !m.filterEnabled(*it.filter)
		m.saveState()
		return m.sendExceptionFilters()
	}
	if it.data != nil {
		it.data.enabled = !it.data.enabled
		return m.sendDataBreakpoints()
	}
	if it.fn != nil {
		it.fn.Enabled = !it.fn.Enabled
		m.saveState()
		return m.sendFunctionBreakpoints()
	}
	return m.toggleEnabledAt(it.bp.Path, it.bp.Line)
}

func (m *Model) gotoBreakpointRow() {
	it := m.selectedBreakpoint()
	if it == nil || it.bp == nil {
		return
	}
	m.openFile(it.bp.Path, it.bp.Line)
	m.setFocus(paneEditor)
}

// gutterMark is how a breakpoint is drawn: red when set or verified, hollow
// when the adapter could not verify it, grey when disabled. Conditional
// breakpoints are ringed and logpoints are diamonds, as in VS Code.
func (m *Model) gutterMark(bp state.Breakpoint) gutterMark {
	glyph, hollow := "●", "○"
	switch {
	case bp.LogMessage != "":
		glyph, hollow = "◆", "◇"
	case bp.Condition != "" || bp.HitCondition != "":
		glyph, hollow = "◉", "◎"
	}
	switch {
	case !bp.Enabled:
		return gutterMark{bp: glyph, bpStyle: m.st.bpDisabled}
	case m.configured:
		if r, ok := m.records[bp.Path][keyOf(bp)]; ok && r.Verified {
			return gutterMark{bp: glyph, bpStyle: m.st.breakpoint}
		}
		return gutterMark{bp: hollow, bpStyle: m.st.bpUnverified}
	}
	return gutterMark{bp: glyph, bpStyle: m.st.breakpoint}
}

func (m *Model) editorDecorations() editorDecorations {
	deco := editorDecorations{marks: map[int]gutterMark{}, inline: map[int][]inlineMark{}}
	path := m.editor.path
	if ref := m.editor.src.SourceReference; path == "" && ref > 0 {
		for _, line := range m.refBps[ref] {
			mark := gutterMark{bp: "○", bpStyle: m.st.bpUnverified}
			if r, ok := m.refRecords[ref][line]; ok && r.Verified {
				mark = gutterMark{bp: "●", bpStyle: m.st.breakpoint}
			}
			deco.marks[line] = mark
		}
		if m.execLoc.ref == ref {
			deco.execLine = m.execLoc.line
		}
		if m.frameLoc.ref == ref && m.frameLoc != m.execLoc {
			deco.frameLine = m.frameLoc.line
		}
		return deco
	}
	if path == "" {
		return deco
	}
	for _, bp := range m.store.BreakpointsIn(path) {
		line := bp.Line
		r, ok := m.records[path][keyOf(bp)]
		if ok && r.Verified && r.Line > 0 {
			line = r.Line
		}
		mark := m.gutterMark(bp)
		if bp.Column > 0 {
			column := bp.Column
			if ok && r.Verified && r.Column > 0 {
				column = r.Column
			}
			deco.inline[line] = append(deco.inline[line], inlineMark{column: column - 1, style: mark.bpStyle})
			continue
		}
		deco.marks[line] = mark
	}
	if m.hints.path == path && m.hints.line == m.editor.cursor+1 {
		deco.hints = m.hints.cols
		deco.hintStyle = m.st.bpUnverified.Underline(true)
	}
	if m.execLoc.path == path {
		deco.execLine = m.execLoc.line
	}
	if m.frameLoc.path == path && m.frameLoc != m.execLoc {
		deco.frameLine = m.frameLoc.line
	}
	return deco
}
