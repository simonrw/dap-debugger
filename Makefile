.PHONY: test

test:
	uv sync
	go test -race ./...
