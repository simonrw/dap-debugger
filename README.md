<img src="assets/icon.svg" width="64" height="64" alt="">

# dap-debugger

A standalone, general-purpose terminal debugger that works like the VS Code debugger, without the editor. It reads a VS Code `launch.json` and drives any debug adapter that speaks the [Debug Adapter Protocol](https://microsoft.github.io/debug-adapter-protocol/) (DAP). debugpy (Python) and Delve (Go) are set up out of the box and tested; other adapters are configured by type. It is built with [Bubble Tea v2](https://github.com/charmbracelet/bubbletea) and the Charm libraries.

- Browse the workspace and set breakpoints before starting, or after attaching but before triggering the code you want to debug.
- Line, conditional, hit-count, logpoint and function breakpoints, watch expressions, exception filters and the selected configuration are remembered per workspace.
- Debug keys follow VS Code, with F-key overrides from the author's VS Code `keybindings.json`.

## Install and run

Requires Go to build. Each language needs its debug adapter installed, for example debugpy for Python or [Delve](https://github.com/go-delve/delve) for Go.

```sh
go install github.com/simonrw/dap-debugger@latest
go install github.com/go-delve/delve/cmd/dlv@latest

dap-debugger path/to/.vscode/launch.json
```

With no argument, `.vscode/launch.json` in the current directory is used. Flags:

| Flag | Meaning |
| --- | --- |
| `--name NAME` | Select a configuration by name (otherwise the last used one) |
| `--start` | Start debugging immediately |
| `--check` | Validate the file, list its configurations and exit |
| `--workspace DIR` | Override `${workspaceFolder}` (default: the folder containing `.vscode`) |
| `--state FILE` | State file (default: `dap-debugger/state.json` in the user config directory) |
| `--adapters FILE` | Debug adapter definitions (default: `dap-debugger/adapters.json` in the user config directory) |
| `--request-timeout`, `--startup-timeout` | Adapter timeouts (15s, 60s) |
| `--trace FILE` | Append every DAP message to a file as JSON lines (also `$DAP_DEBUGGER_TRACE`) |

Try the bundled examples, a small Python script and Go program in `examples/`:

| File | Configuration |
| --- | --- |
| `examples/python-launch.json` | Launch `hello.py` under debugpy |
| `examples/python-attach.json` | Attach to `hello.py` started with `python -m debugpy --listen 127.0.0.1:5678 --wait-for-client examples/hello.py` |
| `examples/go-launch.json` | Build and launch `examples/go` under Delve |
| `examples/go-attach.json` | Attach to the Go program under a headless Delve on port 38697 (commands in the file) |
| `examples/.vscode/launch.json` | All of the above in one file, as VS Code keeps them, plus "current file" |

```sh
go run . examples/go-launch.json
```

## Interface

```
 EXPLORER   DEBUG            │▍ go/main.go  Ln 10/20
 ▶ Go: hello ▾  ^O           │    8 func greet(name string) string {
▍ VARIABLES                  │    9     message := fmt.Sprintf("Hello, %s!", name)
 ▾ Locals                    │ ●▶10     fmt.Println(message)
     name: "debugger 0"      │   11     return message
  WATCH                      │   12 }
  CALL STACK                 │
 ▾ [Go 1] main.greet  PAUSED │  DEBUG CONSOLE
    main.greet  main.go:10   │ Hello, debugger 0!
  BREAKPOINTS                │
 [x] ● main.go:10  go        │ › Evaluate expression in the selected frame
 ⏸ Paused on breakpoint        F9 continue · F8 step over · F7 step into · ⇧F11 step out
```

The sidebar switches between the **Explorer** and the **Debug** view (Variables, Watch, Call Stack, Breakpoints). The source view shows breakpoints in the gutter: red when set or verified, hollow when the adapter could not verify them, grey when disabled, ringed for conditional or hit-count breakpoints, and a diamond for logpoints. A yellow arrow marks the current line and a green one the selected caller frame. The Debug Console shows program output and evaluates expressions in the selected frame. **Evaluation can modify the program.**

### Debugging keys

| Key | Action |
| --- | --- |
| `F5` | Start debugging |
| `Ctrl+F5` | Run without debugging |
| `F9` | Continue |
| `F8` | Step over |
| `F7` | Step into |
| `Shift+F11` | Step out |
| `F6` | Pause |
| `Shift+F5` | Stop (terminates a launched program, detaches from an attached one) |
| `Ctrl+Shift+F5` | Restart |
| `Ctrl+O` | Select launch configuration |

Your terminal must pass function keys through. On macOS, enable "Use F1, F2, etc. keys as standard function keys" or hold `fn`.

### Other keys

| Key | Action |
| --- | --- |
| `F1` | Help |
| `Ctrl+P` | Go to file (fuzzy, honours `.gitignore`) |
| `Ctrl+E` / `Ctrl+R` | Explorer / Debug view |
| `Ctrl+B` | Toggle sidebar |
| `Ctrl+Y` | Source or disassembly view |
| `Ctrl+H/J/K/L` | Move focus left/down/up/right |
| `Tab` / `Shift+Tab` | Next / previous pane |
| `Ctrl+T` | In the bottom panel, switch between the Debug Console and the Terminal |
| `Ctrl+Q` | Stop the session and quit |

In the source view: `b` toggles a breakpoint (or click the gutter), `Shift+F9` or `i` toggles an inline breakpoint at the cursor's column (snapping to the positions the adapter reports, which are underlined on the cursor line when it supports `breakpointLocations`), `B` or `space` enables/disables it, `c` sets a condition, `H` a hit count, `p` turns it into a logpoint (message with `{expressions}`), `/` searches, `n`/`N` repeat the search, and `Ctrl+G` goes to a line. Arrow keys or `h`/`j`/`k`/`l`, `0`/`$`, `PgUp`/`PgDn`, `g`/`G` and `Ctrl+U`/`Ctrl+D` move around.

`Ctrl+Y` switches the source view to the disassembly of the selected frame, when the adapter supports it (Delve does). There, `b` toggles an instruction breakpoint and the step keys step one instruction at a time, as in VS Code's disassembly view.

In the Explorer: `enter` opens files and folders, `-` or backspace shows the parent folder (to browse outside the workspace), `.` shows the selected folder, `~` returns to the workspace and `r` refreshes. In Variables and Watch: `enter`/`space` expands, `e` edits a variable's value or a watch expression, `w` adds a variable to Watch, `v` breaks when a variable's value changes (adapters with data breakpoints, such as Delve), `m` opens a hex view of a value's memory (`e` there writes bytes, `esc` closes it), `a` adds a watch and `d` removes it. In Call Stack, `enter` selects a frame, expands a thread or, with several sessions, focuses a session. In Breakpoints, `space` toggles a breakpoint or exception filter, `enter` jumps to it, `c`, `H` and `p` edit its condition, hit count and log message, `f` adds a function breakpoint and `d` removes one.

## Launch configurations

Use the same `launch.json` as VS Code. Comments, trailing commas, platform overrides (`osx`, `linux`, `windows`) and these variables are supported: `${workspaceFolder}`, `${workspaceFolderBasename}`, `${file}` and its relatives (the file open in the source view), `${cwd}`, `${userHome}`, `${pathSeparator}` and `${env:NAME}`. `${input:...}` and `${command:...}` are not. `envFile` is read and merged into `env`.

`preLaunchTask` and `postDebugTask` run tasks from `tasks.json` next to `launch.json`, with output in the Debug Console. Shell and process tasks are supported, with `args`, `options.cwd`, `options.env` and `dependsOn`, as are the task types VS Code provides itself (`npm`, `typescript`, `gulp`, `grunt`, `jake`) and the `go` and `cargo` types of the Go and Rust extensions. A failing `preLaunchTask` stops the launch. A background task (`isBackground`, or TypeScript's watch option) starts, the launch waits until a line of its output matches its problem matcher's `background.endsPattern`, and it keeps running for later sessions until you quit. Named matchers `$tsc-watch`, `$esbuild-watch`, `$ts-webpack-watch` and `$tsl-webpack-watch` are known; others can be defined in `problemMatchers.json` beside `adapters.json`, as an object of matcher names to end patterns, for example `{"$vite": "ready in \\d+ ms"}`.

```jsonc
{
  "version": "0.2.0",
  "configurations": [
    {
      "name": "Python: current file",
      "type": "debugpy",
      "request": "launch",
      "program": "${file}",
      "justMyCode": true
    },
    {
      "name": "Go: package",
      "type": "go",
      "request": "launch",
      "mode": "debug",
      "program": "${workspaceFolder}/cmd/app"
    }
  ]
}
```

### Python (`debugpy` or `python`)

Launch runs the debugpy adapter. The program's interpreter is the configuration's `python`, else `.venv/bin/python` or `venv/bin/python` in the workspace. The adapter itself runs under the first interpreter that has debugpy installed (`debugAdapterPython`, the program's interpreter, `$VIRTUAL_ENV`, `python3`, `python`). If none has it and `uv` is installed, a throwaway uv environment with debugpy is used, much like the debugpy bundled with VS Code. With `console` set to `integratedTerminal` (debugpy's default) or `externalTerminal`, debugpy asks the debugger to start the program (`runInTerminal`), which runs it on a pseudo-terminal shown in the Terminal tab. With `internalConsole` the program has no stdin, as in VS Code.

Attach connects to a running debugpy server with `connect.host` and `connect.port` (or legacy `host`/`port`):

```sh
python -m debugpy --listen 127.0.0.1:5678 app.py
```

### Go (`go`)

Launch starts `dlv dap` (from `dlvToolPath`, `PATH` or `~/go/bin`) with `mode` set to `debug`, `test`, `exec` or `auto`. Extra `dlvFlags` are passed to Delve. With `console` set to `integratedTerminal` or `externalTerminal`, the program runs in the Terminal tab. Attach with `"mode": "remote"` connects to a headless Delve server at `host`/`port`; `"mode": "local"` with a numeric `processId` attaches Delve to a local process.

```sh
dlv exec ./app --headless --listen=127.0.0.1:38697 --accept-multiclient --continue
```

Build with `-gcflags='all=-N -l'` for accurate variables. Use `--accept-multiclient` so the server and program survive a detach.

## Debug adapters

Each configuration's `type` picks the debug adapter, as in VS Code, where extensions register adapters. The first of these that applies is used:

1. `debugServer` in the configuration: connect to an adapter already listening on that localhost port (VS Code's field, handy when developing an adapter).
2. `debugAdapter` in the configuration: an adapter definition for this configuration only.
3. The user's `adapters.json`: adapter definitions keyed by type.
4. Built-in adapters: `debugpy`/`python` and `go` with the setup described above, plus `lldb-dap`, `lldb-vscode`, `gdb` (GDB 14+, `gdb --interpreter=dap`) and `coreclr` (netcoredbg).

An adapter definition has a `command` and optional `env` and `cwd`, or an `address` of a running adapter. The command's arguments choose the transport:

- `{port}` in an argument: the adapter listens on a free port that is filled in, and the debugger connects to it.
- `{address}` in an argument: the debugger listens and the adapter connects to that `host:port`.
- Neither: DAP over the adapter's stdin and stdout.

```jsonc
// ~/Library/Application Support/dap-debugger/adapters.json on macOS,
// ~/.config/dap-debugger/adapters.json on Linux
{
  "lldb": { "command": ["codelldb", "--port", "{port}"] },
  "rdbg": { "command": ["rdbg", "--open", "--port", "{port}"] },
  "php": { "command": ["node", "/opt/vscode-php-debug/out/phpDebug.js"] },
  "mock": { "address": "127.0.0.1:4711" }
}
```

The rest of the configuration is passed to the adapter's `launch` or `attach` request, as VS Code does, so use the fields the adapter documents. A user definition for `debugpy`, `python` or `go` replaces the built-in setup. `dap-debugger --check` lists the types with a known adapter.

## Remote debugging

DAP has no authentication or encryption and allows running code. Bind debug servers to loopback and use an SSH tunnel:

```sh
ssh -N -L 5678:127.0.0.1:5678 user@remote-host
```

Map local sources to the remote paths with `pathMappings` (debugpy) or `substitutePath` (Delve) in the configuration, as in VS Code. Breakpoints are set on local paths and the adapter translates them.

Typical attach workflow: start the target under its debug server, press `F5` to attach while it runs, open files with `Ctrl+P` or the Explorer, set breakpoints, then trigger the request or operation you want to debug.

## Terminal

Programs started with `console` set to `integratedTerminal` or `externalTerminal` run on a pseudo-terminal, like VS Code's integrated terminal. A Terminal tab appears beside the Debug Console and shows the program's screen through a terminal emulator, so prompts, password prompts and full-screen programs work. While the bottom panel has focus on the Terminal, keys go to the program, except the debug keys and `Ctrl+T`, which switches back to the Debug Console. With `internalConsole` the program has no input, as in VS Code.

## Multiple sessions

A compound in `launch.json` (`"compounds": [{"name": ..., "configurations": [...], "stopAll": true}]`) appears in the configuration picker and starts each configuration in turn, running each one's `preLaunchTask`. Adapters can also start child sessions (DAP `startDebugging`), for example debugpy with `"subProcess": true`. With more than one session, Call Stack lists each session with its threads. The session that last stopped, or the one selected in Call Stack, is the one the debug keys, Variables and Watch act on. Breakpoints are sent to every session. Stopping a session also stops its children, and with `stopAll` every session of its compound.

## Compared with VS Code

| VS Code feature | Here |
| --- | --- |
| Run and Debug view: Variables, Watch, Call Stack, Breakpoints | Same four sections in the Debug view |
| Configuration dropdown, F5 to start | `Ctrl+O` picker, remembered per workspace; F5 |
| Debug toolbar (continue, step, restart, stop) | Same actions on your F-keys; the status bar lists them |
| Line, conditional, hit-count, logpoint and function breakpoints | Supported, persisted, and editable before a session starts |
| Exception breakpoint filters from the adapter | Listed in Breakpoints after the first session, choices remembered |
| Breakpoint verification and relocation | Hollow when unverified; shown at the adapter's line when moved |
| Current line and selected caller frame highlights | Yellow and green arrows and line backgrounds |
| Set Value and Add to Watch on variables | `e` and `w` on a variable; `a` in Watch |
| Debug Console REPL | Evaluates in the selected frame, with history |
| Quick Open (`Ctrl+P`) and Explorer | Same, and the Explorer can leave the workspace |
| Debug adapters contributed by extensions | Any DAP adapter, defined by type in `adapters.json` or per configuration |
| `launch.json` variables, `envFile`, platform overrides | Supported, except `${input:…}` and `${command:…}` |
| Integrated terminal (`console: integratedTerminal`, `runInTerminal`) | A Terminal tab beside the Debug Console emulates the program's terminal, so full-screen programs work; keys go to the program while it has focus |
| `preLaunchTask` and `postDebugTask` | Shell, process, `npm`, `typescript`, `gulp`, `grunt`, `jake`, `go` and `cargo` tasks from `tasks.json`, with `dependsOn` and background tasks |
| Run Without Debugging | `Ctrl+F5` launches with `noDebug` |
| Breakpoints in sources from the adapter (no local file) | Set in the source view for the current session |
| Compounds, including `stopAll` and their `preLaunchTask` | Listed in the configuration picker; sessions start in order |
| Child sessions (`startDebugging`), such as debugpy `subProcess` | Started automatically, shown under their own session in Call Stack and stopped with their parent |
| Break on Value Change (data breakpoints) | `v` on a variable, for the current session, when the adapter supports it (Delve does, debugpy does not) |
| Inline (column) breakpoints and breakpoint position hints | `Shift+F9` or `i` at the cursor's column; valid positions underlined when the adapter reports them |
| View Binary Data (memory) | `m` on a variable or watch: a hex view with writing, when the adapter supports it (Delve does) |
| Disassembly view, instruction breakpoints and stepping | `Ctrl+Y`, when the adapter supports it |

### Checked against VS Code

`tools/vscode-probe` drives VS Code itself through scripted sessions with Delve and debugpy and records every DAP message, using a throwaway profile. Twelve scenarios, recorded from VS Code 1.145, are replayed against dap-debugger with the same keys by `TestRequestsMatchVSCode`, which checks each session sends the same requests, with the same breakpoint, exception, stop and restart arguments, between each pair of events:

| Scenario | Covers |
| --- | --- |
| `basic-go`, `basic-python` | Launch, breakpoint, step over, continue, stop |
| `breakpoints-go` | Conditional, hit-count, logpoint and function breakpoints; step into and out |
| `pause-go` | Stop on entry, continue, pause |
| `restart-go`, `restart-python` | Restart with a `restart` request (Delve), or `terminate` with `restart: true` (debugpy) |
| `attach-go`, `attach-python` | Attach to running Delve and debugpy servers, set breakpoints, disconnect |
| `terminal-python` | `console: integratedTerminal` through `runInTerminal` |
| `exception-python` | Stop on an uncaught exception, with `exceptionInfo` |
| `compound` | Go and Python sessions started together |
| `subprocess-python` | A debugpy child session for a subprocess (`startDebugging`), stopping at a breakpoint in the child |

Re-record with `go test ./internal/ui -run RequestsMatchVSCode -update-vscode`, which opens VS Code. The debug keys are checked against your VS Code `keybindings.json` by `TestKeysFollowVSCodeKeybindings`.

## Limitations

- Task types and problem matchers come from VS Code extensions; the common ones are built in and other matchers can be defined in `problemMatchers.json`, but other extension task types are not supported.

## Development

```sh
mise install
mise exec -- make test
```

Tests drive a built-in fake adapter (`internal/daptest`) over every transport, so the client is tested against the protocol alone. Integration tests also launch and attach to real debugpy and Delve targets. debugpy comes from the `.venv` that `make test` creates with `uv sync`, and Delve tests skip when `dlv` is not installed. `daptest.UseAdapter` sets both up; Python tests fail if the `.venv` is missing or if `examples/.venv` or `examples/venv` exists, as either would replace it. See [QA notes](docs/qa.md) and [adapter research](docs/research.md).

| Package | Responsibility |
| --- | --- |
| `internal/dap` | Message framing and a client that matches responses to requests |
| `internal/fzf` | fzf's fuzzy matching and path-scheme ranking, used by the file picker |
| `internal/launch` | `launch.json` and `tasks.json` parsing, variables, adapter definitions and plans |
| `internal/daptest` | A fake debug adapter for tests, and setup of real adapters for integration tests |
| `internal/session` | Adapter processes, the initialization handshake, breakpoints, shutdown |
| `internal/state` | Persisted breakpoints, watches and settings |
| `internal/ui` | The Bubble Tea interface |

Created in [T3 Code](https://t3.codes).
