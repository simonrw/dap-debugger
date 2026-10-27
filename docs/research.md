# DAP debugger implementation research

## Recommended implementation

Use Python, Textual and `asyncio`, with one language-neutral DAP connection/session implementation and small debugpy/Delve launch-argument builders. Support four primary workflows: Python launch, Python TCP attach, Go launch, and Go TCP attach to an existing headless Delve session. Keep adapter ownership separate from target ownership. Remote attach must never automatically kill the remote target or server.

These conclusions come from official specifications, documentation and upstream source, not live adapter testing. Upstream `main`/`master` links are mutable. Record the installed debugpy/Delve versions in diagnostics and verify the four workflows against those versions before release.

## Wire protocol and startup

Sources: [DAP overview][dap-overview], [DAP schema][dap-schema], [debugpy client handling][debugpy-clients].

- Serialize JSON to UTF-8 bytes, then prefix ASCII `Content-Length: <byte count>\r\n\r\n`. The length is bytes, not Unicode characters. Read headers through the empty line, then exactly that many bytes. Handle partial frames, multiple frames per read, EOF and invalid lengths. Serialize writes and apply bounded message/header sizes.
- Maintain a monotonically increasing outgoing `seq`. Match responses by `request_seq`, not command name or arrival order. Requests, responses and events can interleave. Failed responses use `success: false`; surface their message and body rather than treating them as successful empty results.
- One reader task must continually dispatch messages. Never perform a blocking event handler inside that reader that itself waits for a DAP response. Fail pending requests on EOF, cancellation or transport errors.
- Register event handlers/futures before sending requests. `initialized` is an event, not the response to `initialize`.
- First send `initialize` and await its response. Send `launch` or `attach`, but do not await its response before handling `initialized`. Once `initialized` arrives, configure breakpoints/exceptions, await those responses, then send `configurationDone` if advertised. Finally await the pending launch/attach response. debugpy explicitly delays that response until configuration finishes; serially awaiting launch first deadlocks. Some adapters respond earlier, so support either response timing.
- Give startup/configuration its own timeout, distinguish build time from normal request timeouts, and unblock initialization on a failed launch/attach response even if `initialized` never arrives.

Example initialization, with `adapterID` changed to `go` for Delve:

```json
{"seq":1,"type":"request","command":"initialize","arguments":{"clientID":"dap-debugger","clientName":"DAP Debugger","adapterID":"debugpy","pathFormat":"path","linesStartAt1":true,"columnsStartAt1":true,"supportsVariableType":true,"supportsVariablePaging":false,"supportsRunInTerminalRequest":false,"supportsStartDebuggingRequest":false}}
```

Subsequent examples show the `arguments` object only. Wrap them in the same request envelope with the corresponding `command` and a fresh `seq`.

Initial configuration:

```text
setBreakpoints        {"source":{"path":"/work/app/main.py"},"breakpoints":[{"line":12}]}
setExceptionBreakpoints {"filters":[]}
configurationDone     {}
```

`setBreakpoints` replaces the complete breakpoint list for that source. Send an empty list to remove all of its breakpoints. Preserve requested lines separately from returned verified/relocated lines, and handle subsequent `breakpoint` events. Only use exception filters advertised by the adapter; sending an empty filter list is an explicit choice to disable configurable exception breaks, not a universal recommended default.

## Python / debugpy

Sources: [debugpy README][debugpy-readme], [adapter entry point][debugpy-main], [client handling][debugpy-clients], [configuration settings][debugpy-settings].

### Launch a local target

Install into the interpreter that will run the adapter:

```sh
python -m pip install debugpy
python -m debugpy.adapter
```

The second command is the child process the TUI starts, with piped stdin/stdout for DAP and separately drained stderr for diagnostics. Without `--port`, the adapter uses stdio. Do not merge stderr into the DAP stdout stream.

Send `launch`:

```json
{"name":"Python launch","type":"debugpy","request":"launch","program":"/work/app/main.py","python":["/work/app/.venv/bin/python"],"cwd":"/work/app","args":[],"console":"internalConsole","justMyCode":true,"stopOnEntry":true,"subProcess":false,"redirectOutput":true}
```

Use an explicit chosen target interpreter and absolute paths. For module execution, replace `program` with `module`, not both. `console: internalConsole` avoids the reverse `runInTerminal` request; interactive target stdin/PTY support should be a separate feature. `subProcess: false` keeps the initial product single-session and avoids needing `startDebugging` requests or debugpy-specific subprocess attach events.

### Attach to a remote running Python program

On the target machine, start a debug-enabled program that runs immediately:

