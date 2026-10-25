# SBX Desktop / OpenCode-SBX — build & release orchestration.
#
#   make check              fmt-check + go vet + local build (fast sanity)
#   make build              local binary into sbxd/bin/sbxd
#   make smoke              full hermetic suite (needs docker + one running
#                           sandbox; see docs/RELEASING.md §5 before running)
#   make release [VERSION]  cross-built, version-stamped artifacts + SHA256SUMS
#                           into dist/ (Go toolchain only — no docker needed)
#   make clean              remove dist/ and the local build output
#
# VERSION stamps the binary via -ldflags "-X sbxd/internal/env.Version=…"
# (the source default remains archival). Bump this default alongside the tag.

VERSION ?= 0.3.0

GO     ?= go
GOFMT  ?= gofmt
BIN    := sbxd/bin
DIST   := dist

.PHONY: check fmt vet build smoke release clean

check: fmt vet build

fmt:
	@out="$$($(GOFMT) -l sbxd/internal sbxd/cmd)"; \
	if [ -n "$$out" ]; then echo "need gofmt:"; echo "$$out"; exit 1; fi
	@echo "fmt: clean"

vet:
	cd sbxd && $(GO) vet ./...

build: fmt
	cd sbxd && $(GO) build -o bin/sbxd ./cmd/sbxd

smoke:
	SBXD_SMOKE_PORT=7790 bash tests/sbxddsmoke.sh

release:
	bash scripts/release.sh $(VERSION)

clean:
	rm -rf $(DIST)
	rm -f $(BIN)/sbxd
