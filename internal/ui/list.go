package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// list tracks the cursor and scroll position of a pane made of rows.
type list struct {
	cursor int
	offset int
}

func (l *list) clamp(n, height int) {
	l.cursor = max(0, min(l.cursor, n-1))
	if l.cursor < l.offset {
		l.offset = l.cursor
	}
	if height > 0 && l.cursor >= l.offset+height {
		l.offset = l.cursor - height + 1
	}
	l.offset = max(0, min(l.offset, n-height))
}

func (l *list) move(delta, n, height int) {
	l.cursor += delta
	l.clamp(n, height)
}

// handleKey applies common navigation keys and reports whether one matched.
func (l *list) handleKey(k string, n, height int) bool {
	switch k {
	case "up", "k":
		l.move(-1, n, height)
	case "down", "j":
		l.move(1, n, height)
	case "pgup":
		l.move(-max(1, height-1), n, height)
	case "pgdown":
		l.move(max(1, height-1), n, height)
	case "home", "g":
		l.cursor = 0
		l.clamp(n, height)
	case "end", "G":
		l.cursor = n - 1
		l.clamp(n, height)
	default:
		return false
	}
	return true
}

// click selects the row at y within the pane and returns its index, or -1.
func (l *list) click(y, n, height int) int {
	i := l.offset + y
	if y < 0 || y >= height || i >= n {
		return -1
	}
	l.cursor = i
	return i
}

func (l *list) scroll(delta, n, height int) {
	l.offset = max(0, min(l.offset+delta, n-height))
	l.cursor = max(l.offset, min(l.cursor, l.offset+height-1))
}

// view renders the visible rows, highlighting the cursor row.
func (l *list) view(st styles, rows []string, width, height int, focused bool, empty string) string {
	if len(rows) == 0 {
		return fill([]string{st.dim.Render(" " + empty)}, width, height)
	}
	l.clamp(len(rows), height)

	out := make([]string, 0, height)
	for i := l.offset; i < len(rows) && len(out) < height; i++ {
		row := rows[i]
		if i == l.cursor {
			plain := fit(ansi.Strip(row), width)
			if focused {
				row = st.selected.Render(plain)
			} else {
				row = st.selectedBlur.Render(plain)
			}
		}
		out = append(out, row)
	}
	return fill(out, width, height)
}

func indent(depth int) string {
	return strings.Repeat("  ", depth)
}
