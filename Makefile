.PHONY: build test setup
setup:
	git submodule update --init --recursive
build:
	go build -o bin/lead-engine ./cmd/lead-engine
test:
	go test ./...
