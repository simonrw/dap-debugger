// Package state persists per-workspace debugger state such as breakpoints and
// watch expressions between runs.
package state

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Breakpoint is a line breakpoint in a local source file.
type Breakpoint struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	// Column makes this an inline breakpoint at a 1-based column; 0 is the whole line.
	Column    int    `json:"column,omitempty"`
	Enabled   bool   `json:"enabled"`
	Condition string `json:"condition,omitempty"`
	// HitCondition is an adapter expression such as ">= 3" for the hit count.
	HitCondition string `json:"hitCondition,omitempty"`
	// LogMessage makes this a logpoint that prints instead of stopping.
	LogMessage string `json:"logMessage,omitempty"`
}

// FunctionBreakpoint stops when a function with this name is entered.
type FunctionBreakpoint struct {
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	Condition string `json:"condition,omitempty"`
}

// Workspace is everything remembered about one workspace folder.
type Workspace struct {
	Breakpoints      []Breakpoint         `json:"breakpoints"`
	Functions        []FunctionBreakpoint `json:"functionBreakpoints,omitempty"`
	Watch            []string             `json:"watch"`
	ExceptionFilters map[string]bool      `json:"exceptionFilters"`
	Configuration    string               `json:"configuration,omitempty"`
	LastFile         string               `json:"lastFile,omitempty"`
}

type document struct {
	Workspaces map[string]*Workspace `json:"workspaces"`
}

// Store reads and writes the state file. Each save rereads the file so
// several debugger instances for different workspaces do not overwrite each other.
type Store struct {
	path      string
	workspace string
	*Workspace
}

// DefaultPath is the state file in the user's configuration directory.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "dap-debugger", "state.json"), nil
}

// Open loads the state for a workspace folder, starting empty if none was saved.
func Open(path, workspace string) (*Store, error) {
	doc, err := read(path)
	if err != nil {
		return nil, err
	}

	ws := doc.Workspaces[workspace]
	if ws == nil {
		ws = &Workspace{}
	}
	if ws.ExceptionFilters == nil {
		ws.ExceptionFilters = map[string]bool{}
	}
	return &Store{path: path, workspace: workspace, Workspace: ws}, nil
}

func read(path string) (*document, error) {
	doc := &document{Workspaces: map[string]*Workspace{}}

	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return doc, nil
	}
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal(data, doc); err != nil {
		return nil, err
	}
	if doc.Workspaces == nil {
		doc.Workspaces = map[string]*Workspace{}
	}
	return doc, nil
}

// Save writes this workspace's state atomically.
func (s *Store) Save() error {
	doc, err := read(s.path)
	if err != nil {
		doc = &document{Workspaces: map[string]*Workspace{}}
	}
	doc.Workspaces[s.workspace] = s.Workspace

	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".state-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

// BreakpointsIn returns the breakpoints in one file, sorted by line.
func (s *Store) BreakpointsIn(path string) []Breakpoint {
	var out []Breakpoint
	for _, bp := range s.Breakpoints {
		if bp.Path == path {
			out = append(out, bp)
		}
	}
	slices.SortFunc(out, func(a, b Breakpoint) int {
		if a.Line != b.Line {
			return a.Line - b.Line
		}
		return a.Column - b.Column
	})
	return out
}

// Files returns every file with breakpoints.
func (s *Store) Files() []string {
	var out []string
	for _, bp := range s.Breakpoints {
		if !slices.Contains(out, bp.Path) {
			out = append(out, bp.Path)
		}
	}
	slices.Sort(out)
	return out
}

// Find returns the index of the whole-line breakpoint at path:line, or -1.
func (s *Store) Find(path string, line int) int {
	return s.FindAt(path, line, 0)
}

// FindAt returns the index of the breakpoint at path:line:column, or -1.
func (s *Store) FindAt(path string, line, column int) int {
	return slices.IndexFunc(s.Breakpoints, func(bp Breakpoint) bool {
		return bp.Path == path && bp.Line == line && bp.Column == column
	})
}

// Toggle adds an enabled whole-line breakpoint at path:line, or removes it.
func (s *Store) Toggle(path string, line int) {
	s.ToggleAt(path, line, 0)
}

// ToggleAt adds an enabled breakpoint at path:line:column, or removes it.
func (s *Store) ToggleAt(path string, line, column int) {
	if i := s.FindAt(path, line, column); i >= 0 {
		s.Breakpoints = slices.Delete(s.Breakpoints, i, i+1)
		return
	}
	s.Breakpoints = append(s.Breakpoints, Breakpoint{Path: path, Line: line, Column: column, Enabled: true})
	s.sort()
}

// Remove deletes the breakpoint at path:line:column.
func (s *Store) Remove(path string, line, column int) {
	if i := s.FindAt(path, line, column); i >= 0 {
		s.Breakpoints = slices.Delete(s.Breakpoints, i, i+1)
	}
}

// sort orders breakpoints by file then line.
func (s *Store) sort() {
	slices.SortFunc(s.Breakpoints, func(a, b Breakpoint) int {
		if c := strings.Compare(a.Path, b.Path); c != 0 {
			return c
		}
		if a.Line != b.Line {
			return a.Line - b.Line
		}
		return a.Column - b.Column
	})
}
