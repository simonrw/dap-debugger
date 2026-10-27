# Implementation plan

## Scope and decisions

Build one Python 3.11+ application with Textual and an asyncio DAP client. Support Python through debugpy and Go through Delve. The first release supports local launch and TCP attach to a debugger already controlling a running target. A debugger endpoint is not an arbitrary application port. Remote deployment, SSH setup, PID injection, interactive target stdin, and arbitrary DAP adapters are outside the first release.

The repository initially contains only `README.md` and `assets/icon.svg`. Inspection found Python 3.14.7, Go 1.27.1, uv, and mise. Textual, debugpy, pytest, and `dlv` were not available in the inspected Python environment or executable search path. These are environment observations, not supported version requirements. Install project dependencies in a virtual environment and report unavailable real adapter tests explicitly.

The parent integration owner owns packaging, CLI assembly, documentation outside this file, and final verification. This document fixes the shared interface so core and UI work can proceed independently. Any necessary interface change must be coordinated with the parent rather than independently implemented by both owners.

## Modules and ownership

| Module | Owner | Responsibility |
| --- | --- | --- |
| `src/dap_debugger/config.py` | Core | Parse and validate configuration, resolve local paths, produce adapter arguments |
| `src/dap_debugger/protocol.py` | Core | Framing, correlated requests, event delivery, transport failure, reverse request rejection |
| `src/dap_debugger/session.py` | Core | Adapter processes, initialization, configuration handshake, state, breakpoints, cleanup |
| `src/dap_debugger/ui.py` | UI | Textual app, presentation, selected thread/frame, interaction and background refresh |
| `src/dap_debugger/__main__.py` | Integration | CLI, configuration selection, launch app |
| `tests/test_protocol.py`, `tests/test_session.py` | Core | Fake adapter acceptance tests |
| `tests/test_ui.py` | UI | Textual headless interaction tests using a fake session |
| `tests/integration/` | Integration | Real debugpy and Delve tests |

Keep adapter-specific preparation in `session.py` until its size warrants another module. Do not introduce a plugin registry, dependency injection framework, persistence layer, or separate domain model for each DAP response. The external seam is `DebugSession`. Transport details remain internal. Neither core module imports Textual.

## Configuration contract

`load_config(path: str | Path) -> SessionConfig` reads one UTF-8 JSON object. `SessionConfig.from_dict(data: dict, *, base_dir: Path) -> SessionConfig` provides the same validation for tests and direct construction. Unknown top-level keys fail with a readable error. `arguments` deliberately accepts adapter-specific keys.

```json
{
  "name": "Python example",
  "adapter": "python",
  "request": "launch",
  "python": "python3",
  "arguments": {
    "program": "./examples/hello.py",
    "cwd": ".",
    "args": [],
    "stopOnEntry": false,
    "justMyCode": true
  },
  "breakpoints": {
    "./examples/hello.py": [8]
  },
  "pathMappings": [],
  "requestTimeout": 15,
  "startupTimeout": 60
}
```

| Key | Type and default | Meaning |
| --- | --- | --- |
| `name` | String, `Debug session` | Display label |
| `adapter` | Required `python` or `go` | Adapter selection |
| `request` | Required `launch` or `attach` | Session mode |
| `python` | String, current `sys.executable` | Python executable for a locally owned debugpy adapter |
| `dlv` | String, `dlv` | Delve executable for a locally owned Go adapter |
| `connect` | Object with string `host` and integer `port` | Required for attach, forbidden for launch in this release |
| `arguments` | Object, `{}` | DAP launch or attach arguments |
| `breakpoints` | Object mapping local source paths to positive integer lists, `{}` | Initial line breakpoints |
| `pathMappings` | List of `{ "localRoot": string, "remoteRoot": string }`, `[]` | Local source root to target source root mapping |
| `requestTimeout` | Positive seconds, `15` | Ordinary request deadline |
| `startupTimeout` | Positive seconds, `60` | Overall launch or attach deadline |

