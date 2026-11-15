# QA

## Reproduce

```sh
mise install
mise exec -- make test
```

Delve tests use the `dlv` that a go configuration without `dlvToolPath` would (see the README), and append its directory to `PATH` for the commands they run, such as attach scenario servers; mise installs the pinned Delve version. They skip with a reason when there is no such `dlv`. A skip is not evidence that a scenario works. debugpy comes from the repository's `.venv`, which `make test` creates with `uv sync` from `uv.lock`; Python tests fail unless launch would run the adapter and the program with it.

## Coverage

- `internal/dap`: framing, malformed frames, request/response correlation, adapter error formatting, rejection of reverse requests, timeouts and failure of pending requests when the connection closes.
- `internal/launch`: adapter definitions and their precedence, `tasks.json` resolution (including `npm`, `typescript` and `go` tasks and background ready patterns) (dependencies, shell quoting, options, unsupported tasks, cycles), JSONC, workspace detection, variable substitution, platform overrides, `envFile`, and adapter plans for debugpy launch/attach, Go launch/remote attach/local attach and `debugServer`.
- `internal/state`: persistence, ordering and independence of workspaces in one state file.
- `internal/session` with the fake adapter (`internal/daptest`): a session against an adapter known only by its definition, over stdio, an adapter that listens (`{port}`), an adapter that connects back (`{address}`), a per-configuration `debugAdapter` and `debugServer`.
- `internal/session` (real adapters):
  - Python and Go launch: a verified breakpoint stops the program; the stack, scopes, locals and evaluation are checked; step over, breakpoint removal and continue to termination, with program output and no adapter errors.
  - Stop while paused, for both adapters, without adapter tracebacks.
  - `console: integratedTerminal` for both adapters: the program runs on a pseudo-terminal, reads a line written through the session, and (debugpy) reads a password without echoing it.
  - Python attach to `debugpy --listen` and Go attach to `dlv exec --headless --accept-multiclient --continue`: attach while the program runs, set a breakpoint while running, stop there, detach, then reattach to the same still-running server.
- `internal/ui` with the fake adapter: a whole session driven with keys (breakpoint, variables, set value, step, continue); a compound of two sessions where one starts a child session with `startDebugging`, then `stopAll`; inline breakpoints sent with their column and remembered; the memory view reading and writing memory; inline breakpoints snapping to `breakpointLocations` positions; a breakpoint in a source provided by reference; a background `preLaunchTask` that the launch waits for, reused by the next session and stopped on quit.
- `internal/ui` against the user's VS Code `keybindings.json` (skipped without one): every debug function key matches VS Code's effective binding.
- `internal/ui` (real adapters, driven with key presses):
  - Explorer navigation, opening files, toggling and conditional breakpoints, the breakpoints pane, persistence across restarts and reopening the last file.
  - Quick open and watch expressions.
  - Rendering at small and large sizes.
  - A Go session with F5, F8, Shift+F11, F9 and Shift+F5, and a Python session with `${file}`, F7, console evaluation and continue to exit.
  - `preLaunchTask`/`postDebugTask`, `preLaunchTask`/`postDebugTask`, including a failing task that stops the launch.
  - With debugpy: a `subProcess` child session that stops at a breakpoint in the child process.
  - With debugpy: a full-screen program drawn in the Terminal tab that reads a raw arrow key, and a prompt and password prompt answered from the Terminal.
  - With Delve: the memory view of a string.
  - With Delve: the disassembly view, a single-instruction step and an instruction breakpoint.
  - With Delve: a function breakpoint stops in the function, a logpoint with a hit count logs once on the third pass, and a data breakpoint on the loop counter stops when it changes.

## Comparison with VS Code

`tools/vscode-probe` drives VS Code 1.145 through each scenario in `internal/ui/testdata/vscode` with Delve or debugpy, logging every DAP message with VS Code's debug adapter tracker, and `TestRequestsMatchVSCode` replays the scenarios against dap-debugger. Comparing the two found these differences, all now matched:

- `initialize` reports the same capabilities as VS Code: variable paging, progress reporting, memory events, `argsCanBeInterpretedByShell` and ANSI styling. Each is handled: progress shows in the status bar, memory events refresh the memory view, and shell-interpreted `runInTerminal` arguments run through the shell.
- `setBreakpoints` sends the source name and the deprecated `lines` list.
- Configuration sends empty function, instruction and data breakpoint lists when supported, which resets a reused debug server.
- With `supportsDelayedStackTraceLoading`, the stack loads the top frame and then the rest of the first 20, with "Load more stack frames" for deeper stacks.
- Stopping sends `terminate` before `disconnect` when the adapter supports it. `disconnect` sends `restart: false`, and `terminateDebuggee: false` when disconnecting from an attached program, only to adapters with `supportTerminateDebuggee`.
- Restart uses the `restart` request when the adapter supports it (Delve), repeating the configuration when the adapter sends `initialized` again; otherwise it sends `terminate` with `restart: true` and starts a new session (debugpy).

Sessions are compared one by one, matched by name with digits ignored (a subprocess session is named after its process ID). Allowed differences: request order within a segment (both clients send some concurrently), extra `scopes` and `variables` requests on stops where VS Code's Variables view was not on screen, and the initial `threads` request, which a program that stops at once can overtake.

## Findings during manual QA

- Probing a `debugpy --listen` port by connecting and immediately closing uses up its listener, so a later attach hangs. Tests wait for program output instead.
- Half-closing a TCP connection to debugpy also stops it from accepting new clients, so the session only half-closes connections to adapters it started. Half-closing an owned adapter's stdin lets it exit cleanly instead of printing a broken-pipe traceback.
