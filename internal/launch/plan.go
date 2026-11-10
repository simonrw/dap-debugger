package launch

import (
	"bufio"
	"context"
	"fmt"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Transport says how to reach the debug adapter.
type Transport int

const (
	// Stdio spawns Command and speaks DAP over its stdin and stdout.
	Stdio Transport = iota
	// Dial connects to an adapter already listening on Address.
	Dial
	// Reverse listens locally and spawns Command with the address in place of
	// AddressPlaceholder, so the adapter connects back.
	Reverse
	// Listen spawns Command with a free port in place of PortPlaceholder and
	// connects once the adapter listens on it.
	Listen
)

// Plan is a resolved configuration: how to reach the adapter and what to send it.
type Plan struct {
	Name      string
	Type      string
	Request   string
	AdapterID string
	Transport Transport
	Command   []string
	Dir       string
	// Env holds KEY=VALUE additions to the adapter's environment.
	Env       []string
	Address   string
	Arguments map[string]any
	// Stdin gives the adapter process an input pipe that the debuggee inherits,
	// standing in for VS Code running the adapter in a terminal.
	Stdin bool
	// PreLaunchTask and PostDebugTask are labels of tasks in tasks.json.
	PreLaunchTask string
	PostDebugTask string
	// Warnings describe configuration that was ignored or changed.
	Warnings []string
}

// Launched reports whether the debuggee is owned by this session and should be
// terminated when the session stops.
func (p *Plan) Launched() bool {
	return p.Request == "launch"
}

// clientOnly keys are interpreted here and not passed to the adapter.
var clientOnly = []string{
	"envFile", "debugAdapter", "debugAdapterPython", "debugServer", "dlvToolPath", "dlvFlags",
	"preLaunchTask", "postDebugTask", "osx", "linux", "windows",
}

// HasDebugpy reports whether a Python interpreter can import debugpy.
var HasDebugpy = func(python string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, python, "-c", "import debugpy").Run() == nil
}

// Prepare substitutes variables in cfg and works out how to start its adapter.
func Prepare(cfg Config, vars Vars) (*Plan, error) {
	cfg = withPlatformOverrides(cfg)

	substituted, err := vars.Substitute(cfg)
	if err != nil {
		return nil, fmt.Errorf("configuration %q: %w", cfg.Name(), err)
	}
	cfg = substituted.(Config)

	plan := &Plan{
		Name:    cfg.Name(),
		Type:    cfg.String("type"),
		Request: cfg.String("request"),
	}

	plan.PreLaunchTask = cfg.String("preLaunchTask")
	plan.PostDebugTask = cfg.String("postDebugTask")

	if err := applyEnvFile(cfg, vars.Workspace); err != nil {
		return nil, err
	}

	plan.AdapterID = plan.Type
	custom, err := configAdapter(cfg)
	if err != nil {
		return nil, fmt.Errorf("configuration %q: %w", plan.Name, err)
	}
	if custom == nil {
		if a, ok := vars.Adapters[plan.Type]; ok {
			custom = &a
		}
	}

	switch {
	case custom != nil:
		useAdapter(plan, *custom, vars.Workspace)
	case cfg["debugServer"] != nil:
	case plan.Type == "debugpy" || plan.Type == "python":
		err = preparePython(plan, cfg, vars.Workspace)
	case plan.Type == "go":
		err = prepareGo(plan, cfg, vars.Workspace)
	default:
		a, ok := builtinAdapters[plan.Type]
		if !ok {
			return nil, fmt.Errorf("configuration %q: no debug adapter for type %q; define one in adapters.json, or set debugAdapter or debugServer in the configuration", plan.Name, plan.Type)
		}
		useAdapter(plan, a, vars.Workspace)
	}
	if err != nil {
		return nil, err
	}

	// debugServer points at an adapter that is already running, for any type.
	if port, ok := number(cfg["debugServer"]); ok {
		plan.Transport = Dial
		plan.Address = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		plan.Command = nil
	}

	plan.Arguments = maps.Clone(cfg)
	for _, k := range clientOnly {
		delete(plan.Arguments, k)
	}
	return plan, nil
}

func withPlatformOverrides(cfg Config) Config {
	platform := map[string]string{"darwin": "osx", "linux": "linux", "windows": "windows"}[runtime.GOOS]
	out := maps.Clone(cfg)
	if override, ok := cfg[platform].(map[string]any); ok {
		maps.Copy(out, override)
	}
	return out
}