The typed `SessionConfig` exposes these names as snake_case attributes: `name`, `adapter`, `request`, `python`, `dlv`, `connect`, `arguments`, `breakpoints`, `path_mappings`, `request_timeout`, and `startup_timeout`. `connect` is `dict[str, object] | None`; `arguments` is `dict[str, Any]`; `breakpoints` is `dict[str, list[int]]`; `path_mappings` is `list[dict[str, str]]`. Copy mutable input structures during construction.

Resolve local `cwd`, launch `program`, initial breakpoint paths, and mapping `localRoot` relative to the configuration file directory, not the invoking shell. Do not resolve or test existence of remote paths on the client. Executable names without directory separators use `PATH`. Explicit executable paths are resolved relative to the configuration directory. Ports must be 1 through 65535. Reject booleans where numbers are expected. Do not interpolate environment variables or execute configuration strings.

Python launch requires exactly one of `arguments.program` and `arguments.module`. Core supplies `console: "internalConsole"` and `redirectOutput: true`, and rejects external or integrated terminal console modes. `arguments.args` is a string list. Go launch defaults `arguments.mode` to `debug` and requires `program`; support `debug`, `exec`, and `test`. Preserve explicit `cwd`, `env`, and adapter-specific supported arguments.

Python attach example:

```json
{
  "adapter": "python",
  "request": "attach",
  "connect": { "host": "127.0.0.1", "port": 5678 },
  "arguments": { "justMyCode": true },
  "pathMappings": [
    { "localRoot": "./remote-src", "remoteRoot": "/srv/app" }
  ]
}
```

Go attach example:

```json
{
  "adapter": "go",
  "request": "attach",
  "connect": { "host": "127.0.0.1", "port": 38697 },
  "arguments": { "mode": "remote" },
  "pathMappings": [
    { "localRoot": "./remote-src", "remoteRoot": "/srv/app" }
  ]
}
```

For Python attach, the remote operator starts `python -m debugpy --listen 127.0.0.1:5678 application.py`, with `--wait-for-client` when desired. For Go attach to an already running debug target, the operator starts a headless Delve target, for example `dlv exec ./application --headless --listen=127.0.0.1:38697 --accept-multiclient --continue`. A process that was started independently instead needs operator-managed `dlv attach PID --headless --listen=127.0.0.1:38697 --accept-multiclient --continue`, subject to OS permissions. Remote attach uses `mode: "remote"`; a bare `dlv dap` server does not support that mode. The application must not silently replace remote attach with local process attachment.

Core translates top-level mappings into debugpy `pathMappings` or Delve `substitutePath` entries with `from` set to the local root and `to` set to the remote root. Adapters perform wire-path translation. Do not translate those paths a second time. Reject simultaneous top-level mappings and adapter-specific mapping arguments. Missing source remains a visible UI condition, not a session failure.

## Public async interface

Import `SessionConfig` from `dap_debugger.config`, and `DebugSession` and `DAPError` from `dap_debugger.session`. The interface uses standard DAP dictionaries so the UI can consume the specification directly without parallel model definitions.

```python
class DebugSession:
    def __init__(self, config: SessionConfig): ...

    config: SessionConfig
    state: str
    capabilities: dict[str, Any]

    async def start(self) -> None: ...
    async def request(
        self, command: str, arguments: dict[str, Any] | None = None
    ) -> dict[str, Any]: ...
    async def next_event(self) -> dict[str, Any]: ...
    async def set_breakpoints(self, path: str, lines: list[int]) -> list[dict[str, Any]]: ...
    async def close(self, *, terminate: bool | None = None) -> None: ...


class DAPError(Exception):
    command: str
    message: str
```

`state` is one of `new`, `starting`, `running`, `stopped`, `terminated`, `failed`, or `closed`. `capabilities` is empty until initialization completes. One session instance represents one connection and cannot restart. The UI constructs a fresh session to reconnect.