```sh
python -m debugpy --listen 127.0.0.1:5678 --configure-subProcess false /srv/app/main.py
```

Add `--wait-for-client` before the script when execution should wait for the debugger. Alternatively inject into an already running Python PID on that same target machine:

```sh
python -m debugpy --listen 127.0.0.1:5678 --configure-subProcess false --pid 12345
```

PID injection is platform/permissions dependent and may need platform debugger tooling. Do not imply that an arbitrary Python process already exposes DAP. `debugpy.listen(...)` inside the application is another documented opt-in route.

Create an SSH tunnel outside the TUI, using the user's existing SSH setup:

```sh
ssh -N -L 5678:127.0.0.1:5678 user@remote-host
```

The TUI opens a TCP connection directly to `127.0.0.1:5678`, initializes that connection, then sends `attach`:

```json
{"name":"Python attach","type":"debugpy","request":"attach","connect":{"host":"127.0.0.1","port":5678},"pathMappings":[{"localRoot":"/work/app","remoteRoot":"/srv/app"}],"justMyCode":true,"subProcess":false,"redirectOutput":true}
```

Important: debugpy's listening port is already a DAP adapter endpoint. Do not start another local stdio adapter and expect its `attach.connect` field to proxy to that endpoint. The client establishes the TCP connection itself. In debugpy's adapter source, `connect` identifies the attach-to-an-existing-server case, rather than implementing that client-side network connection.

Set subprocess policy on the remote command line before attach; the attach payload is too late to prevent all pre-connection subprocess instrumentation. Remote `redirectOutput` only captures output once debugging redirects it; it cannot recover historical output.

## Go / Delve

Sources: [Delve DAP guide][delve-dap], [`dlv dap`][delve-command], [`dlv attach`][delve-attach], [`dlv exec`][delve-exec], [configuration types][delve-types], [DAP server implementation][delve-server].

### Launch a local target

Install Delve with a Go toolchain compatible with the selected release:

```sh
go install github.com/go-delve/delve/cmd/dlv@latest
dlv dap --listen=127.0.0.1:0
```

Start `dlv dap` as an owned subprocess and read its stdout until `DAP server listening at: <host>:<port>` supplies the allocated port. Connect once and retain that connection for DAP. Do not use a throwaway TCP readiness probe: this is a single-client server. Drain stdout/stderr throughout the session, with a startup timeout and child-exit detection. `dlv dap` does not use stdio DAP, does not accept `--accept-multiclient`, and does not support `--continue`.

Send `launch` after initialization:

```json
{"name":"Go launch","type":"go","request":"launch","mode":"debug","program":"/work/app","cwd":"/work/app","args":[],"stopOnEntry":true,"outputMode":"remote"}
```

`mode: debug` builds a package; `mode: test` builds a test binary. For a prebuilt executable:

```sh
go build -gcflags='all=-N -l' -o /work/app/bin/app ./cmd/app
```

Then use `mode: exec` with `program: /work/app/bin/app`. The `program`, `cwd` and build paths refer to the machine running Delve, even if the client is remote. Do not disable Delve's Go-version compatibility checks by default.

Current server source supports `outputMode: remote` to forward launched target stdout/stderr as DAP `output` events. The DAP README still describes stdout/stderr-only output, so validate this option with the installed release. The portable fallback for an owned local adapter is to capture its process stdout/stderr. Remote attach cannot retroactively redirect an already running target's output into the client.

### Attach remotely to an existing Go process

On the remote machine, attach Delve to the existing PID and leave it running before the UI connects:

```sh
dlv attach 12345 --headless --listen=127.0.0.1:38697 --accept-multiclient --continue
```

Alternatively start a binary under a persistent headless debugger:

```sh
dlv exec /srv/app/bin/app --headless --listen=127.0.0.1:38697 --accept-multiclient --continue -- --app-flag value
```

Use `--continue` only when immediate execution is intended; omit it to initially leave the target stopped. Attach permissions, container ptrace policies and OS debugger restrictions still apply.

Tunnel the endpoint:

```sh
ssh -N -L 38697:127.0.0.1:38697 user@remote-host
```

Connect the TUI's DAP TCP transport to `127.0.0.1:38697`, initialize, then send `attach`:

```json
{"name":"Go remote attach","type":"go","request":"attach","mode":"remote","stopOnEntry":true,"substitutePath":[{"from":"/work/app","to":"/srv/app"}]}
```

The host/port belong to the TUI transport configuration, not the Delve attach payload. A headless Delve endpoint accepts DAP as well as its JSON-RPC API; do not wrap DAP inside JSON-RPC.

Do not confuse these distinct modes:

