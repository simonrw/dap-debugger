package ui

import (
	tea "charm.land/bubbletea/v2"
	godap "github.com/google/go-dap"

	"github.com/simonrw/dap-debugger/internal/session"
)

// hints are the columns where the adapter can stop on the cursor's line, as
// VS Code shows for inline breakpoints.
type hints struct {
	path string
	line int
	cols []int
}

type locationsMsg struct {
	sess      *session.Session
	path      string
	line      int
	locations []godap.BreakpointLocation
	err       error
}

// fetchHints asks the adapter where breakpoints can go on the cursor's line.
func (m *Model) fetchHints() tea.Cmd {
	path, line := m.editor.path, m.editor.cursor+1
	if path == m.hints.path && line == m.hints.line {
		return nil
	}
	m.hints = hints{path: path, line: line}
	r := m.active
	if path == "" || r.sess == nil || !r.configured || !r.caps.SupportsBreakpointLocationsRequest {
		return nil
	}
	s := r.sess
	return func() tea.Msg {
		var body godap.BreakpointLocationsResponseBody
		err := s.Request("breakpointLocations", map[string]any{"source": godap.Source{Path: path}, "line": line}, &body)
		return locationsMsg{sess: s, path: path, line: line, locations: body.Breakpoints, err: err}
	}
}

func (m *Model) handleHintsMsg(msg tea.Msg) (tea.Cmd, bool) {
	lm, ok := msg.(locationsMsg)
	if !ok {
		return nil, false
	}
	if lm.err != nil || lm.path != m.hints.path || lm.line != m.hints.line {
		return nil, true
	}
	m.hints.cols = nil
	for _, loc := range lm.locations {
		if loc.Line == lm.line && loc.Column > 0 {
			m.hints.cols = append(m.hints.cols, loc.Column-1)
		}
	}
	return nil, true
}

// snapToHint picks the hinted column at or after col, or the last one before it.
func (m *Model) snapToHint(col int) int {
	if m.hints.path != m.editor.path || m.hints.line != m.editor.cursor+1 || len(m.hints.cols) == 0 {
		return col
	}
	for _, c := range m.hints.cols {
		if c >= col {
			return c
		}
	}
	return m.hints.cols[len(m.hints.cols)-1]
}
