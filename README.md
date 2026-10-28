<img src="assets/icon.svg" width="64" height="64" alt="">

# dap-debugger

A terminal debugger for Python and Go, built with Textual and the Debug Adapter Protocol (DAP). Launch a program under debugpy or Delve, or attach to a debugger controlling an existing target over TCP.

## Install and run

Requires Python 3.11 or newer. Go debugging also requires Go and [Delve](https://github.com/go-delve/delve).

```sh
uv sync
uv run dap-debugger examples/python-launch.json
```

`uv sync` installs debugpy as a development dependency. For a standalone installation, install debugpy into the interpreter selected by the configuration.

```sh
pip install . debugpy
dap-debugger examples/python-launch.json
```

Install Delve and put its executable on `PATH`, or set the configuration's `dlv` field to its path.

```sh
go install github.com/go-delve/delve/cmd/dlv@latest
uv run dap-debugger examples/go-launch.json
```

The equivalent module entry point is `python -m dap_debugger CONFIG.json`. Use `--check` to validate a configuration without starting a debugger.

## Interface

The code fills a thin frame with a single row of compact buttons. Threads, stack frames, variables, output and evaluation stay hidden until opened. Errors reveal the diagnostics pane automatically. Submit expressions explicitly in the evaluation input. **Evaluation can modify the target.**

| Key | Action |
| --- | --- |
| `F2` | Toggle threads / stack |
| `F3` | Toggle variables |
| `F4` | Toggle output |
| `Escape` | Close panes and return to code |
| `F5` | Continue |
| `F6` | Pause |
| `F9` | Toggle breakpoint on the selected source line |
| `F10` | Step over |
| `F11` | Step into |
| `Shift+F11` | Step out |
| `Ctrl+E` | Focus evaluation input |
| `Ctrl+D` | Disconnect attached target or stop launched target |
| `Ctrl+Q` | Close session and quit |
| `F1` | Help |

Hover over a button for its action and shortcut. The code has focus on startup. Use Tab to move between controls and arrow keys to select source lines, threads, frames and variables. Inspectors open beside the code on wide terminals and replace it on narrow terminals.

## Launch configuration

Configurations are JSON objects. Local paths are relative to the configuration file, not the shell's working directory. Adapter arguments live in `arguments`.

```json
{
  "name": "My Python application",
  "adapter": "python",
  "request": "launch",
  "arguments": {
    "program": "./main.py",
    "cwd": ".",
    "args": [],
    "justMyCode": true
  },
  "breakpoints": {
    "./main.py": [12]
  }
}
```

For Python, use `module` instead of `program` to launch a module. The optional top-level `python` field selects the interpreter containing debugpy. For Go, set `adapter` to `go` and `arguments.mode` to `debug`, `test` or `exec`. `program` names the package directory or prebuilt executable. Build Go executables with `-gcflags='all=-N -l'` for inspection without optimizations.

Other settings include `requestTimeout` (15 seconds), `startupTimeout` (60 seconds), `dlv` (executable name or path) and `pathMappings`. Unknown top-level fields are rejected. See [the configuration contract](docs/implementation-plan.md#configuration-contract) for details.

## Attach to Python

Start a debug-enabled target in another terminal. Omit `--wait-for-client` to let it run before connecting.

```sh
uv run python -m debugpy --listen 127.0.0.1:5678 --wait-for-client --configure-subProcess false examples/hello.py
```

Then connect from the debugger.

```sh
uv run dap-debugger examples/python-attach.json
```

The client connects directly to debugpy's DAP port. An arbitrary Python application port is not a debugger endpoint. To enable debugging in an independently running process, the remote operator can use debugpy's `--pid` option, subject to platform permissions and prerequisites.

## Attach to Go

Use a persistent headless Delve server for an existing target, not a bare `dlv dap` server. For the example, build a binary and start it under Delve.

```sh
(cd examples/go && go build -gcflags='all=-N -l' -o /tmp/dap-hello .)
dlv exec /tmp/dap-hello --headless --listen=127.0.0.1:38697 --accept-multiclient
```

Then connect.

```sh
uv run dap-debugger examples/go-attach.json
```

To attach Delve to an already running Go process, the remote operator uses this command, with the actual process ID.

```sh
dlv attach 12345 --headless --listen=127.0.0.1:38697 --accept-multiclient --continue
```

The client sends `attach` with `mode: "remote"`. **Use `--accept-multiclient` to preserve the remote session on disconnect.** Delve can leave a stopped target paused after disconnect. Continue before disconnecting when it should keep running. Remote attach does not recover historical target output or guarantee output redirection.

## Remote machines and source mapping

**DAP provides neither authentication nor encryption and permits code execution.** Bind adapters to loopback and establish an SSH tunnel using your existing SSH setup.

```sh
ssh -N -L 5678:127.0.0.1:5678 user@remote-host
```

Connect to the forwarded local port and map your checkout to the target's source paths.

```json
{
  "adapter": "python",
  "request": "attach",
  "connect": { "host": "127.0.0.1", "port": 5678 },
  "arguments": { "justMyCode": true },
  "pathMappings": [
    { "localRoot": "./src", "remoteRoot": "/srv/app" }
  ]
}
```

The same mapping structure works for Go. The client supplies debugpy `pathMappings` or Delve `substitutePath`; the adapters translate paths. The local checkout must match the target's source. Mapping does not copy files. For Go, use the source paths recorded when the binary was built.

## Lifecycle and limitations

Closing a locally launched session requests target termination and reaps the owned adapter. Closing an attached session requests detach without target termination and never signals remote processes. Adapter behavior and server configuration still determine whether the target resumes or remains paused.

This version supports one session at a time. Interactive target stdin, integrated terminals, child-process sessions, reverse execution and PID injection from the UI are not supported. Python targets use the internal console. Missing local source is displayed as unavailable, with adapter source retrieval where supported.

On macOS, Go debugging requires working system debugger permissions and a signed Delve installation. Containers may need ptrace permissions. Connection errors, missing adapter executables and failed launches appear as diagnostics rather than silently switching modes.

## Development

Install the project tools with mise so tests use a compatible Go and Delve pair. The project tracks the latest Go release and pins Delve to a compatible version. CI currently verifies Go 1.27.3 with Delve 1.27.2.

```sh
mise install
uv sync
mise exec -- uv run pytest
uv run ty check .
uv run ruff check .
```

With mise activated in your shell, `uv run pytest` uses the same tools. A Delve compatibility error means the selected debugger is too old for Go; update Delve rather than disable its version check.

Real-adapter tests use temporary loopback listeners and clean up their target processes. Tests skip with explicit reasons when an adapter or OS debugging capability is unavailable. A skip is not evidence of support. See [QA evidence](docs/qa.md), [research and primary sources](docs/research.md), and [the implementation plan](docs/implementation-plan.md).

Created in [T3 Code](https://t3.codes).
