package launch

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Adapter describes how to start or reach a debug adapter. The transport
// follows from the command: "{port}" in an argument means the adapter
// listens on that port, "{address}" means it connects back to that address,
// and otherwise it speaks DAP over stdin and stdout. Address connects to an
// adapter that is already running instead.
type Adapter struct {
	Command []string          `json:"command,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`
	Address string            `json:"address,omitempty"`
}

// builtinAdapters are adapters that need no setup beyond their command.
// debugpy and Delve are built in separately because they need more.
var builtinAdapters = map[string]Adapter{
	"lldb-dap":    {Command: []string{"lldb-dap"}},
	"lldb-vscode": {Command: []string{"lldb-vscode"}},
	"gdb":         {Command: []string{"gdb", "--interpreter=dap"}},
	"coreclr":     {Command: []string{"netcoredbg", "--interpreter=vscode"}},
}

// DefaultAdaptersPath is the user's adapter definitions file.
func DefaultAdaptersPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "dap-debugger", "adapters.json"), nil
}

// LoadAdapters reads adapter definitions keyed by configuration type. A
// missing file is not an error.
func LoadAdapters(path string) (map[string]Adapter, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var adapters map[string]Adapter
	if err := json.Unmarshal(StripJSONC(data), &adapters); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for kind, a := range adapters {
		if len(a.Command) == 0 && a.Address == "" {
			return nil, fmt.Errorf("%s: adapter %q needs a command or an address", path, kind)
		}
	}
	return adapters, nil
}

// AdapterTypes lists the configuration types with a known adapter.
func AdapterTypes(user map[string]Adapter) []string {
	types := []string{"debugpy", "python", "go"}
	for kind := range builtinAdapters {
		types = append(types, kind)
	}
	for kind := range user {
		types = append(types, kind)
	}
	slices.Sort(types)
	return slices.Compact(types)
}

// configAdapter reads a configuration's own debugAdapter definition.
func configAdapter(cfg Config) (*Adapter, error) {
	raw, ok := cfg["debugAdapter"]
	if !ok {
		return nil, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var a Adapter
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, fmt.Errorf("debugAdapter: %w", err)
	}
	if len(a.Command) == 0 && a.Address == "" {
		return nil, errors.New("debugAdapter needs a command or an address")
	}
	return &a, nil
}

// useAdapter configures a plan to start or reach a generic adapter.
func useAdapter(plan *Plan, a Adapter, workspace string) {
	plan.Dir = workspace
	if a.Cwd != "" {
		plan.Dir = resolvePath(workspace, a.Cwd)
	}
	for k, v := range a.Env {
		plan.Env = append(plan.Env, k+"="+v)
	}
	slices.Sort(plan.Env)

	if a.Address != "" {
		plan.Transport = Dial
		plan.Address = a.Address
		return
	}

	plan.Command = slices.Clone(a.Command)
	plan.Command[0] = resolveExecutable(workspace, plan.Command[0])
	plan.Transport = transportFor(plan.Command)
}

func transportFor(command []string) Transport {
	for _, arg := range command {
		switch {
		case strings.Contains(arg, PortPlaceholder):
			return Listen
		case strings.Contains(arg, AddressPlaceholder):
			return Reverse
		}
	}
	return Stdio
}

// Placeholders in adapter commands, replaced when the session starts.
const (
	PortPlaceholder    = "{port}"
	AddressPlaceholder = "{address}"
)
