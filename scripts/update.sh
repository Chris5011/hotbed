#!/usr/bin/env bash
# scripts/update.sh — self-update a deployed OpenCode-SBX kit tree.
#
#   scripts/update.sh            # latest release + latest tree, keep daemon running
#   scripts/update.sh --yes      # also restart the resident daemon afterwards
#   scripts/update.sh v0.2.5     # pin a tag
#
# Order matters: tree first (gh via git the repo is auth'd with), then the
# release ASSET for the binary (tagged, SHA-summed, never a rebuild), then
# wrappers/prereqs via install.sh, then optionally the daemon restart.
#
# Your local working tree is preserved automatically: `git pull --autostash`
# stashes uncommitted changes (including ignored-looking tool drift, e.g.
# sbxd/bin refreshes are .gitignored) and pops them back after the merge —
# no manual `git stash` dance anymore.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
YES=0
for a in "$@"; do
    case "$a" in
        --yes) YES=1 ;;
        v*) TAG="$a" ;;
        *) echo "unknown arg: $a (usage: scripts/update.sh [--yes] [tag])" >&2; exit 2 ;;
    esac
done
say()  { printf '\033[36;1mupdate:\033[0m %s\n' "$*"; }
warn() { printf '\033[33;1mupdate:\033[0m %s\n' "$*"; }
fail() { printf '\033[31;1mupdate:\033[0m %s\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

command -v gh >/dev/null 2>&1 || fail "gh CLI required (gh auth login once)"
command -v git >/dev/null 2>&1 || fail "git required"
[[ -f vm/sbxvm.sh && -f bin/sbx && -d sbxd ]] || fail "run from the kit tree"

# ---- arch + latest release ----------------------------------------------------
case "$(uname -m)" in
    x86_64)  KA=amd64 ;;
    aarch64) KA=arm64 ;;
    *)       fail "unsupported arch $(uname -m) (assets: amd64/arm64)" ;;
esac
TAG="${TAG:-$(gh release list --limit 1 --json tagName --jq '.[0].tagName')}"
[[ "$TAG" == v* ]] || fail "weird tag: $TAG"
say "target release: $TAG"

# ---- tree refresh (uncommitted changes ride via autostash) --------------------
if [[ -n "$(git status --porcelain 2>/dev/null)" ]]; then
    warn "uncommitted local changes detected — git pull --autostash will carry them through and restore them"
fi
git fetch origin --tags --quiet
if git merge-base --is-ancestor HEAD origin/main 2>/dev/null; then
    git pull --ff-only --autostash --quiet origin main \
        || fail "tree not fast-forwardable — local divergence? (git status / git diff to review)"
    say "kit tree updated ($(git rev-parse --short HEAD))"
else
    say "kit tree already at origin/main (or diverged — skipping tree update; binary update continues)"
fi
git rev-parse -q --verify "refs/tags/$TAG" >/dev/null \
    || warn "tag $TAG not on the fetched tree yet (asset install proceeds anyway)"

# ---- release asset for the daemon binary ---------------------------------------
mkdir -p dist
rm -f "dist/sbxd-$TAG-linux-$KA" "dist/SHA256SUMS"
say "downloading release asset (sbxd-$TAG-linux-$KA + SHA256SUMS)…"
gh release download "$TAG" --repo "$(gh repo view --json nameWithOwner --jq .nameWithOwner)" \
    --pattern "sbxd-$TAG-linux-$KA" --pattern SHA256SUMS --dir dist --clobber
( cd dist && sha256sum -c SHA256SUMS --ignore-missing ) >/dev/null 2>&1 \
    || fail "SHA256SUMS verification failed — refusing to install the binary"
install -m 755 "dist/sbxd-$TAG-linux-$KA" sbxd/bin/sbxd
say "daemon binary installed: sbxd/bin/sbxd ← dist/sbxd-$TAG-linux-$KA"

# ---- prereqs, wrappers, base image pre-build -----------------------------------
./install.sh --yes

# ---- optional daemon restart (bind address preserved via detection) -------------
if [[ $YES -eq 1 ]]; then
    OLD_PID="$(ss -ltnp 2>/dev/null | grep ':7770 ' | grep -o 'pid=[0-9]*' | cut -d= -f2 | head -1)"
    BIND="127.0.0.1"
    if ss -ltn 2>/dev/null | grep -q ':7770 .* 0.0.0.0:7770'; then BIND="0.0.0.0"; fi
    ss -ltn 2>/dev/null | grep ':7770 ' | awk '{print $4}' | grep -q . || true
    if [[ -n "$OLD_PID" ]]; then
        say "stopping old daemon (PID $OLD_PID)…"
        kill "$OLD_PID"; sleep 1
    fi
    # new daemon out-of-the-way: exact PID from the listener, honest log file
    SDE_LOG="${TMPDIR:-/tmp}/sbxd-update-serve.log"
    SBXD_BIND="$BIND" nohup "$ROOT/sbxd/bin/sbxd" serve >"$SDE_LOG" 2>&1 &
    sleep 3
    NEW_PID="$(ss -ltnp 2>/dev/null | grep ':7770 ' | grep -o 'pid=[0-9]*' | cut -d= -f2 | head -1)"
    say "daemon restarted (PID ${NEW_PID:-unknown}, bind $BIND — log $SDE_LOG)"
    curl -s --max-time 5 http://127.0.0.1:7770/api/health \
        | grep -q '"version"' && say "daemon healthy" || warn "daemon not answering /api/health yet — check $SDE_LOG"
else
    say "done. restart the daemon to pick up the new binary:"
    say "  kill \$(ss -ltnp 2>/dev/null | grep :7770 | grep -o 'pid=[0-9]*' | cut -d= -f2 | head -1)"
    say "  SBXD_BIND=0.0.0.0 sbxd-serve     # or plain: sbxd-serve (loopback)"
fi
say "update complete ($TAG)"
