package ui

import (
	"strings"
	"sync/atomic"

	"charm.land/bubbles/v2/key"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/simonrw/dap-debugger/internal/session"
)

const maxTranscript = 256 * 1024

type panel int

const (
	panelConsole panel = iota
	panelTerminal
)

// terminalView emulates the terminal a debuggee runs in, like VS Code's
// integrated terminal: output is drawn by a terminal emulator and key presses
// are encoded and sent to the program.
type terminalView struct {
	emu  *vt.Emulator
	sess *session.Session
	// transcript is the output as plain text.
	transcript strings.Builder
	cols, rows int
	closed     bool
	// stopped tells forwardInput to stop; it is read from that goroutine.
	stopped atomic.Bool
}

func newTerminalView(s *session.Session, cols, rows int) *terminalView {
	t := &terminalView{emu: vt.NewEmulator(cols, rows), sess: s, cols: cols, rows: rows}
	s.ResizeTerminal(cols, rows)
	go t.forwardInput()
	return t
}

// forwardInput sends what the emulator encodes, key presses and replies to
// terminal queries, to the program.
func (t *terminalView) forwardInput() {
	buf := make([]byte, 4096)
	for {
		n, err := t.emu.Read(buf)
		if t.stopped.Load() || err != nil {
			return
		}
		if n > 0 {
			_ = t.sess.WriteInput(buf[:n])
		}
	}
}

func (t *terminalView) write(data []byte) {
	_, _ = t.emu.Write(data)
	if t.transcript.Len() < maxTranscript {
		t.transcript.WriteString(ansi.Strip(string(data)))
	}
}

func (t *terminalView) sendKey(msg tea.KeyPressMsg) {
	if !t.closed {
		t.emu.SendKeys(uv.KeyPressEvent(msg))
	}
}

func (t *terminalView) paste(text string) {
	if !t.closed {
		t.emu.Paste(text)
	}
}

// close stops input once the program has gone; the screen stays visible.
// The emulator is not closed, as that races with a pending Read; a final
// keystroke wakes the reader so it can see it should stop.
func (t *terminalView) close() {
	if !t.closed {
		t.closed = true
		t.stopped.Store(true)
		t.emu.SendText(" ")
	}
}

func (t *terminalView) resize(cols, rows int) {
	if cols == t.cols && rows == t.rows || cols < 1 || rows < 1 {
		return
	}
	t.cols, t.rows = cols, rows
	t.emu.Resize(cols, rows)
	if !t.closed {
		t.sess.ResizeTerminal(cols, rows)
	}
}

func (t *terminalView) view(width, height int, focused bool) string {
	t.resize(width-1, height)
	lines := strings.Split(t.emu.Render(), "\n")
	if focused && !t.closed && !t.emu.CursorHidden() {
		pos := t.emu.CursorPosition()
		if pos.Y >= 0 && pos.Y < len(lines) {
			lines[pos.Y] = markColumn(lines[pos.Y], pos.X, lipgloss.NewStyle().Reverse(true))
		}
	}
	for i := range lines {
		lines[i] = " " + lines[i]
	}
	return fill(lines, width, height)
}

// isDebugKey reports keys that keep working while the terminal has focus,
// as VS Code keeps its debug commands out of the integrated terminal.
func (m *Model) isDebugKey(msg tea.KeyPressMsg) bool {
	k := m.keys
	for _, b := range []key.Binding{k.Start, k.Continue, k.StepOver, k.StepInto, k.StepOut, k.Pause, k.Stop, k.Restart, k.Quit, k.Help} {
		if key.Matches(msg, b) {
			return true
		}
	}
	return false
}
