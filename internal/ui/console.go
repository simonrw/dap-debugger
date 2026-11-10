package ui

import (
	"slices"
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const maxConsoleLines = 5000

type consoleLine struct {
	text     string
	category string
}

// console is the debug console: program output plus an expression input.
type console struct {
	lines []consoleLine
	// open means the last line has not been terminated by a newline yet.
	open bool
	// scroll is how many wrapped rows the view is scrolled up from the bottom.
	scroll int

	input   textinput.Model
	history []string
	histPos int
}

func newConsole() console {
	in := textinput.New()
	in.SetVirtualCursor(true)
	in.Prompt = "› "
	in.Placeholder = "Evaluate expression in the selected frame (can modify the program)"
	return console{input: in}
}

// write appends output, which may contain partial lines.
func (c *console) write(category, text string) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	parts := strings.Split(text, "\n")
	for i, part := range parts {
		part = sanitize(part)
		last := len(parts) - 1
		if i == last && part == "" {
			c.open = false
			break
		}
		if i == 0 && c.open && len(c.lines) > 0 && c.lines[len(c.lines)-1].category == category {
			c.lines[len(c.lines)-1].text += part
		} else {
			c.lines = append(c.lines, consoleLine{text: part, category: category})
		}
		c.open = i == last
	}
	if over := len(c.lines) - maxConsoleLines; over > 0 {
		c.lines = c.lines[over:]
	}
	c.scroll = 0
}

// println appends a complete line.
func (c *console) println(category, text string) {
	if c.open {
		c.open = false
	}
	c.write(category, text+"\n")
}

func (c *console) style(st styles, category string) lipgloss.Style {
	switch category {
	case "stderr", "error":
		return st.stderr
	case "console", "telemetry":
		return st.console
	case "info", "important":
		return st.info
	case "input":
		return st.input
	case "result":
		return st.result
	}
	return st.base
}

// wrappedTail wraps lines from the end until it has at least n rows.
func (c *console) wrappedTail(st styles, width, n int) []string {
	var rows []string
	for i := len(c.lines) - 1; i >= 0 && len(rows) < n; i-- {
		l := c.lines[i]
		style := c.style(st, l.category)
		wrapped := strings.Split(ansi.Hardwrap(l.text, max(1, width-1), true), "\n")
		for j := len(wrapped) - 1; j >= 0; j-- {
			rows = append(rows, " "+style.Render(wrapped[j]))
		}
	}
	slices.Reverse(rows)
	return rows
}

func (c *console) view(st styles, width, height int) string {
	rows := c.wrappedTail(st, width, c.scroll+height)
	c.scroll = max(0, min(c.scroll, len(rows)-height))
	end := len(rows) - c.scroll
	start := max(0, end-height)
	visible := rows[start:end]

	lines := make([]string, 0, height)
	for i := len(visible); i < height; i++ {
		lines = append(lines, "")
	}
	lines = append(lines, visible...)
	return fill(lines, width, height)
}

func (c *console) historyMove(delta int) {
	if len(c.history) == 0 {
		return
	}
	c.histPos = max(0, min(c.histPos+delta, len(c.history)))
	if c.histPos == len(c.history) {
		c.input.SetValue("")
	} else {
		c.input.SetValue(c.history[c.histPos])
	}
	c.input.CursorEnd()
}

func (c *console) remember(expr string) {
	if len(c.history) == 0 || c.history[len(c.history)-1] != expr {
		c.history = append(c.history, expr)
	}
	c.histPos = len(c.history)
}
