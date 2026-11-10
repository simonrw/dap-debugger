package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// View implements tea.Model.
func (m *Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = "dap-debugger: " + m.displayName()
	return v
}

func (m *Model) render() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}
	if m.width < 20 || m.height < 8 {
		return fill([]string{"Terminal too small"}, m.width, m.height)
	}
	l := m.layout()
	w := max(m.width, 20)

	main := m.renderMain(l)
	body := main
	if l.sidebar.w > 0 {
		sep := strings.TrimSuffix(strings.Repeat(m.st.separator.Render("│")+"\n", l.sidebar.h), "\n")
		body = lipgloss.JoinHorizontal(lipgloss.Top, m.renderSidebar(l), sep, main)
	}
	screen := body + "\n" + m.renderStatus(w)

	switch {
	case m.showHelp:
		screen = overlay(screen, m.renderHelp(), w, max(m.height, 8))
	case m.modal != nil:
		width := min(80, w-4)
		box := m.modal.view(m.st, width)
		screen = overlayAt(screen, box, (w-lipgloss.Width(box))/2, 2, w, max(m.height, 8))
	}
	return screen
}

func (m *Model) renderSidebar(l layout) string {
	st := m.st
	width := l.sidebar.w
	var lines []string

	explorer := st.tab.Render("EXPLORER")
	debug := st.tab.Render("DEBUG")
	if m.view == viewExplorer {
		explorer = st.tabActive.Render("EXPLORER")
	} else {
		debug = st.tabActive.Render("DEBUG")
	}
	lines = append(lines, fit(explorer+" "+debug, width))

	if m.view == viewExplorer {
		lines = append(lines, m.sectionHeader(paneExplorer, strings.ToUpper(m.explorer.root.name), width))
		r := l.sections[paneExplorer]
		bpFiles := map[string]bool{}
		for _, f := range m.store.Files() {
			bpFiles[f] = true
		}
		rows := m.explorer.renderRows(st, bpFiles)
		lines = append(lines, m.explorer.list.view(st, rows, width, r.h, m.focus == paneExplorer, "Empty folder"))
		return strings.Join(lines, "\n")
	}

	config := " " + st.execArrow.Render("▶") + " " + st.bold.Render(m.selectionName()) + st.dim.Render(" ▾  ^O")
	lines = append(lines, fit(config, width))

	titles := map[pane]string{
		paneVariables:   "VARIABLES",
		paneWatch:       "WATCH",
		paneStack:       "CALL STACK",
		paneBreakpoints: "BREAKPOINTS",
	}
	empty := map[pane]string{
		paneVariables:   "Not paused",
		paneWatch:       "No expressions (a to add)",
		paneStack:       "Not running",
		paneBreakpoints: "No breakpoints (b in the source view)",
	}
	for _, p := range debugSections {
		r := l.sections[p]
		lines = append(lines, m.sectionHeader(p, titles[p], width))

		var rows []string
		switch p {
		case paneVariables:
			rows = renderVarRows(st, flattenVars(m.scopes))
		case paneWatch:
			rows = renderVarRows(st, flattenVars(m.watches))
		case paneStack:
			rows = m.stackRows()
		case paneBreakpoints:
			rows = m.breakpointRows()
		}
		if r.h > 0 {
			lines = append(lines, m.lists[p].view(st, rows, width, r.h, m.focus == p, empty[p]))
		}
	}
	return strings.Join(lines, "\n")
}

func (m *Model) sectionHeader(p pane, title string, width int) string {
	style := m.st.header
	marker := "  "
	if m.focus == p {
		style = m.st.headerFocused
		marker = "▍ "
	}
	return fit(style.Render(marker+title), width)
}