`start()` opens the transport, initializes the adapter, starts launch or attach, installs initial breakpoints, sends `configurationDone` when supported, and awaits the launch or attach response. It must not wait for that response before processing `initialized` and sending configuration. An early `stopped` event remains authoritative; completing startup must not overwrite it with `running`. Startup failure cleans up owned resources before raising `DAPError`.

`request()` returns the response `body`, or `{}` when absent. Unsuccessful adapter responses, timeouts, invalid lifecycle use, and transport failures raise `DAPError` with a readable message. Cancellation remains `asyncio.CancelledError`. Concurrent requests are supported and correlated by `request_seq`, never response arrival order. Session-owned lifecycle commands `initialize`, `launch`, `attach`, `configurationDone`, and `disconnect` are not sent by the UI.

`next_event()` is a single-consumer stream. Ordinary events retain the DAP shape `{ "type": "event", "event": string, "body": object }`; absent bodies are normalized to `{}`. Core additionally emits `sessionState` with `{ "state": string }` and `sessionError` with `{ "message": string }`. State is updated before its event is delivered. The final event is `sessionState` with `closed`. The UI stops consuming after that event. Events received during startup remain available even if the consumer starts later. Core processes lifecycle events internally without consuming or hiding them from the UI.

`set_breakpoints()` replaces all line breakpoints for one local source and returns adapter breakpoint records, including verification and relocated lines. An empty list clears that source. Paths passed by the UI are absolute local paths; the adapter applies configured mappings. Before startup, this updates desired breakpoints and returns pending records with `verified: false`; once configured, it sends `setBreakpoints`. Serialize updates per source so rapid toggles cannot restore stale breakpoint sets. Configuration changes during startup must not be lost.

`close()` is idempotent, bounded, and safe after partial startup. `terminate=None` means true for an owned local launch and false for attach. Send `disconnect` with `terminateDebuggee` set accordingly, then close the transport and reap only locally owned adapter processes. Attached targets and remote adapters are never killed by OS signals. Explicit attach termination requires user confirmation and adapter support. Cleanup failure is reported but must not prevent the TUI from exiting.

### DAP requests used by the UI

| UI operation | Command and arguments | Result used |
| --- | --- | --- |
| List threads | `threads`, `{}` | `threads` |
| Stack | `stackTrace`, `{threadId, startFrame: 0, levels: 100}` | `stackFrames` |
| Frame scopes | `scopes`, `{frameId}` | `scopes` |
| Expand variable | `variables`, `{variablesReference}` | `variables` |
| Evaluate | `evaluate`, `{expression, frameId, context: "repl"}` | `result`, `variablesReference` |
| Continue | `continue`, `{threadId}` | `allThreadsContinued` |
| Pause | `pause`, `{threadId}` | Empty body |
| Step over | `next`, `{threadId}` | Empty body |
| Step into | `stepIn`, `{threadId}` | Empty body |
| Step out | `stepOut`, `{threadId}` | Empty body |
| Adapter source | `source`, `{source, sourceReference}` | `content`, optional `mimeType` |

Use actual adapter thread IDs, including IDs from `stopped`, never an assumed value of 1. Frame IDs and variable references expire on resume. Discard delayed refresh results using a UI stop-generation counter. On `stopped`, refresh threads, choose the event thread when present, fetch its stack, select the first frame, and fetch its scopes. Expand variables lazily. Thread and frame selection refresh their dependent panels. `continued` clears stale execution highlights and disables stopped-only actions. `terminated` disables execution commands without abruptly discarding output.

## Transport and lifecycle implementation

