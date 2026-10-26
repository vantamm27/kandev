GO ?= go
VERSION := $(shell awk -F'"' '/^version:/ {print $$2}' manifest.yaml)
ARCHIVE := kandev-fleet-$(VERSION).tar.gz

.PHONY: test vet package-host clean

test:
	$(GO) test -trimpath -race -timeout 5m ./...

vet:
	$(GO) vet -trimpath ./...

# package-host builds a host-platform archive the way the official plugin
# template does: stage manifest, server binary, and UI, then plugin-pack.
package-host:
	rm -rf .build/stage && mkdir -p .build/stage/server
	cp manifest.yaml .build/stage/manifest.yaml
	cp -R ui .build/stage/ui
	$(GO) build -trimpath -o .build/stage/server/plugin-$$($(GO) env GOOS)-$$($(GO) env GOARCH) ./server
	$(GO) run github.com/kandev/kandev/cmd/plugin-pack -dir .build/stage -out .build/$(ARCHIVE) -platform-only
	@echo "package: .build/$(ARCHIVE)"

clean:
	rm -rf .build