func (m *Model) renderMain(l layout) string {
	st := m.st
	var lines []string

	title := st.dim.Render(" No file")
	if m.memory.on {
		title = " " + st.bold.Render("Memory of "+sanitize(m.memory.name)) + st.dim.Render("  e write bytes · esc close")
	} else if m.disasm.on {
		title = " " + st.bold.Render("Disassembly") + st.dim.Render("  b instruction breakpoint · steps go by instruction · ^Y source")
	} else if m.editor.name != "" {
		name := relPath(m.file.Workspace, m.editor.name)
		title = " " + st.bold.Render(name)
		if m.editor.path == "" {
			title += st.dim.Render("  (read only, from debug adapter)")
		}
		if len(m.editor.lines) > 0 {
			title += st.dim.Render(fmt.Sprintf("  Ln %d/%d", m.editor.cursor+1, len(m.editor.lines)))
		}
		if m.editor.search != "" {
			title += st.dim.Render(fmt.Sprintf("  /%s", m.editor.search))
		}
	}
	headerStyle := st.header
	if m.focus == paneEditor {
		headerStyle = st.headerFocused
	}
	lines = append(lines, fit(headerStyle.Render(map[bool]string{true: "▍", false: " "}[m.focus == paneEditor])+title, l.edHeader.w))
	if m.memory.on {
		lines = append(lines, m.memoryView(l.editor.w, l.editor.h, m.focus == paneEditor))
	} else if m.disasm.on {
		lines = append(lines, m.disasmView(l.editor.w, l.editor.h, m.focus == paneEditor))
	} else {
		lines = append(lines, m.editor.view(st, l.editor.w, l.editor.h, m.focus == paneEditor, m.editorDecorations()))
	}
	lines = append(lines, m.panelHeader(l.conHead.w))
	if m.panel == panelTerminal && m.term != nil {
		lines = append(lines, m.term.view(l.console.w, l.console.h+1, m.focus == paneConsole))
		return strings.Join(lines, "\n")
	}
	lines = append(lines, m.console.view(st, l.console.w, l.console.h))

	m.console.input.SetWidth(max(1, l.conInput.w-4))
	lines = append(lines, fit(" "+m.console.input.View(), l.conInput.w))
	return strings.Join(lines, "\n")
}

// panelHeader shows the bottom panel's tabs: the Debug Console, and the
// Terminal once a program runs in one.
func (m *Model) panelHeader(width int) string {
	style := m.st.header
	marker := "  "
	if m.focus == paneConsole {
		style = m.st.headerFocused
		marker = "▍ "
	}
	tab := func(title string, p panel) string {
		if m.panel == p {
			return style.Underline(true).Render(title)
		}
		return m.st.dim.Render(title)
	}
	header := style.Render(marker) + tab("DEBUG CONSOLE", panelConsole)
	if m.term != nil {
		header += "   " + tab("TERMINAL", panelTerminal)
		if m.focus == paneConsole {
			header += m.st.dim.Render("   ^T switch")
		}
	}
	return fit(header, width)
}

func (m *Model) statusText() string {
	for _, p := range m.progress {
		text := "◌ " + sanitize(p.title)
		if p.message != "" {
			text += ": " + sanitize(p.message)
		}
		return text
	}
	name := m.displayName()
	switch m.status {
	case statusStarting:
		return "◌ Starting " + name
	case statusRunning:
		return "▶ Running " + name
	case statusStopped:
		reason := "Paused"
		if m.stopReason != "" {
			reason = "Paused on " + m.stopReason
		}
		return "⏸ " + reason
	case statusStopping:
		return "■ Stopping " + name
	}
	return "○ " + name
}

func (m *Model) statusBindings() []key.Binding {
	k := m.keys
	switch m.status {
	case statusStopped:
		return []key.Binding{k.Continue, k.StepOver, k.StepInto, k.StepOut, k.Stop, k.Restart, k.Help}
	case statusRunning:
		return []key.Binding{k.Pause, k.Stop, k.Restart, k.Help}
	case statusStarting, statusStopping:
		return []key.Binding{k.Stop, k.Help}
	}
	return []key.Binding{k.Start, k.PickConfig, k.QuickOpen, k.Help, k.Quit}
}

