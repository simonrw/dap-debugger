package ui

import "charm.land/bubbles/v2/key"

// keyMap follows the VS Code debug bindings in the user's keybindings.json:
// F5 starts, F9 continues, F8 steps over and F7 steps into. Other debug keys
// keep their VS Code defaults.
type keyMap struct {
	Start      key.Binding
	RunNoDebug key.Binding
	Continue   key.Binding
	StepOver   key.Binding
	StepInto   key.Binding
	StepOut    key.Binding
	Pause      key.Binding
	Stop       key.Binding
	Restart    key.Binding

	Help          key.Binding
	QuickOpen     key.Binding
	PickConfig    key.Binding
	Explorer      key.Binding
	DebugView     key.Binding
	ToggleSidebar key.Binding
	Disassembly   key.Binding
	Quit          key.Binding

	NavLeft  key.Binding
	NavDown  key.Binding
	NavUp    key.Binding
	NavRight key.Binding
	NextPane key.Binding
	PrevPane key.Binding

	ToggleBreakpoint key.Binding
	InlineBreakpoint key.Binding
	ToggleEnabled    key.Binding
	Condition        key.Binding
	HitCount         key.Binding
	LogPoint         key.Binding
	AddFunction      key.Binding
	GotoLine         key.Binding
	Search           key.Binding
	SearchNext       key.Binding
	SearchPrev       key.Binding
	AddWatch         key.Binding
	Remove           key.Binding
}

func newKeyMap() keyMap {
	b := func(keys []string, help, desc string) key.Binding {
		return key.NewBinding(key.WithKeys(keys...), key.WithHelp(help, desc))
	}
	return keyMap{
		Start:      b([]string{"f5"}, "F5", "start"),
		RunNoDebug: b([]string{"ctrl+f5"}, "^F5", "run without debugging"),
		Continue:   b([]string{"f9"}, "F9", "continue"),
		StepOver:   b([]string{"f8"}, "F8", "step over"),
		StepInto:   b([]string{"f7"}, "F7", "step into"),
		StepOut:    b([]string{"shift+f11"}, "⇧F11", "step out"),
		Pause:      b([]string{"f6"}, "F6", "pause"),
		Stop:       b([]string{"shift+f5"}, "⇧F5", "stop"),
		Restart:    b([]string{"ctrl+shift+f5", "super+shift+f5"}, "⌃⇧F5", "restart"),

		Help:          b([]string{"f1"}, "F1", "help"),
		QuickOpen:     b([]string{"ctrl+p"}, "^P", "open file"),
		PickConfig:    b([]string{"ctrl+o"}, "^O", "configuration"),
		Explorer:      b([]string{"ctrl+e", "ctrl+shift+e"}, "^E", "explorer"),
		DebugView:     b([]string{"ctrl+r", "ctrl+shift+d"}, "^R", "debug view"),
		ToggleSidebar: b([]string{"ctrl+b"}, "^B", "toggle sidebar"),
		Disassembly:   b([]string{"ctrl+y"}, "^Y", "source/disassembly view"),
		Quit:          b([]string{"ctrl+q", "ctrl+c"}, "^Q", "quit"),

		NavLeft:  b([]string{"ctrl+h"}, "^H", "focus left"),
		NavDown:  b([]string{"ctrl+j"}, "^J", "focus down"),
		NavUp:    b([]string{"ctrl+k"}, "^K", "focus up"),
		NavRight: b([]string{"ctrl+l"}, "^L", "focus right"),
		NextPane: b([]string{"tab"}, "tab", "next pane"),
		PrevPane: b([]string{"shift+tab"}, "⇧tab", "previous pane"),

		ToggleBreakpoint: b([]string{"b"}, "b", "toggle breakpoint"),
		InlineBreakpoint: b([]string{"shift+f9", "i"}, "⇧F9/i", "inline breakpoint at cursor column"),
		ToggleEnabled:    b([]string{"B", "space"}, "B/space", "enable/disable breakpoint"),
		Condition:        b([]string{"c"}, "c", "breakpoint condition"),
		HitCount:         b([]string{"H"}, "H", "breakpoint hit count"),
		LogPoint:         b([]string{"p"}, "p", "logpoint message"),
		AddFunction:      b([]string{"f"}, "f", "add function breakpoint"),
		GotoLine:         b([]string{"ctrl+g", ":"}, "^G", "go to line"),
		Search:           b([]string{"/"}, "/", "search"),
		SearchNext:       b([]string{"n"}, "n/N", "next/previous match"),
		SearchPrev:       b([]string{"N"}, "N", "previous match"),
		AddWatch:         b([]string{"a", "+"}, "a", "add watch"),
		Remove:           b([]string{"d", "delete", "backspace"}, "d", "remove"),
	}
}
