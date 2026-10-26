GO ?= go

.PHONY: build test vet

build:
	$(GO) build -trimpath -o bin/fleetd ./cmd/fleetd

test:
	$(GO) test -trimpath -race ./...

vet:
	$(GO) vet -trimpath ./...
