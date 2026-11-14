# VS Code probe

A throwaway VS Code extension that records the Debug Adapter Protocol messages VS Code exchanges with Delve or debugpy while it drives a scripted session. It registers its own debug types for the two adapters and logs with VS Code's debug adapter tracker API, so it needs no other extensions.

```sh
tools/vscode-probe/run.sh internal/ui/testdata/vscode/attach-python.json /tmp/vscode-attach-python.json
```

The scenario files in `internal/ui/testdata/vscode/*.json` describe each session (see `extension.js` for the format). The script opens a VS Code window with a temporary profile, which is removed afterwards, and writes the DAP log to the output file. debugpy and scenario servers run with the interpreter from the repository's `.venv`, which `uv sync` creates. Each scenario file keeps the requests between the `initialized` and `stopped` events, and `TestRequestsMatchVSCode` checks that dap-debugger sends the same ones; `go test ./internal/ui -run RequestsMatchVSCode -update-vscode` re-records them. Compare dap-debugger's own messages with `dap-debugger --trace FILE`.
