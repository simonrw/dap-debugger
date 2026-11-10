package launch

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strings"
)

// TaskCommand is one process to run for a VS Code task.
type TaskCommand struct {
	Label string
	Args  []string
	Dir   string
	// Env holds KEY=VALUE additions to the environment.
	Env []string
	// Ready is set for a background task: it keeps running, and the launch
	// continues once a line of its output matches.
	Ready *regexp.Regexp
}

// providedTasks are task types that VS Code extensions provide, mapped to
// the command each runs.
var providedTasks = map[string]func(task Config) ([]string, error){
	"npm": func(task Config) ([]string, error) {
		script := task.String("script")
		if script == "" {
			return nil, errors.New("npm task needs a script")
		}
		return []string{"npm", "run", script}, nil
	},
	"typescript": func(task Config) ([]string, error) {
		args := []string{"tsc", "-p", cmpOr(task.String("tsconfig"), "tsconfig.json")}
		if task.String("option") == "watch" {
			args = append(args, "--watch")
		}
		return args, nil
	},
	"gulp":  runnerTask("gulp", "task"),
	"grunt": runnerTask("grunt", "task"),
	"jake":  runnerTask("jake", "task"),
	"cargo": func(task Config) ([]string, error) {
		command := task.String("command")
		if command == "" {
			return nil, errors.New("cargo task needs a command such as build or test")
		}
		return []string{"cargo", command}, nil
	},
	"go": func(task Config) ([]string, error) {
		command := task.String("command")
		if command == "" {
			return nil, errors.New("go task needs a command such as build or test")
		}
		return []string{"go", command}, nil
	},
}

func taskTypes() string {
	types := []string{"shell", "process"}
	for kind := range providedTasks {
		types = append(types, kind)
	}
	slices.Sort(types[2:])
	return strings.Join(types, ", ")
}

// runnerTask runs a task runner with the task named by field.
func runnerTask(runner, field string) func(Config) ([]string, error) {
	return func(task Config) ([]string, error) {
		name := task.String(field)
		if name == "" {
			return nil, fmt.Errorf("%s task needs a %s", runner, field)
		}
		return []string{runner, name}, nil
	}
}

func cmpOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// namedMatchers are background problem matchers known by name, from VS Code
// and from widely used extensions, mapped to the end pattern that marks a
// watching task as ready.
var namedMatchers = map[string]string{
	"$tsc-watch":         `(?:Found \d+ errors?\. Watching for file changes\.|Compilation complete\. Watching for file changes\.)`,
	"$esbuild-watch":     `\[watch\] build finished`,
	"$ts-webpack-watch":  `(?:compiled|Compiled|failed|Failed)`,
	"$tsl-webpack-watch": `(?:compiled|Compiled|failed|Failed)`,
}

// ProblemMatchers adds named background problem matchers, keyed by name with
// or without the leading "$", to their end patterns.
type ProblemMatchers map[string]string

// LoadProblemMatchers reads user problem matcher definitions: a JSON object of
// matcher names to background end patterns. A missing file is not an error.
func LoadProblemMatchers(path string) (ProblemMatchers, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var matchers ProblemMatchers
	if err := json.Unmarshal(StripJSONC(data), &matchers); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for name, pattern := range matchers {
		if _, err := regexp.Compile(pattern); err != nil {
			return nil, fmt.Errorf("%s: matcher %q: %w", path, name, err)
		}
	}
	return matchers, nil
}

func (u ProblemMatchers) lookup(name string) (string, bool) {
	if p, ok := u[name]; ok {
		return p, true
	}
	if p, ok := u[strings.TrimPrefix(name, "$")]; ok {
		return p, true
	}
	p, ok := namedMatchers[name]
	return p, ok
}

// readyPattern finds the background end pattern in a task's problemMatcher,
// which is a matcher, a matcher name, or a list of either.
func readyPattern(matcher any, user ProblemMatchers) (string, bool) {
	switch x := matcher.(type) {
	case string:
		return user.lookup(x)
	case map[string]any:
		if bg, ok := x["background"].(map[string]any); ok {
			switch end := bg["endsPattern"].(type) {
			case string:
				return end, true
			case map[string]any:
				if re, ok := end["regexp"].(string); ok {
					return re, true
				}
			}
		}
	case []any:
		for _, item := range x {
			if p, ok := readyPattern(item, user); ok {
				return p, true
			}
		}
	}
	return "", false
}

type tasksFile struct {
	Options map[string]any `json:"options"`
	Tasks   []Config       `json:"tasks"`
}

