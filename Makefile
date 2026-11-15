.PHONY: test

test:
	uv sync --locked
	go test -race ./...
