# DAP Debugger

A general-purpose terminal debugger for any Debug Adapter Protocol adapter, written in Go with Bubble Tea v2. debugpy and Delve are the adapters tested so far; `internal/daptest` is a fake adapter for protocol-level tests.

## Tooling

* I use mise so if a required tool does not work, try `mise exec -- <command>`
* Run the tests with `mise exec -- make test`; integration tests use real debugpy and Delve
* Do not install debugpy into the mise Python. The tests use the repo `.venv` created by `uv sync` from `uv.lock`, which `make test` runs; Python tests fail if it is missing
