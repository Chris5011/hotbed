# Hotbed / hotbed — build & release orchestration.
#
#   make check              fmt-check + go vet + local build (fast sanity)
#   make build              local binary into hotbed/bin/hotbedd
#   make smoke              full hermetic suite (needs docker + one running
#                           sandbox; see docs/RELEASING.md §5 before running)
#   make release [VERSION]  cross-built, version-stamped artifacts + SHA256SUMS
#                           into dist/ (Go toolchain only — no docker needed)
#   make clean              remove dist/ and the local build output
#
# VERSION stamps the binary via -ldflags "-X hotbed/internal/env.Version=…"
# (the source default remains archival). Bump this default alongside the tag.

VERSION ?= 0.7.1

GO     ?= go
GOFMT  ?= gofmt
BIN    := hotbed/bin
DIST   := dist

.PHONY: check fmt vet build static smoke release clean

check: fmt vet build static

static:
	@bash scripts/static-check.sh

fmt:
	@out="$$($(GOFMT) -l hotbed/internal hotbed/cmd)"; \
	if [ -n "$$out" ]; then echo "need gofmt:"; echo "$$out"; exit 1; fi
	@echo "fmt: clean"

vet:
	cd hotbed && $(GO) vet ./...

build: fmt
	cd hotbed && $(GO) build -o bin/hotbedd ./cmd/hotbedd

smoke:
	HOTBEDD_SMOKE_PORT=7790 bash tests/hotbedd-smoke.sh

release:
	bash scripts/release.sh $(VERSION)

clean:
	rm -rf $(DIST)
	rm -f $(BIN)/hotbedd