func (m *Model) renderStatus(width int) string {
	style := m.st.statusIdle
	if m.status != statusInactive {
		style = m.st.statusDebug
	}

	h := help.New()
	h.ShortSeparator = " · "
	h.Styles.ShortKey = style.Bold(true)
	h.Styles.ShortDesc = style
	h.Styles.ShortSeparator = style
	h.Styles.Ellipsis = style

	left := " " + m.statusText() + " "
	right := h.ShortHelpView(m.statusBindings()) + " "
	gap := width - ansi.StringWidth(left) - ansi.StringWidth(right)
	if gap < 1 {
		return style.Render(fit(left, width))
	}
	return style.Render(left+strings.Repeat(" ", gap)) + right
}

func (m *Model) renderHelp() string {
	k := m.keys
	groups := []struct {
		title    string
		bindings []key.Binding
	}{
		{"Debugging", []key.Binding{k.Start, k.RunNoDebug, k.Continue, k.StepOver, k.StepInto, k.StepOut, k.Pause, k.Stop, k.Restart, k.PickConfig}},
		{"Source view", []key.Binding{k.ToggleBreakpoint, k.InlineBreakpoint, k.ToggleEnabled, k.Condition, k.HitCount, k.LogPoint, k.GotoLine, k.Search, k.SearchNext}},
		{"Panes", []key.Binding{k.Explorer, k.DebugView, k.QuickOpen, k.Disassembly, k.ToggleSidebar, k.NavLeft, k.NavDown, k.NavUp, k.NavRight, k.NextPane}},
		{"Explorer", []key.Binding{
			key.NewBinding(key.WithHelp("- / ⌫", "parent folder")),
			key.NewBinding(key.WithHelp(".", "show selected folder")),
			key.NewBinding(key.WithHelp("~", "back to workspace")),
			key.NewBinding(key.WithHelp("r", "refresh")),
		}},
		{"Lists", []key.Binding{
			key.NewBinding(key.WithHelp("enter/space", "open, expand, select frame")),
			key.NewBinding(key.WithHelp("space/x", "enable/disable breakpoint")),
			k.AddFunction,
			k.AddWatch,
			key.NewBinding(key.WithHelp("e", "edit watch or variable value")),
			key.NewBinding(key.WithHelp("w", "add variable to watch")),
			key.NewBinding(key.WithHelp("v", "break on value change")),
			key.NewBinding(key.WithHelp("m", "view memory")),
			k.Remove,
		}},
		{"Debug Console", []key.Binding{
			key.NewBinding(key.WithHelp("enter", "evaluate")),
			key.NewBinding(key.WithHelp("^T", "switch to/from the Terminal")),
			key.NewBinding(key.WithHelp("↑/↓", "history")),
		}},
		{"General", []key.Binding{k.Help, k.Quit}},
	}

	st := m.st
	column := func(from, to int) string {
		var lines []string
		for _, g := range groups[from:to] {
			lines = append(lines, st.bold.Render(g.title))
			for _, b := range g.bindings {
				lines = append(lines, fmt.Sprintf("  %-12s %s", b.Help().Key, st.dim.Render(b.Help().Desc)))
			}
			lines = append(lines, "")
		}
		return strings.Join(lines, "\n")
	}

	body := lipgloss.JoinHorizontal(lipgloss.Top, column(0, 3), "    ", column(3, len(groups)))
	footer := st.dim.Render("Click the gutter to toggle breakpoints. Press any key to close.")
	return st.modal.Render(st.modalTitle.Render("Keyboard shortcuts") + "\n\n" + body + "\n" + footer)
}

func overlay(base, box string, width, height int) string {
	x := (width - lipgloss.Width(box)) / 2
	y := max(0, (height-lipgloss.Height(box))/2)
	return overlayAt(base, box, x, y, width, height)
}

func overlayAt(base, box string, x, y, width, height int) string {
	layers := lipgloss.NewCompositor(
		lipgloss.NewLayer(base),
		lipgloss.NewLayer(box).X(max(0, x)).Y(max(0, y)).Z(1),
	)
	return lipgloss.NewCanvas(width, height).Compose(layers).Render()
}