- `dlv --headless ...` plus `attach`/`mode: remote`: Delve already owns a target session, and the TUI joins it. This is the primary remote-running-target workflow.
- `dlv dap --listen=...` plus `attach`/`mode: local`/`processId: 12345`: the DAP request asks Delve to attach to a PID on Delve's machine. The machine can be remote to the TUI; `local` means local to the adapter. `mode: remote` is not supported by `dlv dap`.

Delve reports goroutines as DAP threads. Use the advertised IDs, never OS thread IDs or a hardcoded thread ID. `stopOnEntry: true` is a useful initial inspection default; current Delve otherwise continues after `configurationDone`.

## Source paths and inspection

Sources: [DAP schema][dap-schema], [debugpy settings][debugpy-settings], [Delve types][delve-types].

- Send client-local absolute source paths in breakpoints. For Python, use `pathMappings: [{localRoot, remoteRoot}]`. For Go, use `substitutePath: [{from: localRoot, to: remoteRoot}]`. Delve's `to` should match the recorded build/source path, which is not necessarily the deployed executable's directory. Do not reverse these fields.
- Let the adapter perform its mapping; do not apply the same mapping again in a generic transport layer. A mapping does not synchronize files. The local checkout must match the running program.
- For a stack frame's source, prefer a valid local file, or use DAP `source` with its `sourceReference` when provided. Handle unavailable source as a normal UI state. Do not assume Delve can fetch arbitrary remote source files.
- On `stopped`, fetch `threads`, select the event's `threadId` when available, fetch `stackTrace`, then `scopes` for the selected frame, then `variables` lazily for expanded references. Honor `allThreadsStopped` and `allThreadsContinued`.
- Invalidate stack frames, scopes and variable references after execution resumes. Drop late inspection responses using a session/stop generation so old results do not overwrite a newer stop.

Example inspection/control arguments, using IDs returned by the adapter:

```text
threads       {}
stackTrace    {"threadId":7,"startFrame":0,"levels":50}
scopes        {"frameId":42}
variables     {"variablesReference":1001}
evaluate      {"expression":"counter","frameId":42,"context":"repl"}
continue      {"threadId":7}
next          {"threadId":7}
stepIn        {"threadId":7}
stepOut       {"threadId":7}
pause         {"threadId":7}
```

## Shutdown and capability pitfalls

Sources: [DAP schema][dap-schema], [debugpy clients][debugpy-clients], [debugpy session finalization][debugpy-sessions], [Delve lifecycle guide][delve-dap], [Delve server source][delve-server].

- Separate actions: **Stop launched target**, **Disconnect**, and optionally **Terminate attached target** with explicit confirmation. Closing the UI should disconnect remote sessions, not terminate them. Never kill a process group that the UI did not create.
- For ordinary local launch shutdown, send `disconnect` with `{"restart":false,"terminateDebuggee":true}` where supported. Delve's local launch defaults already kill the launched target even without that option; debugpy's launcher is also terminated during session finalization. Do not promise that disconnect can preserve a locally launched target for these adapters.
- For Python attach, send `disconnect` with `{"restart":false,"terminateDebuggee":false}` and close the connection after a bounded wait. debugpy's default is to preserve an attached process.
- For Go remote attach to a multi-client headless server, use `terminateDebuggee: false`. The server and target remain alive, but the target stays in its current running/stopped state. Offer explicit **Continue and disconnect** versus **Disconnect and leave paused**. For the former, clear the client's breakpoints and continue before disconnecting, while recognizing that exceptions or another stop can race. Do not claim that `suspendDebuggee: false` guarantees resumption: current Delve does not implement that option.
- A single-client headless Delve server shuts down on disconnect. If it originally launched the target with `exec`/`debug`, it also kills that target, even though the UI used a remote attach request. Therefore document `--accept-multiclient` as necessary for the recommended persistent remote workflow. Client request kind alone does not determine ownership.
- `terminated` means the debugging session ended; `exited` reports the debuggee's exit code. They are not interchangeable. On natural termination, send one idempotent disconnect cleanup; adapters such as Delve can wait for it to exit. Avoid recursive shutdown when an explicit disconnect also produces `terminated`.
- After protocol shutdown, close transports, reject pending requests, cancel reader/inspection tasks and reap only owned adapter subprocesses. Escalate terminate/kill for owned adapters after a timeout, not for external remote servers. Always drain child pipes to avoid deadlocks.
- Inspect the `initialize` capability response and merge later `capabilities` events. Delve enables some capabilities only after attach. Unsupported flags normally mean false. Gate optional requests such as `configurationDone`, `terminate`, restart and mutation features. Do not advertise reverse `runInTerminal`/`startDebugging` support unless implemented; return a failed response for unknown reverse requests rather than silently hanging.
- Exact schema spelling matters: disconnect support is **`supportTerminateDebuggee`** and **`supportSuspendDebuggee`**, singular `support`, unlike most `supports...` fields. Current debugpy source advertises the nonstandard `supportsTerminateDebuggee`; accept that known alias if normalizing these adapters. Delve uses the standard singular field and may publish it in a capabilities event.
- Delve advertises no `terminate` request. debugpy advertises one, but its default implementation hard-kills rather than guaranteeing a graceful application shutdown. `onTerminate: KeyboardInterrupt` changes debugpy behavior. Do not label every DAP terminate action “graceful”.
- Both endpoints are powerful debugging interfaces, not authenticated application APIs. Prefer loopback plus SSH tunneling. debugpy explicitly warns that reachable clients can execute arbitrary code. Do not bind to `0.0.0.0` by default or claim TLS/authentication is built into this client.

