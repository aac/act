.PHONY: build test lint fmt tidy fuzz release-local

build:
	go build ./...

test:
	# -timeout is Go's PER-PACKAGE limit. Explicit here because Go's default
	# (10m) is not enough on a busy machine: internal/cli measured 1129-1210s
	# uncontended-except-for-fleet on the mini, 2026-09-04 (act-f763fc). 30m
	# leaves real headroom without masking a genuine hang.
	go test -timeout 30m ./...

lint:
	go vet ./...

fmt:
	gofmt -w .

tidy:
	go mod tidy

fuzz:
	go test -fuzz=Fuzz -fuzztime=10s ./internal/fold/

# release-local builds the same 5-target matrix as the release workflow
# into ./dist for manual smoke-testing without cutting a tag.
# Override VERSION=v0.0.0-local on the command line to embed a custom string.
VERSION ?= v0.0.0-local

release-local:
	@rm -rf dist
	@mkdir -p dist
	@set -e; \
	for spec in darwin:amd64: darwin:arm64: linux:amd64: linux:arm64: windows:amd64:.exe; do \
	  goos=$$(echo "$$spec" | cut -d: -f1); \
	  goarch=$$(echo "$$spec" | cut -d: -f2); \
	  ext=$$(echo "$$spec" | cut -d: -f3); \
	  out="dist/act-$$goos-$$goarch$$ext"; \
	  echo "==> $$out"; \
	  GOOS=$$goos GOARCH=$$goarch CGO_ENABLED=0 go build \
	    -trimpath \
	    -ldflags "-s -w -X github.com/aac/act/internal/version.Binary=$(VERSION)" \
	    -o "$$out" ./cmd/act/; \
	  (cd dist && sha256sum "act-$$goos-$$goarch$$ext" > "act-$$goos-$$goarch$$ext.sha256"); \
	done
	@cd dist && cat *.sha256 > checksums.txt
	@echo "Artifacts:"
	@ls -la dist