Frame JSON messages as UTF-8 bytes with ASCII `Content-Length: N\r\n\r\n`, where N counts bytes. Read exactly N bytes and tolerate fragmented headers, fragmented bodies, and multiple frames in one read. Use one reader task and a write lock. Validate message type, required correlation fields, and body size. Limit header size to 8 KiB and message size to 16 MiB. Reject malformed frames, unexpected EOF, and oversized bodies with one visible session error, and resolve all pending requests exceptionally. Unknown late responses after timeout or cancellation are ignored.

Do not let output flooding block the reader or pending responses. Bound retained output, drop or coalesce old output events first, and never silently drop lifecycle events. The UI also bounds its output history. Drain adapter stderr separately from the DAP stream and expose it as diagnostic output. Unsupported reverse requests receive an unsuccessful response. Advertise `supportsRunInTerminalRequest: false`; never run an adapter-supplied shell command.

Python launch starts `[config.python, "-m", "debugpy.adapter"]` using stdio. Go launch starts `[config.dlv, "dap", "--listen=127.0.0.1:PORT"]` and connects over loopback TCP. Select a free port, retry a bounded number of times if binding races, and use a bounded readiness loop that observes early child exit. Attach opens only the configured TCP connection and does not spawn an adapter. All subprocess invocations use argv arrays and `create_subprocess_exec`, never a shell.

Initialization declares client identity, path format `path`, one-based lines and columns, and only implemented client capabilities. Register the initialized-event waiter before launch or attach. Keep the reader running throughout startup, and enforce the overall startup deadline independently of ordinary request deadlines. Send empty exception filters only when the adapter advertises exception breakpoint configuration, rather than assuming identical Python and Go defaults.

## UI and keyboard contract

`DebuggerApp(config: SessionConfig, *, session: DebugSession | None = None)` is exported from `dap_debugger.ui`. If a session is supplied, use it without constructing another. The fake UI session implements the same interface. The application starts the session in a background worker after mounting, consumes events in another worker, and closes it on exit. No network request or file loading blocks Textual's input loop.

The main screen shows a session/state header, source with line numbers and breakpoint/current-line markers, thread and stack selectors, a scope/variable tree, output, an evaluation input, and a binding footer. At narrow widths use tabs or stacked panes rather than hiding controls. Show a useful empty state before startup, a connecting state, and actionable missing-dependency or connection errors. Source is read-only. An unavailable file displays its path and permits adapter `source` fallback when `sourceReference` is positive.

| Key | Behavior |
| --- | --- |
| `F5` | Continue when stopped |
| `F6` | Pause when running |
| `F9` | Toggle a breakpoint on the selected local source line |
| `F10` | Step over |
| `F11` | Step into |
| `Shift+F11` | Step out |
| `Ctrl+E` | Focus expression input |
| `Enter` in expression input | Evaluate explicitly in the selected frame |
| `Tab`, `Shift+Tab` | Move focus |
| Arrow keys and `Enter` | Navigate and select list/tree entries |
| `Ctrl+D` | Disconnect or stop the current session using ownership defaults |
| `Ctrl+Q` | Close session and quit |
| `F1` | Show help, including function-key alternatives through visible buttons |

All execution actions also have visible clickable buttons. Disable invalid actions based on state and selected thread. Do not override ordinary text editing keys with single-letter global shortcuts. Display failed requests without crashing the app. Pending, verified, rejected, and relocated breakpoints have distinct presentation. Keep the debuggee output separate from expression results and client diagnostics. Render adapter strings as literal text, not Textual markup or terminal control sequences.

## Acceptance matrix

