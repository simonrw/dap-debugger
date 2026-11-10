package ui

import (
	"bytes"
	"fmt"
	"image/color"
	"os"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	chromastyles "github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/x/ansi"
	godap "github.com/google/go-dap"
)

const maxSourceSize = 8 * 1024 * 1024

type token struct {
	text string
	kind chroma.TokenType
}

// editor is a read-only source view with a breakpoint gutter.
type editor struct {
	// path is the local file shown, or "" for adapter-provided source.
	path string
	// src is the adapter-provided source shown when path is "".
	src     godap.Source
	name    string
	lines   [][]token
	message string

	cursor int
	// col is the cursor's 0-based column, used for inline breakpoints.
	col    int
	offset int
	xoff   int
	search string

	palette map[chroma.TokenType]lipgloss.Style
	cache   map[int]string
}

// gutterMark describes the decorations for one source line.
type gutterMark struct {
	bp      string
	bpStyle lipgloss.Style
}

// inlineMark highlights the character where an inline breakpoint is.
type inlineMark struct {
	column int
	style  lipgloss.Style
}

type editorDecorations struct {
	marks  map[int]gutterMark
	inline map[int][]inlineMark
	// hints are columns where an inline breakpoint can go on the cursor line.
	hints     []int
	hintStyle lipgloss.Style
	execLine  int
	frameLine int
}

func (e *editor) setPalette(isDark bool) {
	name := "github"
	if isDark {
		name = "github-dark"
	}
	style := chromastyles.Get(name)

	e.palette = map[chroma.TokenType]lipgloss.Style{}
	for _, t := range style.Types() {
		entry := style.Get(t)
		s := lipgloss.NewStyle()
		if entry.Colour.IsSet() {
			s = s.Foreground(lipgloss.Color(entry.Colour.String()))
		}
		if entry.Bold == chroma.Yes {
			s = s.Bold(true)
		}
		if entry.Italic == chroma.Yes {
			s = s.Italic(true)
		}
		e.palette[t] = s
	}
	e.cache = map[int]string{}
}

func (e *editor) styleFor(t chroma.TokenType) lipgloss.Style {
	for {
		if s, ok := e.palette[t]; ok {
			return s
		}
		parent := t.Parent()
		if parent == t {
			return lipgloss.NewStyle()
		}
		t = parent
	}
}

// openFile loads a local file for display.
func (e *editor) openFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() > maxSourceSize {
		return fmt.Errorf("%s is too large to display", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
		return fmt.Errorf("%s is a binary file", path)
	}

	e.load(path, path, string(data))
	return nil
}

// load shows content under a name; path is "" when it is not a local file.
func (e *editor) load(path, name, content string) {
	if e.path != path || e.name != name {
		e.cursor, e.col, e.offset, e.xoff = 0, 0, 0, 0
	}
	e.path = path
	e.name = name
	e.src = godap.Source{}
	e.message = ""
	e.lines = tokenize(name, content)
	e.cache = map[int]string{}
	e.cursor = min(e.cursor, max(0, len(e.lines)-1))
}

func (e *editor) clear(message string) {
	e.path, e.name, e.lines, e.src = "", "", nil, godap.Source{}
	e.message = message
	e.cache = map[int]string{}
	e.cursor, e.offset, e.xoff = 0, 0, 0
}

func tokenize(name, content string) [][]token {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\t", "    ")
	content = strings.TrimSuffix(content, "\n")

	lexer := lexers.Match(name)
	if lexer == nil {
		lexer = lexers.Analyse(content)
	}
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)

	lines := [][]token{{}}
	iter, err := lexer.Tokenise(nil, content)
	if err != nil {
		for i, l := range strings.Split(content, "\n") {
			if i > 0 {
				lines = append(lines, nil)
			}
			lines[i] = []token{{text: l, kind: chroma.Text}}
		}
		return lines
	}

	for tok := iter(); tok != chroma.EOF; tok = iter() {
		parts := strings.Split(tok.Value, "\n")
		for i, part := range parts {
			if i > 0 {
				lines = append(lines, nil)
			}
			if part != "" {
				last := len(lines) - 1
				lines[last] = append(lines[last], token{text: sanitize(part), kind: tok.Type})
			}
		}
	}
	// Lexers may append a newline to the final line, adding a phantom line.
	if want := strings.Count(content, "\n") + 1; len(lines) > want {
		lines = lines[:want]
	}
	return lines
}

func (e *editor) lineText(i int) string {
	var b strings.Builder
	for _, t := range e.lines[i] {
		b.WriteString(t.text)
	}
	return b.String()
}

// renderLine styles one line, painting bg behind the text when set.
func (e *editor) renderLine(i int, bg color.Color) string {
	if bg == nil {
		if s, ok := e.cache[i]; ok {
			return s
		}
	}

	var b strings.Builder
	for _, t := range e.lines[i] {
		st := e.styleFor(t.kind)
		if bg != nil {
			st = st.Background(bg)
		}
		b.WriteString(st.Render(t.text))
	}

	if bg == nil {
		e.cache[i] = b.String()
	}
	return b.String()
}

func (e *editor) move(delta, height int) {
	e.setCursor(e.cursor+delta, height)
}

func (e *editor) setCursor(line, height int) {
	if len(e.lines) == 0 {
		return
	}
	e.cursor = max(0, min(line, len(e.lines)-1))
	e.scrollToCursor(height)
}

func (e *editor) scrollToCursor(height int) {
	if e.cursor < e.offset {
		e.offset = e.cursor
	}
	if e.cursor >= e.offset+height {
		e.offset = e.cursor - height + 1
	}
	e.offset = max(0, min(e.offset, max(0, len(e.lines)-height)))
}

