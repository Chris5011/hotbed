#!/usr/bin/env bash
# scripts/release.sh VERSION — cross-build the sbxd daemon (linux amd64+arm64),
# stamp the tagged version in, hash, and drop everything into dist/.
#
# Go toolchain only (module is stdlib-only; CGO disabled). No docker required.
# See docs/RELEASING.md for the full release playbook.
set -euo pipefail

VER="${1:?usage: make release VERSION=<semver>  (e.g. 0.2.1)}"
repo="$(cd "$(dirname "$0")/.." && pwd)"
out="$repo/dist"
mkdir -p "$out"

export GOOS=linux
export CGO_ENABLED=0

for arch in amd64 arm64; do
    ( cd "$repo/sbxd" && GOARCH="$arch" go build -trimpath \
        -ldflags "-s -w -X sbxd/internal/env.Version=v$VER" \
        -o "$out/sbxd-v$VER-linux-$arch" ./cmd/sbxd )
done

( cd "$out" && sha256sum sbxd-v$VER-linux-* > SHA256SUMS )

# Stamp proof: run the native-arch binary and let it print its version.
native="$(go env GOARCH)"
"$out/sbxd-v$VER-linux-$native" version

echo
ls -lh "$out"
