package ui

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	godap "github.com/google/go-dap"

	"github.com/simonrw/dap-debugger/internal/session"
)

// disasm is the disassembly view, shown in place of the source view.
type disasm struct {
	on     bool
	list   list
	instrs []godap.DisassembledInstruction
	// ip is the address of the selected frame's current instruction.
	ip      string
	message string
}

type disasmMsg struct {
	sess    *session.Session
	gen     int
	frameID int
	ip      string
	instrs  []godap.DisassembledInstruction
	err     error
}

type instructionBreakpointsMsg struct {
	sess    *session.Session
	records []godap.Breakpoint
	err     error
}

// toggleDisassembly switches between the source and disassembly views.
func (m *Model) toggleDisassembly() tea.Cmd {
	m.disasm.on = !m.disasm.on
	m.memory.on = false
	if !m.disasm.on {
		return nil
	}
	m.setFocus(paneEditor)
	return m.fetchDisassembly()
}

// fetchDisassembly loads instructions around the selected frame's position.
func (m *Model) fetchDisassembly() tea.Cmd {
	d := &m.disasm
	if m.sess == nil || m.status != statusStopped || m.frameIP == "" {
		d.instrs, d.message = nil, "Disassembly is shown when the program is paused"
		return nil
	}
	if !m.caps.SupportsDisassembleRequest {
		d.instrs, d.message = nil, "This debug adapter does not support disassembly"
		return nil
	}
	d.message = "Loading…"
	s, gen, frame, ip := m.sess, m.gen, m.frameID, m.frameIP
	return func() tea.Msg {
		var body godap.DisassembleResponseBody
		err := s.Request("disassemble", map[string]any{
			"memoryReference":   ip,
			"instructionOffset": -40,
			"instructionCount":  120,
			"resolveSymbols":    true,
		}, &body)
		return disasmMsg{sess: s, gen: gen, frameID: frame, ip: ip, instrs: body.Instructions, err: err}
	}
}

func (m *Model) handleDisasmMsg(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case disasmMsg:
		if msg.sess != m.sess || msg.gen != m.gen || msg.frameID != m.frameID {
			return nil, true
		}
		d := &m.disasm
		if msg.err != nil {
			d.instrs, d.message = nil, "Disassembly unavailable: "+shortError(msg.err)
			return nil, true
		}
		d.instrs, d.ip, d.message = msg.instrs, msg.ip, ""
		if i := slices.IndexFunc(d.instrs, func(in godap.DisassembledInstruction) bool { return sameAddress(in.Address, d.ip) }); i >= 0 {
			d.list.cursor = i
			d.list.offset = max(0, i-m.layout().editor.h/2)
		}
		return nil, true

	case instructionBreakpointsMsg:
		if msg.err != nil {
			m.console.println("stderr", msg.err.Error())
		}
		for _, r := range msg.records {
			if !r.Verified && r.Message != "" {
				m.console.println("stderr", r.Message)
			}
		}
		return nil, true
	}
	return nil, false
}

// sameAddress compares addresses that adapters may format differently, such as 0x00ab and 0xab.
func sameAddress(a, b string) bool {
	norm := func(s string) string {
		s = strings.TrimPrefix(strings.ToLower(s), "0x")
		return strings.TrimLeft(s, "0")
	}
	return a != "" && norm(a) == norm(b)
}

func (m *Model) disasmKey(msg tea.KeyPressMsg) tea.Cmd {
	d := &m.disasm
	height := m.layout().editor.h
	if d.list.handleKey(msg.String(), len(d.instrs), height) {
		return nil
	}
	if msg.String() == "b" && d.list.cursor < len(d.instrs) {
		return m.toggleInstructionBreakpoint(d.instrs[d.list.cursor].Address)
	}
	if msg.String() == "esc" {
		d.on = false
	}
	return nil
}

// toggleInstructionBreakpoint sets or clears a breakpoint on an instruction
// for the current session, as VS Code's disassembly view does.
func (m *Model) toggleInstructionBreakpoint(address string) tea.Cmd {
	if m.sess == nil || !m.caps.SupportsInstructionBreakpoints {
		m.console.println("stderr", "This debug adapter does not support instruction breakpoints")
		return nil
	}
	if i := slices.IndexFunc(m.instBps, func(a string) bool { return sameAddress(a, address) }); i >= 0 {
		m.instBps = slices.Delete(m.instBps, i, i+1)
	} else {
		m.instBps = append(m.instBps, address)
	}

	s := m.sess
	bps := make([]map[string]any, len(m.instBps))
	for i, a := range m.instBps {
		bps[i] = map[string]any{"instructionReference": a}
	}
	return func() tea.Msg {
		var body godap.SetInstructionBreakpointsResponseBody
		err := s.Request("setInstructionBreakpoints", map[string]any{"breakpoints": bps}, &body)
		return instructionBreakpointsMsg{sess: s, records: body.Breakpoints, err: err}
	}
}

func (m *Model) disasmView(width, height int, focused bool) string {
	d := &m.disasm
	st := m.st
	if len(d.instrs) == 0 {
		return fill([]string{"", "  " + st.dim.Render(d.message)}, width, height)
	}

	rows := make([]string, len(d.instrs))
	lastSource := ""
	for i, in := range d.instrs {
		mark := " "
		if slices.ContainsFunc(m.instBps, func(a string) bool { return sameAddress(a, in.Address) }) {
			mark = st.breakpoint.Render("●")
		}
		arrow := " "
		if sameAddress(in.Address, d.ip) {
			arrow = st.execArrow.Render("▶")
		}

		where := ""
		if in.Location != nil && in.Line > 0 {
			src := in.Location.Name
			if src == "" {
				src = filepath.Base(in.Location.Path)
			}
			where = fmt.Sprintf("%s:%d", src, in.Line)
		}
		if where == lastSource {
			where = ""
		} else if where != "" {
			lastSource = where
		}

		row := " " + mark + arrow + " " + st.lineNumber.Render(fmt.Sprintf("%-18s", in.Address)) + " " +
			st.base.Render(sanitize(in.Instruction))
		if in.Symbol != "" && where == "" {
			row += "  " + st.dim.Render(sanitize(in.Symbol))
		}
		if where != "" {
			row += "  " + st.dim.Render(sanitize(where))
		}
		rows[i] = row
	}
	return d.list.view(st, rows, width, height, focused, "")
}
