package ui

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	godap "github.com/google/go-dap"

	"github.com/simonrw/dap-debugger/internal/session"
)

const (
	memoryChunk = 512
	memoryRow   = 16
)

// memoryView is a hex view of debuggee memory, shown in place of the source
// view, like VS Code's binary data view.
type memoryView struct {
	on      bool
	name    string
	ref     string
	offset  int
	address uint64
	data    []byte
	list    list
	message string
}

type memoryMsg struct {
	sess   *session.Session
	ref    string
	offset int
	body   godap.ReadMemoryResponseBody
	err    error
}

type memoryWrittenMsg struct {
	sess *session.Session
	err  error
}

// openMemory shows the memory a variable refers to.
func (m *Model) openMemory(n *varNode) tea.Cmd {
	if n == nil || n.memRef == "" {
		m.console.println("stderr", "This value has no memory reference")
		return nil
	}
	if !m.caps.SupportsReadMemoryRequest {
		m.console.println("stderr", "This debug adapter does not support reading memory")
		return nil
	}
	m.memory = memoryView{on: true, name: n.name, ref: n.memRef, message: "Loading…"}
	m.disasm.on = false
	m.setFocus(paneEditor)
	return m.readMemory(0)
}

func (m *Model) readMemory(offset int) tea.Cmd {
	if m.sess == nil {
		return nil
	}
	s, ref := m.sess, m.memory.ref
	return func() tea.Msg {
		var body godap.ReadMemoryResponseBody
		err := s.Request("readMemory", map[string]any{"memoryReference": ref, "offset": offset, "count": memoryChunk}, &body)
		return memoryMsg{sess: s, ref: ref, offset: offset, body: body, err: err}
	}
}

func (m *Model) handleMemoryMsg(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case memoryMsg:
		mv := &m.memory
		if msg.sess != m.sess || msg.ref != mv.ref {
			return nil, true
		}
		if msg.err != nil {
			mv.data, mv.message = nil, "Memory unavailable: "+shortError(msg.err)
			return nil, true
		}
		data, err := base64.StdEncoding.DecodeString(msg.body.Data)
		if err != nil {
			mv.data, mv.message = nil, "The adapter returned invalid memory data"
			return nil, true
		}
		address, _ := strconv.ParseUint(strings.TrimPrefix(msg.body.Address, "0x"), 16, 64)
		mv.offset, mv.address, mv.data, mv.message = msg.offset, address, data, ""
		if len(data) == 0 {
			mv.message = fmt.Sprintf("%d bytes at %s cannot be read", msg.body.UnreadableBytes, msg.body.Address)
		}
		return nil, true

	case memoryWrittenMsg:
		if msg.err != nil {
			m.console.println("stderr", msg.err.Error())
			return nil, true
		}
		// Variables may show the changed memory.
		cmds := []tea.Cmd{m.readMemory(m.memory.offset)}
		if m.status == statusStopped && m.frameID != 0 {
			cmds = append(cmds, m.fetchScopes(m.frameID), m.evaluateWatches())
		}
		return tea.Batch(cmds...), true
	}
	return nil, false
}

func (m *Model) memoryRows() int {
	return (len(m.memory.data) + memoryRow - 1) / memoryRow
}

func (m *Model) memoryKey(msg tea.KeyPressMsg) tea.Cmd {
	mv := &m.memory
	height := m.layout().editor.h
	rows := m.memoryRows()

	switch msg.String() {
	case "esc":
		mv.on = false
		return nil
	case "pgdown", "ctrl+d", "down", "j":
		if mv.list.cursor >= rows-1 && rows > 0 {
			mv.list.cursor = 0
			return m.readMemory(mv.offset + memoryChunk)
		}
	case "pgup", "ctrl+u", "up", "k":
		if mv.list.cursor == 0 && mv.offset > 0 {
			mv.list.cursor = memoryChunk/memoryRow - 1
			return m.readMemory(max(0, mv.offset-memoryChunk))
		}
	case "e":
		return m.editMemory()
	}
	mv.list.handleKey(msg.String(), rows, height)
	return nil
}

// editMemory prompts for bytes to write at the selected row.
func (m *Model) editMemory() tea.Cmd {
	mv := &m.memory
	if !m.caps.SupportsWriteMemoryRequest {
		m.console.println("stderr", "This debug adapter does not support writing memory")
		return nil
	}
	if mv.list.cursor >= m.memoryRows() {
		return nil
	}
	start := mv.list.cursor * memoryRow
	row := mv.data[start:min(start+memoryRow, len(mv.data))]
	current := strings.TrimSpace(fmt.Sprintf("% x", row))
	title := fmt.Sprintf("Write bytes at %#x", mv.address+uint64(start))
	s, ref, offset := m.sess, mv.ref, mv.offset+start
	m.openModal(newPrompt(title, current, "Hex bytes, such as de ad be ef", func(m *Model, value string) tea.Cmd {
		data, err := hex.DecodeString(strings.Join(strings.Fields(value), ""))
		if err != nil || len(data) == 0 {
			m.console.println("stderr", "Not hex bytes: "+value)
			return nil
		}
		return func() tea.Msg {
			err := s.Request("writeMemory", map[string]any{
				"memoryReference": ref, "offset": offset, "data": base64.StdEncoding.EncodeToString(data),
			}, nil)
			return memoryWrittenMsg{sess: s, err: err}
		}
	}))
	return nil
}

func (m *Model) memoryView(width, height int, focused bool) string {
	mv := &m.memory
	st := m.st
	if len(mv.data) == 0 {
		return fill([]string{"", "  " + st.dim.Render(mv.message)}, width, height)
	}

	rows := make([]string, m.memoryRows())
	for i := range rows {
		chunk := mv.data[i*memoryRow : min((i+1)*memoryRow, len(mv.data))]
		var ascii strings.Builder
		for _, b := range chunk {
			if b >= 0x20 && b < 0x7f {
				ascii.WriteByte(b)
			} else {
				ascii.WriteByte('.')
			}
		}
		hexBytes := fmt.Sprintf("% x", chunk)
		rows[i] = " " + st.lineNumber.Render(fmt.Sprintf("%#016x", mv.address+uint64(i*memoryRow))) + "  " +
			st.base.Render(fmt.Sprintf("%-47s", hexBytes)) + "  " + st.varString.Render(ascii.String())
	}
	return mv.list.view(st, rows, width, height, focused, "")
}
