package ui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/simonrw/dap-debugger/internal/fzf"
)

// modal is a dialog drawn over the main view that takes all keyboard input.
type modal struct {
	title string
	input textinput.Model

	// items, when set, makes this a picker filtered by the input.
	items    []string
	filtered []int
	list     list
	// fuzzyFilter matches items fuzzily instead of by substring.
	fuzzyFilter bool

	onSubmit func(m *Model, value string, index int) tea.Cmd
}

const pickerRows = 12

func newPrompt(title, value, placeholder string, onSubmit func(m *Model, value string) tea.Cmd) *modal {
	in := textinput.New()
	in.Prompt = "› "
	in.Placeholder = placeholder
	in.SetVirtualCursor(true)
	in.SetValue(value)
	in.CursorEnd()
	in.Focus()
	return &modal{
		title: title,
		input: in,
		onSubmit: func(m *Model, value string, _ int) tea.Cmd {
			return onSubmit(m, value)
		},
	}
}

func newPicker(title string, items []string, selected int, fuzzyFilter bool, onSelect func(m *Model, index int) tea.Cmd) *modal {
	in := textinput.New()
	in.Prompt = "› "
	in.Placeholder = "Type to filter"
	in.SetVirtualCursor(true)
	in.Focus()
	md := &modal{
		title:       title,
		input:       in,
		items:       items,
		fuzzyFilter: fuzzyFilter,
		onSubmit: func(m *Model, _ string, index int) tea.Cmd {
			if index < 0 {
				return nil
			}
			return onSelect(m, index)
		},
	}
	md.filter()
	md.list.cursor = max(0, selected)
	md.list.clamp(len(md.filtered), pickerRows)
	return md
}

func (md *modal) isPicker() bool {
	return md.items != nil
}

func (md *modal) filter() {
	query := md.input.Value()
	md.filtered = md.filtered[:0]

	if md.fuzzyFilter {
		md.filtered = append(md.filtered, fzf.Filter(query, md.items)...)
	} else {
		q := strings.ToLower(query)
		for i, item := range md.items {
			if strings.Contains(strings.ToLower(item), q) {
				md.filtered = append(md.filtered, i)
			}
		}
	}
	md.list.cursor = 0
	md.list.offset = 0
}

// update handles a key and reports whether the modal should close.
func (md *modal) update(m *Model, msg tea.KeyPressMsg) (bool, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		return true, nil
	case "enter":
		index := -1
		if md.isPicker() {
			if md.list.cursor < len(md.filtered) {
				index = md.filtered[md.list.cursor]
			}
		}
		return true, md.onSubmit(m, md.input.Value(), index)
	}

	if md.isPicker() {
		switch msg.String() {
		case "up", "ctrl+k", "ctrl+p":
			md.list.move(-1, len(md.filtered), pickerRows)
			return false, nil
		case "down", "ctrl+j", "ctrl+n":
			md.list.move(1, len(md.filtered), pickerRows)
			return false, nil
		case "pgup":
			md.list.move(-pickerRows, len(md.filtered), pickerRows)
			return false, nil
		case "pgdown":
			md.list.move(pickerRows, len(md.filtered), pickerRows)
			return false, nil
		}
	}

	before := md.input.Value()
	var cmd tea.Cmd
	md.input, cmd = md.input.Update(msg)
	if md.isPicker() && md.input.Value() != before {
		md.filter()
	}
	return false, cmd
}

func (md *modal) view(st styles, width int) string {
	inner := max(10, width-4)
	md.input.SetWidth(inner - 2)

	lines := []string{st.modalTitle.Render(md.title), md.input.View()}
	if md.isPicker() {
		rows := make([]string, len(md.filtered))
		for i, idx := range md.filtered {
			rows[i] = " " + md.items[idx]
		}
		height := min(pickerRows, max(1, len(rows)))
		lines = append(lines, md.list.view(st, rows, inner, height, true, "No matches"))
	}
	return st.modal.Width(width).Render(strings.Join(lines, "\n"))
}
