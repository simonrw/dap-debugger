# VS Code probe

A throwaway VS Code extension that records the Debug Adapter Protocol messages VS Code exchanges with Delve or debugpy while it drives a scripted session: a breakpoint, step over, continue, then stop. It registers its own debug types for the two adapters and logs with VS Code's debug adapter tracker API, so it needs no other extensions.

```sh
tools/vscode-probe/run.sh go /tmp/vscode-go.json
tools/vscode-probe/run.sh python /tmp/vscode-python.json
```

debugpy runs from the repository's `.venv`, created by `uv sync`. It opens a VS Code window with a temporary profile, which is removed afterwards. The requests between the `initialized` and `stopped` events are kept in `internal/ui/testdata/vscode-*.json`, and `TestRequestsMatchVSCode` checks that dap-debugger sends the same ones. Compare dap-debugger's own messages with `dap-debugger --trace FILE`.
