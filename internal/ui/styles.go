package ui

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// styles holds every style used by the UI, derived from the terminal background.
type styles struct {
	isDark bool

	base          lipgloss.Style
	dim           lipgloss.Style
	bold          lipgloss.Style
	separator     lipgloss.Style
	header        lipgloss.Style
	headerFocused lipgloss.Style
	tab           lipgloss.Style
	tabActive     lipgloss.Style
	selected      lipgloss.Style
	selectedBlur  lipgloss.Style

	statusIdle  lipgloss.Style
	statusDebug lipgloss.Style

	breakpoint   lipgloss.Style
	bpDisabled   lipgloss.Style
	bpUnverified lipgloss.Style
	execArrow    lipgloss.Style
	frameArrow   lipgloss.Style
	lineNumber   lipgloss.Style
	lineNumberHi lipgloss.Style

	execBg   color.Color
	frameBg  color.Color
	cursorBg color.Color
	searchBg color.Color

	varName   lipgloss.Style
	varString lipgloss.Style
	varNumber lipgloss.Style
	varOther  lipgloss.Style
	dir       lipgloss.Style

	stderr  lipgloss.Style
	console lipgloss.Style
	info    lipgloss.Style
	input   lipgloss.Style
	result  lipgloss.Style

	modal      lipgloss.Style
	modalTitle lipgloss.Style
}

func newStyles(isDark bool) styles {
	pick := lipgloss.LightDark(isDark)
	c := lipgloss.Color

	fg := pick(c("#1f1f1f"), c("#cccccc"))
	dim := pick(c("#8a8a8a"), c("#808080"))
	accent := c("#007acc")
	selection := pick(c("#cce5ff"), c("#04395e"))
	selectionBlur := pick(c("#e4e6f1"), c("#37373d"))

	s := styles{isDark: isDark}
	s.base = lipgloss.NewStyle().Foreground(fg)
	s.dim = lipgloss.NewStyle().Foreground(dim)
	s.bold = lipgloss.NewStyle().Foreground(fg).Bold(true)
	s.separator = lipgloss.NewStyle().Foreground(pick(c("#d0d0d0"), c("#3c3c3c")))
	s.header = lipgloss.NewStyle().Foreground(dim).Bold(true)
	s.headerFocused = lipgloss.NewStyle().Foreground(accent).Bold(true)
	s.tab = lipgloss.NewStyle().Foreground(dim).Padding(0, 1)
	s.tabActive = lipgloss.NewStyle().Foreground(fg).Bold(true).Underline(true).Padding(0, 1)
	s.selected = lipgloss.NewStyle().Foreground(pick(c("#000000"), c("#ffffff"))).Background(selection)
	s.selectedBlur = lipgloss.NewStyle().Foreground(fg).Background(selectionBlur)

	white := c("#ffffff")
	s.statusIdle = lipgloss.NewStyle().Foreground(white).Background(accent)
	s.statusDebug = lipgloss.NewStyle().Foreground(white).Background(c("#cc6633"))

	s.breakpoint = lipgloss.NewStyle().Foreground(c("#e51400"))
	s.bpDisabled = lipgloss.NewStyle().Foreground(dim)
	s.bpUnverified = lipgloss.NewStyle().Foreground(dim)
	s.execArrow = lipgloss.NewStyle().Foreground(pick(c("#b58900"), c("#ffcc00"))).Bold(true)
	s.frameArrow = lipgloss.NewStyle().Foreground(pick(c("#388a34"), c("#89d185"))).Bold(true)
	s.lineNumber = lipgloss.NewStyle().Foreground(pick(c("#9a9a9a"), c("#6e7681")))
	s.lineNumberHi = lipgloss.NewStyle().Foreground(fg)

	s.execBg = pick(c("#fff5c2"), c("#4b4b18"))
	s.frameBg = pick(c("#d7f0d6"), c("#1e4d2b"))
	s.cursorBg = pick(c("#eeeeee"), c("#2a2d2e"))
	s.searchBg = pick(c("#ffe08a"), c("#623315"))

	s.varName = lipgloss.NewStyle().Foreground(pick(c("#001080"), c("#9cdcfe")))
	s.varString = lipgloss.NewStyle().Foreground(pick(c("#a31515"), c("#ce9178")))
	s.varNumber = lipgloss.NewStyle().Foreground(pick(c("#098658"), c("#b5cea8")))
	s.varOther = lipgloss.NewStyle().Foreground(pick(c("#0000ff"), c("#569cd6")))
	s.dir = lipgloss.NewStyle().Foreground(fg).Bold(true)

	s.stderr = lipgloss.NewStyle().Foreground(pick(c("#cd3131"), c("#f48771")))
	s.console = lipgloss.NewStyle().Foreground(dim)
	s.info = lipgloss.NewStyle().Foreground(accent)
	s.input = lipgloss.NewStyle().Foreground(fg).Bold(true)
	s.result = lipgloss.NewStyle().Foreground(fg)

	s.modal = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(accent).
		Padding(0, 1)
	s.modalTitle = lipgloss.NewStyle().Foreground(accent).Bold(true)
	return s
}
