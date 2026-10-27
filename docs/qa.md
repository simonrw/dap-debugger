# Real-adapter QA

## Reproduce

```sh
mise install
uv sync --dev
mise exec -- uv run pytest tests/integration -v -rs
```

Tests use the public `SessionConfig` and `DebugSession` APIs, not mocks or transport internals. They run by default; `-m 'not integration'` excludes them. Debugpy must be installed in the pytest interpreter. Go must be on `PATH`; Delve is found on `PATH` or in `.tools/bin/dlv`.

Optional project-local Delve installation, without changing system installations or `go.mod`:

```sh
mkdir -p .tools/bin
GOBIN="$PWD/.tools/bin" go install github.com/go-delve/delve/cmd/dlv@v1.27.2
```

## Coverage

- Python launch through the real debugpy adapter.
- Python TCP attach to an independently started debugpy listener, with both identical and mapped source paths. The target must produce a heartbeat before the client connects.
- Go launch in `exec`, `debug` and `test` modes. The `exec` fixture is compiled with `-gcflags=all=-N -l`.
- Go remote attach to `dlv exec --headless --accept-multiclient --continue`, with both identical and mapped source paths. This is not attach to `dlv dap` or PID injection.
- Each scenario checks a breakpoint stop, stopped state, real thread IDs, stack source and line, scopes, local variables, explicit evaluation, verified breakpoint replacement, step over, breakpoint removal, and continue.
- Launch scenarios continue to target termination. Attach scenarios disconnect with ownership defaults, then require a fresh target heartbeat while the external debugger process remains alive. Go attach scenarios reconnect and repeat inspection against the same persistent server.

All targets, source mirrors, marker files, binaries, and logs are isolated under pytest temporary directories. TCP endpoints bind ephemeral loopback ports. Readiness requires target heartbeats and, for Python, an explicit listener marker, not a blind sleep. Startup, events, inspection, builds, and shutdown have deadlines. Cleanup sets the target's finish marker and reaps only fixture-owned process groups; it never signals a PID obtained from DAP.

Missing executables/modules and recognized OS debugging restrictions produce explicit pytest skip reasons with the observed diagnostic. Unknown adapter errors, timeouts, compilation errors, and assertion failures fail rather than skip. A skip is not evidence that a scenario works.

## Environment and evidence

- Host: Darwin arm64.
- Project virtual environment: CPython 3.13.2, debugpy 1.8.22, pytest 9.1.1, Textual 1.0.0.
- Shell Python: 3.14.7; integration tests use the project virtual environment instead.
- Go: go1.27.1 darwin/arm64.
- Project-local Delve: 1.27.2, build `360e7b2181d3da54115aaee4f569954bca95cfe3`.
- Direct Go fixture preflight: compilation exited 0; a real headless Delve process produced a target heartbeat.
- Initial QA result: **5 passed, 1 failed, 0 skipped**. Python launch exposed the executable-symlink defect documented below.
- After core fixes and expanded coverage, the parent ran `uv run pytest -q tests/test_cli.py tests/test_config.py tests/test_protocol.py tests/test_session.py tests/integration -ra`.
- Verified result: **78 passed in 7.20s**, including **8 real-adapter tests with no skips**. Python launch, same-path/mapped running-target attach, Go `exec`/`debug`/`test` launch, same-path/mapped headless attach and reconnect all passed. No Darwin ptrace restriction was observed.
- `.venv/bin/ruff check tests/integration` and `.venv/bin/ruff format --check tests/integration` both passed.

### Fixed defects

Python launch loses the virtual environment. `SessionConfig.from_dict()` routes the Python executable through `_executable()` and `_local_path()`, whose `Path.resolve()` dereferences `.venv/bin/python` to the underlying CPython installation. That installation does not contain the project's debugpy dependency.

The public session reports `initialize: unexpected EOF from debug adapter`; the queued adapter diagnostic is:

```text
/Users/simon/.local/share/uv/python/cpython-3.13.2-macos-aarch64-none/bin/python3.13: Error while finding module specification for 'debugpy.adapter' (ModuleNotFoundError: No module named 'debugpy')
```

Preserve executable symlinks while making explicit executable paths absolute relative to the configuration directory. This also affects the default `sys.executable`, not only explicit `python` settings. Do not install debugpy into the base interpreter to hide the defect.

A separate diagnostic run restored `config.python = sys.executable` after parsing and passed the complete Python launch inspection/step/termination flow. This is supporting evidence, not an additional passing pytest case. The integration test retains normal configuration parsing. The core owner fixed executable normalization, and the normal launch test now passes. No production files were changed by the test owner.

Expanded Go `debug` and `test` coverage found that Delve was building from the invoking shell's directory rather than the target module. The core owner corrected the owned Delve process working directory. Both modes now build, stop, inspect, step and exit successfully.

Loopback attach does not verify SSH deployment, firewall traversal, remote authentication, independently launched PID attachment, or cross-platform behavior. Real-adapter step into and step out are outside this integration suite.

## Toolchain mismatch regression

Activating the user's mise environment reproduced six Go failures because its installed Delve 1.26.3 rejected Go 1.27.1. Earlier agent runs did not activate mise and instead selected the project-local Delve 1.27.2 fallback. The tests correctly failed instead of bypassing the adapter's compatibility check.

The project keeps `go = "latest"` and pins Delve to 1.27.2. On 2026-10-27, the [official Go version endpoint](https://go.dev/VERSION?m=text) reported Go 1.27.3. [Delve 1.27.2's compatibility check](https://github.com/go-delve/delve/blob/v1.27.2/pkg/goversion/compat.go) supports Go 1.27. CI uses this verified pair.

After installing the mise-managed tools, `mise exec -- uv run pytest` passed **96 tests with no skips** in 25.48 seconds using Go 1.27.3 and Delve 1.27.2. Type checking and Ruff also passed. No tests were skipped to hide the mismatch, and Go version checking remains enabled.

## Final acceptance

The complete suite passed with **96 tests, zero failures and zero skips** in 23.58 seconds using `uv run pytest -q -ra`. This includes eight session-level real-adapter cases, one real-debugpy headless UI case in `tests/integration/test_tui.py`, and two controlling-terminal cases in `tests/integration/test_terminal.py`. The headless UI case drives source inspection, explicit evaluation, breakpoint removal, step over, continue and quit through Textual controls.

Headless UI tests also cover thread/frame selection, lazy variable expansion, delayed inspection responses, literal adapter text, breakpoint relocation/rejection, narrow layouts, live events while Help is open, startup errors and cleanup. Independent review identified a failed-step recovery race and an expression-input shortcut conflict. Both were reproduced with failing tests and fixed; the regressions now pass.

A subsequent real-terminal report exposed a gap in headless testing: debugpy's launcher gave its target ownership of the TUI's controlling terminal, causing zsh to suspend the debugger with `SIGTTIN` on input. Owned adapters now start in separate sessions. The new Python and Go pseudo-terminal tests verify foreground ownership and successful input after launch. The original zsh/uv reproduction also passed with focus-out, mouse motion, F1 help and Ctrl+Q shutdown.

`uv run ruff check .`, `git diff --check` and `uv build` passed. Build artifacts are `dist/dap_debugger-0.1.0-py3-none-any.whl` and `dist/dap_debugger-0.1.0.tar.gz`. Linux CI is configured but was not executed in this local session.