// ResolveTask finds a task by label in a tasks.json file and returns the
// commands to run in order, dependencies first. Shell and process tasks are
// supported, as are the npm, typescript and go task types that VS Code
// extensions provide, and background tasks with a ready pattern.
func ResolveTask(path, label string, vars Vars) ([]TaskCommand, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("task %q: %w", label, err)
	}

	var file tasksFile
	if err := json.Unmarshal(StripJSONC(data), &file); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	var out []TaskCommand
	var visit func(label string, stack []string) error
	visit = func(label string, stack []string) error {
		if slices.Contains(stack, label) {
			return fmt.Errorf("task %q depends on itself", label)
		}
		task := file.find(label)
		if task == nil {
			return fmt.Errorf("no task labelled %q in %s", label, path)
		}
		task = withPlatformOverrides(task)

		for _, dep := range dependencies(task["dependsOn"]) {
			if err := visit(dep, append(stack, label)); err != nil {
				return err
			}
		}
		kind := task.String("type")
		if task.String("command") == "" && providedTasks[kind] == nil {
			// A task without a command only groups its dependencies.
			if kind != "" && kind != "shell" && kind != "process" {
				return fmt.Errorf("task %q: task type %q is not supported (use %s)", label, kind, taskTypes())
			}
			return nil
		}

		cmd, err := taskCommand(task, file.Options, vars)
		if err != nil {
			return fmt.Errorf("task %q: %w", label, err)
		}
		out = append(out, cmd)
		return nil
	}

	if err := visit(label, nil); err != nil {
		return nil, err
	}
	return out, nil
}

func (f tasksFile) find(label string) Config {
	for _, t := range f.Tasks {
		if t.String("label") == label || t.String("taskName") == label {
			return t
		}
	}
	return nil
}

func dependencies(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		var out []string
		for _, item := range x {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func taskCommand(task Config, defaults map[string]any, vars Vars) (TaskCommand, error) {
	kind := task.String("type")
	provided := providedTasks[kind]
	if kind != "shell" && kind != "process" && provided == nil {
		return TaskCommand{}, fmt.Errorf("task type %q is not supported (use %s)", kind, taskTypes())
	}

	substituted, err := vars.Substitute(task)
	if err != nil {
		return TaskCommand{}, err
	}
	task = substituted.(Config)

	options, _ := task["options"].(map[string]any)
	if options == nil {
		options = defaults
	}

	cmd := TaskCommand{Label: cmpOr(task.String("label"), kind+": "+task.String("script")), Dir: vars.Workspace}

	// TypeScript's watch option makes a background task even without isBackground.
	if task["isBackground"] == true || kind == "typescript" && task.String("option") == "watch" {
		pattern, ok := readyPattern(task["problemMatcher"], vars.Matchers)
		if !ok && kind == "typescript" {
			pattern, ok = namedMatchers["$tsc-watch"], true
		}
		if !ok {
			return TaskCommand{}, errors.New("a background task needs a problemMatcher with background.endsPattern, or a named one that is known; define it in problemMatchers.json")
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return TaskCommand{}, fmt.Errorf("background endsPattern: %w", err)
		}
		cmd.Ready = re
	}
	if cwd, ok := options["cwd"].(string); ok && cwd != "" {
		if s, err := vars.Substitute(cwd); err == nil {
			cmd.Dir = resolvePath(vars.Workspace, s.(string))
		}
	}
	if env, ok := options["env"].(map[string]any); ok {
		for k, v := range env {
			if s, err := vars.Substitute(v); err == nil {
				cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%v", k, s))
			}
		}
		slices.Sort(cmd.Env)
	}

	var args []string
	for _, a := range listOf(task["args"]) {
		args = append(args, argValue(a))
	}

	if provided != nil {
		base, err := provided(task)
		if err != nil {
			return TaskCommand{}, err
		}
		if path := task.String("path"); path != "" && kind == "npm" {
			cmd.Dir = resolvePath(vars.Workspace, path)
		}
		cmd.Args = append(base, args...)
		return cmd, nil
	}
	if kind == "process" {
		cmd.Args = append([]string{task.String("command")}, args...)
		return cmd, nil
	}

	line := task.String("command")
	for _, a := range args {
		line += " " + shellQuote(a)
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	cmd.Args = []string{shell, "-c", line}
	return cmd, nil
}

func listOf(v any) []any {
	if x, ok := v.([]any); ok {
		return x
	}
	return nil
}

// argValue reads a task argument, which is a string or {"value": ..., "quoting": ...}.
func argValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case map[string]any:
		return fmt.Sprint(x["value"])
	}
	return fmt.Sprint(v)
}

func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`!*?[]{}()<>|&;#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
