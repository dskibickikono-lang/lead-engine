.PHONY: build test
build:
	go build -o bin/lead-engine ./cmd/lead-engine
test:
	go test ./...