func preparePython(plan *Plan, cfg Config, workspace string) error {
	plan.AdapterID = "debugpy"

	interpreter := cfg.String("python")
	if interpreter == "" {
		interpreter = workspaceVenvPython(workspace)
		if interpreter != "" {
			cfg["python"] = interpreter
		}
	} else {
		interpreter = resolveExecutable(workspace, interpreter)
		cfg["python"] = interpreter
	}

	if plan.Request == "attach" {
		if connect, ok := cfg["connect"].(map[string]any); ok {
			address, err := hostPort(connect["host"], connect["port"])
			if err != nil {
				return fmt.Errorf("configuration %q: connect: %w", plan.Name, err)
			}
			plan.Transport = Dial
			plan.Address = address
			return nil
		}
		if cfg["port"] != nil {
			address, err := hostPort(cfg["host"], cfg["port"])
			if err != nil {
				return fmt.Errorf("configuration %q: %w", plan.Name, err)
			}
			plan.Transport = Dial
			plan.Address = address
			return nil
		}
		if cfg["listen"] != nil {
			return fmt.Errorf("configuration %q: listen is not supported; start the target with debugpy --listen and use connect", plan.Name)
		}
		if cfg["processId"] == nil {
			return fmt.Errorf("configuration %q: attach needs connect.host/connect.port or processId", plan.Name)
		}
	} else {
		if cfg["program"] == nil && cfg["module"] == nil && cfg["code"] == nil {
			return fmt.Errorf("configuration %q: launch needs program, module or code", plan.Name)
		}
		if cfg.String("cwd") == "" {
			cfg["cwd"] = workspace
		} else {
			cfg["cwd"] = resolvePath(workspace, cfg.String("cwd"))
		}
		// Without a console debugpy uses integratedTerminal; the session then
		// runs the program with its input fed from the debug console.
		if cfg.String("console") == "internalConsole" && cfg["redirectOutput"] == nil {
			cfg["redirectOutput"] = true
		}
	}

	command, err := debugpyAdapter(cfg.String("debugAdapterPython"), workspace, interpreter)
	if err != nil {
		return fmt.Errorf("configuration %q: %w", plan.Name, err)
	}
	if command[0] == "uv" && interpreter == "" && plan.Request == "launch" {
		// Without this the program would run in uv's throwaway environment.
		if python, err := exec.LookPath("python3"); err == nil {
			cfg["python"] = python
		}
	}
	plan.Transport = Stdio
	plan.Command = command
	plan.Dir = workspace
	return nil
}

