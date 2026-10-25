#!/usr/bin/env bash
# smoke.sh — end-to-end verification of the OpenCode-SBX container kit.
#
# Usage:   tests/smoke.sh [--fresh-images]     (build if images missing)
#
# Covers: engine reachability, bind-mounts, stack detection, per-stack tools,
# ownership translation, exit codes, persistence lifecycle, fresh mode, opencode,
# and teardown hygiene. Exits nonzero on the first failing section.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="${ROOT%/tests}"
SBX="$ROOT/bin/sbx"
PASS=0; FAIL=0
SCRATCH="$(mktemp -d /tmp/opencode/sbx-smoke.XXXXXX)"
[[ "${1:-}" == *fresh-images* ]] && "$SBX" build

hdr()  { printf '\n\e[1m== %s ==\e[0m\n' "$*"; }
pass() { printf '  \e[32m✔\e[0m %s\n' "$1"; PASS=$((PASS + 1)); }
fail() { printf '  \e[31m✘\e[0m %s\n' "$1"; FAIL=$((FAIL + 1)); }

trap 'echo; if [[ $FAIL -ne 0 ]]; then echo "smoke scratch kept: $SCRATCH"; else rm -rf "$SCRATCH"; fi' EXIT

# ---------------- engine ----------------
hdr "engine & doctor"
if "$SBX" doctor >/dev/null 2>&1; then pass "doctor"; else fail "doctor"; fi

# ---------------- detection ----------------
hdr "stack detection"
mkdir -p "$SCRATCH"/{n1,p1,g1,r1,b1}
: > "$SCRATCH/n1/package.json"
: > "$SCRATCH/p1/pyproject.toml"
: > "$SCRATCH/g1/go.mod"
: > "$SCRATCH/r1/Cargo.toml"
det()  { (cd "$1" && "$SBX" stack 2>/dev/null); }
[[ "$(det "$SCRATCH/n1")" == node ]]    && pass "package.json → node"    || fail "package.json → node"
[[ "$(det "$SCRATCH/p1")" == python ]]  && pass "pyproject.toml → python" || fail "pyproject.toml → python"
[[ "$(det "$SCRATCH/g1")" == go ]]      && pass "go.mod → go"            || fail "go.mod → go"
[[ "$(det "$SCRATCH/r1")" == rust ]]    && pass "Cargo.toml → rust"      || fail "Cargo.toml → rust"
[[ "$(det "$SCRATCH/b1")" == base ]]    && pass "empty → base"           || fail "empty → base"

# ---------------- fresh runs per stack ----------------
hdr "ephemeral (--fresh) runs"
fresh() { local dir="$1" stack="$2" script="$3"; (cd "$dir" && printf '%s\n' "$script" | timeout 120 "$SBX" --fresh --stack "$stack" 2>/dev/null); }
[[ "$(fresh "$SCRATCH/n1" node   'node -v >/dev/null && npm -v >/dev/null; echo fresh-node-ok')"    == *fresh-node-ok* ]] \
    && pass "node + npm in fresh sandbox"     || fail "node + npm in fresh sandbox"
[[ "$(fresh "$SCRATCH/p1" python 'python3 -V >/dev/null && uv --version >/dev/null; echo fresh-py-ok')"    == *fresh-py-ok* ]] \
    && pass "python + uv in fresh sandbox"    || fail "python + uv in fresh sandbox"
[[ "$(fresh "$SCRATCH/g1" go     'go version >/dev/null; echo fresh-go-ok')"               == *fresh-go-ok* ]] \
    && pass "go in fresh sandbox"             || fail "go in fresh sandbox"
[[ "$(fresh "$SCRATCH/r1" rust   'rustc --version >/dev/null; echo fresh-rust-ok')"        == *fresh-rust-ok* ]] \
    && pass "rust in fresh sandbox"           || fail "rust in fresh sandbox"
[[ "$("$SBX" ls)" != *"$SCRATCH"* ]] \
    && pass "no leftover containers after fresh runs" || fail "leftover containers after fresh runs"

# ---------------- ownership ----------------
hdr "ownership translation"
out="$(fresh "$SCRATCH/b1" base 'touch owned-here.txt; echo touched')"
if [[ -f "$SCRATCH/b1/owned-here.txt" ]] && [[ "$(stat -c %U "$SCRATCH/b1/owned-here.txt")" == "$(id -un)" ]]; then
    pass "container-created file is host-user owned"
else
    fail "container-created file is host-user owned (out=$out; ls: $(ls -la $SCRATCH/b1/owned-here.txt 2>/dev/null))"
fi

# ---------------- lifecycle ----------------
hdr "persistent lifecycle"
cd "$SCRATCH/b1"
if [[ "$("$SBX" status 2>&1)" == *"no sandbox yet"* ]]; then pass "status before create"; else fail "status before create"; fi
if printf 'echo exec-hello\nexit 0\n' | timeout 120 "$SBX" exec -- bash 2>/dev/null | grep -q exec-hello; then
    pass "exec creates sandbox + streams stdin"
else
    fail "exec creates sandbox + streams stdin"
fi
"$SBX" status 2>&1 | grep -q " \[running\]" && pass "status running"        || fail "status running"
"$SBX" exec -- true >/dev/null 2>&1              && pass "second exec reattaches"  || fail "second exec reattaches"
timeout 120 "$SBX" opencode -- --version 2>/dev/null | grep -q opencode \
                                                 && pass "opencode via sbx"        || fail "opencode via sbx"
"$SBX" exec -- bash -c 'exit 42' 2>/dev/null; [[ $? -eq 42 ]] \
                                                 && pass "exit codes propagate"    || fail "exit codes propagate"

"$SBX" rm --yes >/dev/null 2>&1
if [[ "$("$SBX" status 2>&1)" == *"no sandbox yet"* ]]; then pass "rm deletes sandbox"; else fail "rm deletes sandbox"; fi
[[ "$("$SBX" ls)" != *"sbx-base-"*"$SCRATCH"* ]] && pass "ls reflects removal"    || fail "ls reflects removal"
cd - >/dev/null

# ---------------- teardown ----------------
hdr "cleanup"
"$SBX" prune --yes >/dev/null 2>&1 && pass "prune --yes"  || fail "prune --yes"
"$SBX" caches ls >/dev/null 2>&1   && pass "caches ls"    || fail "caches ls"

echo
if [[ $FAIL -eq 0 ]]; then
    printf '\e[32;1mSMOKE: all %d checks passed ✓\e[0m\n' "$PASS"
else
    printf '\e[31;1mSMOKE: %d failed / %d passed\e[0m\n' "$FAIL" "$PASS"
    exit 1
fi