// center scrolls so line sits in the middle of the view.
func (e *editor) center(line, height int) {
	if len(e.lines) == 0 {
		return
	}
	e.cursor = max(0, min(line, len(e.lines)-1))
	e.offset = max(0, min(e.cursor-height/2, len(e.lines)-height))
}

func (e *editor) scroll(delta, height int) {
	e.offset = max(0, min(e.offset+delta, len(e.lines)-height))
	if e.cursor < e.offset {
		e.cursor = e.offset
	}
	if e.cursor >= e.offset+height {
		e.cursor = e.offset + height - 1
	}
}

// find moves to the next line containing the search text in the given direction.
func (e *editor) find(forward bool, height int) bool {
	if e.search == "" || len(e.lines) == 0 {
		return false
	}
	needle := strings.ToLower(e.search)
	n := len(e.lines)
	for step := 1; step <= n; step++ {
		i := e.cursor + step
		if !forward {
			i = e.cursor - step
		}
		i = ((i % n) + n) % n
		if strings.Contains(strings.ToLower(e.lineText(i)), needle) {
			e.center(i, height)
			return true
		}
	}
	return false
}

func (e *editor) gutterWidth() int {
	return 3 + len(strconv.Itoa(max(1, len(e.lines)))) + 1
}

func (e *editor) view(st styles, width, height int, focused bool, deco editorDecorations) string {
	if len(e.lines) == 0 {
		msg := e.message
		if msg == "" {
			msg = "Empty file"
		}
		lines := []string{"", "  " + st.dim.Render(msg)}
		return fill(lines, width, height)
	}

	e.offset = max(0, min(e.offset, max(0, len(e.lines)-height)))
	digits := len(strconv.Itoa(len(e.lines)))
	gutter := e.gutterWidth()
	codeWidth := max(1, width-gutter)

	out := make([]string, 0, height)
	for row := 0; row < height; row++ {
		i := e.offset + row
		if i >= len(e.lines) {
			out = append(out, "")
			continue
		}
		lineNo := i + 1

		var bg color.Color
		switch {
		case lineNo == deco.execLine:
			bg = st.execBg
		case lineNo == deco.frameLine:
			bg = st.frameBg
		case i == e.cursor && focused:
			bg = st.cursorBg
		case e.search != "" && strings.Contains(strings.ToLower(e.lineText(i)), strings.ToLower(e.search)):
			bg = st.searchBg
		}

		bp := " "
		if mark, ok := deco.marks[lineNo]; ok {
			bp = mark.bpStyle.Render(mark.bp)
		}
		arrow := " "
		if lineNo == deco.execLine {
			arrow = st.execArrow.Render("▶")
		} else if lineNo == deco.frameLine {
			arrow = st.frameArrow.Render("▶")
		}
		numStyle := st.lineNumber
		if i == e.cursor {
			numStyle = st.lineNumberHi
		}
		number := numStyle.Render(fmt.Sprintf("%*d", digits, lineNo))

		line := e.renderLine(i, bg)
		if i == e.cursor {
			for _, c := range deco.hints {
				line = markColumn(line, c, deco.hintStyle)
			}
		}
		for _, mk := range deco.inline[lineNo] {
			line = markColumn(line, mk.column, mk.style.Bold(true).Underline(true))
		}
		if i == e.cursor && focused {
			line = markColumn(line, e.col, lipgloss.NewStyle().Reverse(true))
		}
		code := ansi.Cut(line, e.xoff, e.xoff+codeWidth)
		if pad := codeWidth - ansi.StringWidth(code); pad > 0 {
			padStyle := lipgloss.NewStyle()
			if bg != nil {
				padStyle = padStyle.Background(bg)
			}
			code += padStyle.Render(strings.Repeat(" ", pad))
		}

		out = append(out, " "+bp+arrow+number+" "+code)
	}
	return strings.Join(out, "\n")
}

// markColumn restyles the character at a 0-based column of a styled line,
// padding the line when the column is past its end.
func markColumn(s string, col int, style lipgloss.Style) string {
	if w := ansi.StringWidth(s); col >= w {
		s += strings.Repeat(" ", col-w+1)
	}
	end := ansi.StringWidth(s)
	ch := ansi.Strip(ansi.Cut(s, col, col+1))
	return ansi.Cut(s, 0, col) + style.Render(ch) + ansi.Cut(s, col+1, end)
}

// moveColumn moves the column cursor and scrolls sideways to keep it visible.
func (e *editor) moveColumn(delta, width int) {
	if len(e.lines) == 0 {
		return
	}
	e.col = max(0, min(e.col+delta, ansi.StringWidth(e.lineText(e.cursor))))
	codeWidth := max(1, width-e.gutterWidth())
	if e.col < e.xoff {
		e.xoff = e.col
	}
	if e.col >= e.xoff+codeWidth {
		e.xoff = e.col - codeWidth + 1
	}
}

// fill pads lines to exactly width columns and height rows.
func fill(lines []string, width, height int) string {
	out := make([]string, height)
	for i := range out {
		if i < len(lines) {
			out[i] = fit(lines[i], width)
		} else {
			out[i] = strings.Repeat(" ", width)
		}
	}
	return strings.Join(out, "\n")
}

// fit truncates or pads a styled line to width columns.
func fit(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = ansi.Truncate(s, width, "…")
	if w := ansi.StringWidth(s); w < width {
		s += strings.Repeat(" ", width-w)
	}
	return s
}

// sanitize removes escape sequences and control characters from untrusted text.
func sanitize(s string) string {
	s = ansi.Strip(s)
	return strings.Map(func(r rune) rune {
		if r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return -1
		}
		return r
	}, s)
}
