GO ?= go

.PHONY: build test vet

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

build:
	$(GO) build -trimpath -o bin/fleetd ./cmd/fleetd
	$(GO) build -trimpath -ldflags "-X main.version=$(VERSION)" -o bin/fleet-agent ./cmd/fleet-agent

test:
	$(GO) test -trimpath -race ./...

vet:
	$(GO) vet -trimpath ./...
