package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"

	"github.com/simonrw/dap-debugger/internal/launch"
)

// vscodeDefaults are VS Code's default keys for the debug commands.
var vscodeDefaults = map[string][]string{
	"workbench.action.debug.start":               {"f5"},
	"workbench.action.debug.continue":            {"f5"},
	"workbench.action.debug.run":                 {"ctrl+f5"},
	"workbench.action.debug.stepOver":            {"f10"},
	"workbench.action.debug.stepInto":            {"f11"},
	"workbench.action.debug.stepOut":             {"shift+f11"},
	"workbench.action.debug.pause":               {"f6"},
	"workbench.action.debug.stop":                {"shift+f5"},
	"workbench.action.debug.restart":             {"ctrl+shift+f5"},
	"editor.debug.action.toggleBreakpoint":       {"f9"},
	"editor.debug.action.toggleInlineBreakpoint": {"shift+f9"},
}

func userKeybindingsPath() string {
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "Code", "User", "keybindings.json")
	}
	return filepath.Join(home, ".config", "Code", "User", "keybindings.json")
}

// effectiveKeys applies keybindings.json entries to the defaults: "-command"
// removes a key from a command and "command" adds one.
func effectiveKeys(t *testing.T, path string) map[string][]string {
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("no VS Code keybindings: %v", err)
	}
	var entries []struct {
		Key     string `json:"key"`
		Command string `json:"command"`
	}
	if err := json.Unmarshal(launch.StripJSONC(data), &entries); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	keys := map[string][]string{}
	for cmd, k := range vscodeDefaults {
		keys[cmd] = slices.Clone(k)
	}
	for _, e := range entries {
		k := strings.ReplaceAll(strings.ToLower(e.Key), "cmd+", "ctrl+")
		if cmd, removed := strings.CutPrefix(e.Command, "-"); removed {
			keys[cmd] = slices.DeleteFunc(keys[cmd], func(x string) bool { return x == k })
		} else if _, ok := vscodeDefaults[e.Command]; ok {
			keys[e.Command] = append(keys[e.Command], k)
		}
	}
	return keys
}

// functionKeys keeps the keys that involve a function key.
func functionKeys(keys []string) []string {
	var out []string
	for _, k := range keys {
		last := k[strings.LastIndex(k, "+")+1:]
		if len(last) > 1 && last[0] == 'f' {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// TestKeysFollowVSCodeKeybindings checks the debug keys against the user's
// VS Code keybindings.json: every function key VS Code has for a debug command
// does the same here, and no other function key is bound to it.
func TestKeysFollowVSCodeKeybindings(t *testing.T) {
	effective := effectiveKeys(t, userKeybindingsPath())
	k := newKeyMap()

	ours := map[string]key.Binding{
		"workbench.action.debug.continue":            k.Continue,
		"workbench.action.debug.run":                 k.RunNoDebug,
		"workbench.action.debug.stepOver":            k.StepOver,
		"workbench.action.debug.stepInto":            k.StepInto,
		"workbench.action.debug.stepOut":             k.StepOut,
		"workbench.action.debug.pause":               k.Pause,
		"workbench.action.debug.stop":                k.Stop,
		"workbench.action.debug.restart":             k.Restart,
		"editor.debug.action.toggleBreakpoint":       k.ToggleBreakpoint,
		"editor.debug.action.toggleInlineBreakpoint": k.InlineBreakpoint,
	}
	// F5 starts debugging; VS Code only gives it continue when the user has not removed that.
	start := functionKeys(effective["workbench.action.debug.start"])
	if got := functionKeys(k.Start.Keys()); !slices.Equal(got, start) {
		t.Errorf("start: keys %v, VS Code %v", got, start)
	}

	for cmd, b := range ours {
		want := functionKeys(effective[cmd])
		got := functionKeys(slices.DeleteFunc(slices.Clone(b.Keys()), func(x string) bool { return strings.HasPrefix(x, "super+") }))
		if !slices.Equal(got, want) {
			t.Errorf("%s: keys %v, VS Code %v", cmd, got, want)
		}
	}
	t.Logf("checked against %s", userKeybindingsPath())
}