| Level | Scenario | Required evidence |
| --- | --- | --- |
| Fake DAP | Fragmented and combined frames, non-ASCII output | Correct byte framing and complete ordered event delivery |
| Fake DAP | Two requests answered in reverse order | Each caller receives its own body |
| Fake DAP | Failed response, deadline, caller cancellation, EOF | Typed error or cancellation, no stranded pending futures |
| Fake DAP | Launch response delayed until `configurationDone` | Startup completes without deadlock |
| Fake DAP | Stop arrives before launch response | Final state stays stopped and UI receives the event |
| Fake DAP | Unsupported reverse request | Failure response, no executed command |
| Fake DAP | Breakpoint replacement, removal, rapid toggles | Full replacement semantics and correct final desired set |
| Fake DAP | Attach and launch cleanup, partial startup failure | Correct terminate flag, bounded cleanup, no killed external process |
| Fake DAP | Output flood, malformed or oversized frame | Bounded retained output, explicit error for invalid framing |
| UI fake session | Mount and startup success/failure | App remains responsive and state/errors are visible |
| UI fake session | Stop, select frame, expand variable, evaluate | Correct DAP IDs and requests; values render literally |
| UI fake session | Resume while a stack request is delayed | No stale frame or variable restoration |
| UI fake session | Keys, buttons, breakpoint feedback, resize, quit | Equivalent actions, usable small layout, exactly-once logical cleanup |
| Real debugpy | Launch fixture with breakpoint | Stop, stack, local variable, evaluate, step, continue, exit |
| Real debugpy | Attach to independently started listening fixture | Same inspection flow, disconnect leaves target alive |
| Real Delve | Launch Go fixture in debug or exec mode | Stop, stack, local variable, evaluate, step, continue, exit |
| Real Delve | Attach to headless multi-client Delve fixture | Remote-mode handshake, inspect, disconnect leaves target alive |
| Real adapters | Target paths differ from local paths | Mapped source and breakpoint work; no double translation |
| CLI | `--help`, malformed config, missing adapter, unreachable endpoint | Useful usage or bounded actionable failure |

Use subprocess fixtures with ephemeral loopback ports, explicit readiness signals, bounded waits, and `finally` cleanup. Never use fixed sleeps as the only readiness check. Integration tests may skip for missing executables or OS debugging restrictions with explicit reasons, but a skipped test is not proof of support. Record actual adapter and runtime versions in final verification. Loopback attach proves TCP attach behavior, not firewall or SSH deployment. The parent should distinguish those claims.

## Safety decisions

DAP permits code execution and normally provides neither authentication nor transport encryption. Local listeners bind only loopback. Document SSH forwarding for remote endpoints and warn when attaching directly to a non-loopback address. The client must not provision remote machines or request credentials.

Evaluation is performed only after explicit submission and is labelled as potentially modifying the target. No automatic watches evaluate arbitrary expressions. Configuration is trusted input capable of launching programs; opening a file in the source pane never launches it. Do not write source files, persist evaluated values, or print full environment dictionaries. Never signal a process solely because its PID came from a remote adapter. Bound shutdown and allow the UI to exit even when the peer is unresponsive.

Python subprocess debugging defaults off unless explicitly enabled and implemented; do not advertise multi-session child debugging. No restart, reverse execution, memory editing, register editing, conditional breakpoints, or terminal emulation is required for initial acceptance.

## Implementation sequence

1. Core owner implements configuration, framing, fake adapter tests, and the frozen session interface.
2. UI owner implements against a fake session immediately, using the exact event and response shapes above.
3. Integration owner adds packaging, `dap-debugger CONFIG.json` and `python -m dap_debugger CONFIG.json`, runnable examples, dependency instructions, and real adapter fixtures.
4. Parent verifies core and UI together, runs the acceptance matrix, documents gaps honestly, and resolves any contract change once for both owners.

## Primary references checked

- [debugpy README](https://github.com/microsoft/debugpy/blob/main/README.md) documents listening, optional wait-for-client behavior, and attach setup.
- [Delve DAP documentation](https://github.com/go-delve/delve/blob/master/Documentation/api/dap/README.md) distinguishes `dlv dap` launch/local attach from headless remote attach, lists mapping arguments, and describes disconnect ownership behavior.
- [DAP specification](https://microsoft.github.io/debug-adapter-protocol/specification) is the implementation reference for request, response, event, and capability field names.
