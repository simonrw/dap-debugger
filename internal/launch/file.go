// Package launch reads VS Code launch.json files and turns a configuration into
// a plan for starting a debug adapter.
package launch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Config is one entry of the "configurations" array, kept as raw JSON values so
// adapter-specific fields pass through untouched.
type Config map[string]any

// Name returns the configuration's display name.
func (c Config) Name() string {
	return c.String("name")
}

// String returns a string field, or "" when it is absent or not a string.
func (c Config) String(key string) string {
	s, _ := c[key].(string)
	return s
}

// Compound starts several configurations together.
type Compound struct {
	Name           string `json:"name"`
	Configurations []any  `json:"configurations"`
	PreLaunchTask  string `json:"preLaunchTask"`
	// StopAll stops every session of the compound when one is stopped.
	StopAll bool `json:"stopAll"`
}

// Members returns the names of the compound's configurations, which are
// strings or {"name": ...} objects.
func (c *Compound) Members() []string {
	var names []string
	for _, item := range c.Configurations {
		switch x := item.(type) {
		case string:
			names = append(names, x)
		case map[string]any:
			if name, ok := x["name"].(string); ok {
				names = append(names, name)
			}
		}
	}
	return names
}

// File is a parsed launch.json.
type File struct {
	Path           string
	Workspace      string
	Configurations []Config
	Compounds      []*Compound
}

// Load reads a launch.json file, which may contain comments and trailing commas.
// The workspace folder is the parent of the .vscode directory holding the file,
// or the file's own directory otherwise.
func Load(path string) (*File, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}

	var doc struct {
		Configurations []Config    `json:"configurations"`
		Compounds      []*Compound `json:"compounds"`
	}
	if err := json.Unmarshal(StripJSONC(data), &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", abs, err)
	}

	if len(doc.Configurations) == 0 && len(doc.Compounds) == 0 {
		return nil, fmt.Errorf("%s has no configurations", abs)
	}

	for i, c := range doc.Configurations {
		if c.Name() == "" {
			return nil, fmt.Errorf("configuration %d has no name", i+1)
		}
		if c.String("type") == "" {
			return nil, fmt.Errorf("configuration %q has no type", c.Name())
		}
		if r := c.String("request"); r != "launch" && r != "attach" {
			return nil, fmt.Errorf("configuration %q: request must be launch or attach", c.Name())
		}
	}

	f := &File{Path: abs, Configurations: doc.Configurations, Compounds: doc.Compounds}
	for _, c := range doc.Compounds {
		if c.Name == "" {
			return nil, errors.New("a compound has no name")
		}
		members := c.Members()
		if len(members) == 0 {
			return nil, fmt.Errorf("compound %q has no configurations", c.Name)
		}
		for _, name := range members {
			if _, err := f.Find(name); err != nil {
				return nil, fmt.Errorf("compound %q: %w", c.Name, err)
			}
		}
	}

	dir := filepath.Dir(abs)
	workspace := dir
	if filepath.Base(dir) == ".vscode" {
		workspace = filepath.Dir(dir)
	}

	f.Workspace = workspace
	return f, nil
}

// Find returns the configuration with the given name.
func (f *File) Find(name string) (Config, error) {
	for _, c := range f.Configurations {
		if c.Name() == name {
			return c, nil
		}
	}
	return nil, fmt.Errorf("no configuration named %q", name)
}

// StripJSONC removes comments and trailing commas so the result is plain JSON.
func StripJSONC(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inString := false

	for i := 0; i < len(data); i++ {
		c := data[i]

		if inString {
			out = append(out, c)
			if c == '\\' && i+1 < len(data) {
				i++
				out = append(out, data[i])
			} else if c == '"' {
				inString = false
			}
			continue
		}

		switch {
		case c == '"':
			inString = true
			out = append(out, c)
		case c == '/' && i+1 < len(data) && data[i+1] == '/':
			for i < len(data) && data[i] != '\n' {
				i++
			}
			if i < len(data) {
				out = append(out, '\n')
			}
		case c == '/' && i+1 < len(data) && data[i+1] == '*':
			i += 2
			for i+1 < len(data) && !(data[i] == '*' && data[i+1] == '/') {
				i++
			}
			i++
		case c == '}' || c == ']':
			out = dropTrailingComma(out)
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return out
}

func dropTrailingComma(out []byte) []byte {
	j := len(out) - 1
	for j >= 0 && (out[j] == ' ' || out[j] == '\t' || out[j] == '\n' || out[j] == '\r') {
		j--
	}
	if j >= 0 && out[j] == ',' {
		return append(out[:j], out[j+1:]...)
	}
	return out
}

// ErrUnsupportedVariable reports a ${...} variable that needs editor integration.
var ErrUnsupportedVariable = errors.New("unsupported variable")