## Textual assessment

Sources: [Textual workers][textual-workers], [Textual testing][textual-tests].

Textual is a reasonable simple choice: it supports async event handlers/workers, hierarchical widgets and headless testing. Keep the DAP session usable without Textual so protocol tests do not require terminal rendering. Use asyncio streams/subprocesses directly; avoid a thread per request or a second event loop.

Run connection setup, builds and long inspection operations in async workers, not blocking handlers. An exclusive refresh worker can cancel stale inspection work; the underlying transport must still consume and safely ignore a cancelled request's eventual response. Keep the reader alive for the entire session rather than cancelling it on every UI refresh. Use messages/reactive state to render snapshots. If blocking work needs a thread, use Textual's `call_from_thread` or `post_message`, not direct widget mutations.

A compact initial layout can contain source, threads/goroutines, stack, variables, output and an evaluation input. Tests can use `App.run_test()` and `Pilot` for shortcuts and selection, plus independent fake-adapter transport tests and real debugpy/Delve smoke tests. Textual does not solve PTY input or DAP adapter semantics; defer interactive terminal launching until its reverse request/process lifecycle can be implemented correctly.

## Verification checklist

- Fragmented/combined UTF-8 DAP frames; out-of-order responses; failed response, EOF and timeout cleanup.
- Adapter withholding launch response until `configurationDone`; `initialized` arriving before launch returns; launch failing before `initialized`.
- Each language: launch, breakpoint, inspect locals, evaluate, next/step, continue and stop.
- Each language: attach to an already running target, map local paths, stop/inspect, disconnect and confirm process survival.
- Go persistent attach: disconnect while paused versus continue-and-disconnect; reconnect to the same headless server.
- Natural exit and startup failure do not leak owned subprocesses or tasks. Remote attach does not kill externally owned processes.
- Output forwarding tested against installed Delve version; unavailable source and unsupported capabilities produce useful UI feedback.

## Note capture

Notion capture was attempted through tool discovery, but no Notion connector/create-page tool was available in this child task. No credentials were accessed. This file is the durable research record; a parent with the connector can copy its body into a page in the user's Personal notebook with tags `research`, `debugging` and `architecture`.

## Primary sources

[dap-overview]: https://microsoft.github.io/debug-adapter-protocol/overview
[dap-schema]: https://github.com/microsoft/debug-adapter-protocol/blob/main/debugAdapterProtocol.json
[debugpy-readme]: https://github.com/microsoft/debugpy/blob/main/README.md
[debugpy-main]: https://github.com/microsoft/debugpy/blob/main/src/debugpy/adapter/__main__.py
[debugpy-clients]: https://github.com/microsoft/debugpy/blob/main/src/debugpy/adapter/clients.py
[debugpy-sessions]: https://github.com/microsoft/debugpy/blob/main/src/debugpy/adapter/sessions.py
[debugpy-settings]: https://github.com/microsoft/debugpy/wiki/Debug-configuration-settings
[delve-dap]: https://github.com/go-delve/delve/blob/master/Documentation/api/dap/README.md
[delve-command]: https://github.com/go-delve/delve/blob/master/Documentation/usage/dlv_dap.md
[delve-attach]: https://github.com/go-delve/delve/blob/master/Documentation/usage/dlv_attach.md
[delve-exec]: https://github.com/go-delve/delve/blob/master/Documentation/usage/dlv_exec.md
[delve-types]: https://github.com/go-delve/delve/blob/master/service/dap/types.go
[delve-server]: https://github.com/go-delve/delve/blob/master/service/dap/server.go
[textual-workers]: https://textual.textualize.io/guide/workers/
[textual-tests]: https://textual.textualize.io/guide/testing/
