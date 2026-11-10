// Command dap-debugger is a standalone terminal debugger driven by VS Code
// launch.json configurations.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/simonrw/dap-debugger/internal/launch"
	"github.com/simonrw/dap-debugger/internal/session"
	"github.com/simonrw/dap-debugger/internal/state"
	"github.com/simonrw/dap-debugger/internal/ui"
)

func main() {
	os.Exit(run())
}

func run() int {
	flags := flag.NewFlagSet("dap-debugger", flag.ContinueOnError)
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage: dap-debugger [flags] [launch.json]\n\n")
		fmt.Fprintf(flags.Output(), "Debug programs using VS Code launch configurations. Without a path,\n.vscode/launch.json in the current directory is used.\n\nFlags:\n")
		flags.PrintDefaults()
	}
	name := flags.String("name", "", "launch configuration to select")
	start := flags.Bool("start", false, "start debugging immediately")
	check := flags.Bool("check", false, "validate the launch configurations and exit")
	statePath := flags.String("state", "", "state file for breakpoints and watches (default: user config directory)")
	adaptersPath := flags.String("adapters", "", "debug adapter definitions by configuration type (default: adapters.json in the user config directory)")
	workspace := flags.String("workspace", "", "workspace folder for ${workspaceFolder} (default: derived from the launch.json location)")
	requestTimeout := flags.Duration("request-timeout", 15*time.Second, "timeout for debug adapter requests")
	startupTimeout := flags.Duration("startup-timeout", 60*time.Second, "timeout for starting a debug session")
	trace := flags.String("trace", os.Getenv("DAP_DEBUGGER_TRACE"), "append every DAP message to this file as JSON lines (also $DAP_DEBUGGER_TRACE)")

	if err := flags.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 1 {
		flags.Usage()
		return 2
	}

	path := flags.Arg(0)
	if path == "" {
		path = filepath.Join(".vscode", "launch.json")
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintln(os.Stderr, "dap-debugger: no launch.json given and .vscode/launch.json not found")
			return 2
		}
	}

	file, err := launch.Load(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dap-debugger: %v\n", err)
		return 2
	}
	if *workspace != "" {
		abs, err := filepath.Abs(*workspace)
		if err != nil {
			fmt.Fprintf(os.Stderr, "dap-debugger: %v\n", err)
			return 2
		}
		file.Workspace = abs
	}
	if *name != "" {
		if _, err := file.Find(*name); err != nil {
			fmt.Fprintf(os.Stderr, "dap-debugger: %v\n", err)
			return 2
		}
	}

	if *adaptersPath == "" {
		if *adaptersPath, err = launch.DefaultAdaptersPath(); err != nil {
			fmt.Fprintf(os.Stderr, "dap-debugger: %v\n", err)
			return 2
		}
	}
	adapters, err := launch.LoadAdapters(*adaptersPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dap-debugger: %v\n", err)
		return 2
	}
	matchers, err := launch.LoadProblemMatchers(filepath.Join(filepath.Dir(*adaptersPath), "problemMatchers.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "dap-debugger: %v\n", err)
		return 2
	}

	if *check {
		fmt.Printf("Workspace: %s\n", file.Workspace)
		for _, c := range file.Configurations {
			fmt.Printf("  %s (%s %s)\n", c.Name(), c.String("type"), c.String("request"))
		}
		fmt.Printf("Adapters for types: %s\n", strings.Join(launch.AdapterTypes(adapters), ", "))
		return 0
	}

	if *statePath == "" {
		*statePath, err = state.DefaultPath()
		if err != nil {
			fmt.Fprintf(os.Stderr, "dap-debugger: %v\n", err)
			return 2
		}
	}
	store, err := state.Open(*statePath, file.Workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dap-debugger: read state %s: %v\n", *statePath, err)
		return 2
	}

	model := ui.New(ui.Options{
		File:          file,
		Store:         store,
		Configuration: *name,
		AutoStart:     *start,
		Adapters:      adapters,
		Matchers:      matchers,
		Session:       session.Options{RequestTimeout: *requestTimeout, StartupTimeout: *startupTimeout, Trace: *trace},
	})
	_, err = tea.NewProgram(model).Run()
	model.Shutdown()
	if err != nil {
		fmt.Fprintf(os.Stderr, "dap-debugger: %v\n", err)
		return 1
	}
	return 0
}
