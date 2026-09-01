BINARY  := poirot
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build test lint tidy
build:
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/poirot
test:
	go test ./... -race -count=1
lint:
	go vet ./...
tidy:
	go mod tidy