func workspaceVenvPython(workspace string) string {
	for _, venv := range []string{".venv", "venv"} {
		candidate := filepath.Join(workspace, venv, "bin", "python")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

// debugpyAdapter returns the command for a debugpy adapter, preferring an
// interpreter that already has debugpy and falling back to a throwaway uv
// environment. The debuggee's interpreter does not need debugpy: the adapter
// injects it.
func debugpyAdapter(explicit, workspace, interpreter string) ([]string, error) {
	if explicit != "" {
		return []string{resolveExecutable(workspace, explicit), "-m", "debugpy.adapter"}, nil
	}

	var candidates []string
	if interpreter != "" {
		candidates = append(candidates, interpreter)
	}
	if venv := os.Getenv("VIRTUAL_ENV"); venv != "" {
		candidates = append(candidates, filepath.Join(venv, "bin", "python"))
	}
	candidates = append(candidates, "python3", "python")

	for _, c := range candidates {
		if HasDebugpy(c) {
			return []string{c, "-m", "debugpy.adapter"}, nil
		}
	}
	if _, err := exec.LookPath("uv"); err == nil {
		return []string{"uv", "run", "--quiet", "--no-project", "--with", "debugpy", "python", "-m", "debugpy.adapter"}, nil
	}
	return nil, fmt.Errorf("no Python interpreter with debugpy found (tried %s); install it with 'pip install debugpy' or set debugAdapterPython", strings.Join(candidates, ", "))
}

func prepareGo(plan *Plan, cfg Config, workspace string) error {
	plan.AdapterID = "go"

	if console := cfg.String("console"); console == "integratedTerminal" || console == "externalTerminal" {
		plan.Stdin = plan.Request == "launch"
		delete(cfg, "console")
	}

	mode := cfg.String("mode")

	if plan.Request == "attach" {
		if mode == "" {
			mode = "local"
		}
		cfg["mode"] = mode

		switch mode {
		case "remote":
			address, err := hostPort(cfg["host"], cfg["port"])
			if err != nil {
				return fmt.Errorf("configuration %q: %w", plan.Name, err)
			}
			plan.Transport = Dial
			plan.Address = address
			return nil
		case "local":
			pid, ok := number(cfg["processId"])
			if !ok {
				return fmt.Errorf("configuration %q: local attach needs a numeric processId", plan.Name)
			}
			cfg["processId"] = pid
		default:
			return fmt.Errorf("configuration %q: attach mode must be local or remote", plan.Name)
		}
	} else {
		program := cfg.String("program")
		if program == "" {
			return fmt.Errorf("configuration %q: launch needs program", plan.Name)
		}
		program = resolvePath(workspace, program)
		cfg["program"] = program

		if mode == "" || mode == "auto" {
			mode = "debug"
			if strings.HasSuffix(program, "_test.go") {
				mode = "test"
			}
		}
		cfg["mode"] = mode

		if cwd := cfg.String("cwd"); cwd != "" {
			cfg["cwd"] = resolvePath(workspace, cwd)
			plan.Dir = cfg.String("cwd")
		} else if info, err := os.Stat(program); err == nil && info.IsDir() {
			plan.Dir = program
		} else {
			plan.Dir = filepath.Dir(program)
		}
	}

	if plan.Dir == "" {
		plan.Dir = workspace
	}

	dlv, err := findDlv(cfg.String("dlvToolPath"), workspace)
	if err != nil {
		return fmt.Errorf("configuration %q: %w", plan.Name, err)
	}

	plan.Transport = Reverse
	plan.Command = []string{dlv, "dap", "--client-addr=" + AddressPlaceholder}
	if flags, ok := cfg["dlvFlags"].([]any); ok {
		for _, f := range flags {
			if s, ok := f.(string); ok {
				plan.Command = append(plan.Command, s)
			}
		}
	}
	return nil
}

func findDlv(explicit, workspace string) (string, error) {
	if explicit != "" {
		return resolveExecutable(workspace, explicit), nil
	}
	if path, err := exec.LookPath("dlv"); err == nil {
		return path, nil
	}

	var dirs []string
	if gobin := os.Getenv("GOBIN"); gobin != "" {
		dirs = append(dirs, gobin)
	}
	if gopath := os.Getenv("GOPATH"); gopath != "" {
		dirs = append(dirs, filepath.Join(gopath, "bin"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, "go", "bin"))
	}
	for _, d := range dirs {
		candidate := filepath.Join(d, "dlv")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("dlv not found; install it with 'go install github.com/go-delve/delve/cmd/dlv@latest' or set dlvToolPath")
}

// applyEnvFile merges KEY=VALUE lines from envFile into env, with env taking precedence.
func applyEnvFile(cfg Config, workspace string) error {
	path := cfg.String("envFile")
	if path == "" {
		return nil
	}
	path = resolvePath(workspace, path)

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("configuration %q: envFile: %w", cfg.Name(), err)
	}
	defer f.Close()

	env := map[string]any{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		env[strings.TrimSpace(key)] = value
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("configuration %q: envFile: %w", cfg.Name(), err)
	}

	if existing, ok := cfg["env"].(map[string]any); ok {
		maps.Copy(env, existing)
	}
	cfg["env"] = env
	return nil
}

// resolvePath makes a path relative to the workspace absolute.
func resolvePath(workspace, path string) string {
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, rest)
		}
	}
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(workspace, path)
}

// resolveExecutable is resolvePath, except bare names such as "python3" stay as
// they are to be looked up on PATH.
func resolveExecutable(workspace, path string) string {
	if !strings.ContainsRune(path, filepath.Separator) {
		return path
	}
	return resolvePath(workspace, path)
}

func hostPort(host, port any) (string, error) {
	h, _ := host.(string)
	if h == "" {
		h = "127.0.0.1"
	}
	p, ok := number(port)
	if !ok || p < 1 || p > 65535 {
		return "", fmt.Errorf("port must be a number from 1 to 65535")
	}
	return net.JoinHostPort(h, strconv.Itoa(p)), nil
}

func number(v any) (int, bool) {
	switch x := v.(type) {
	case float64:
		return int(x), x == float64(int(x))
	case int:
		return x, true
	case string:
		n, err := strconv.Atoi(x)
		return n, err == nil
	}
	return 0, false
}
