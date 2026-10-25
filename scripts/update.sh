#!/usr/bin/env bash
# scripts/update.sh -- self-update a deployed OpenCode-SBX kit tree.
#
#   scripts/update.sh              # latest release + latest tree, keep daemon running
#   scripts/update.sh --yes        # also restart the resident daemon afterwards
#   scripts/update.sh --yes --tls  # restart AND flip SBXD_TLS (https) in the same shot
#   scripts/update.sh v0.2.5       # pin a tag
#
# Order matters: tree first (gh via git the repo is auth'd with), then the
# release ASSET for the binary (tagged, SHA-summed, never a rebuild), then
# wrappers/prereqs via install.sh, then optionally the daemon restart.
#
# Your local working tree is preserved automatically: `git pull --autostash`
# stashes uncommitted edits in tracked files and pops them back after the
# merge -- no manual `git stash` dance anymore.
#
# Fresh boxes: the bootstrap fetches THIS script (`gh api ... | base64 -d >
# scripts/update.sh`) BEFORE any git pull exists, so the file lands untracked
# and the later merge refuses to write the now-tracked file over it. That is
# not divergence -- the script detects untracked files the incoming merge
# wants to write, moves them aside, merges, and reconciles:
#   identical              -> tracked version wins (drop the stale copy)
#   scripts/update.sh      -> tracked version wins (receipt kept aside; the
#                             fresh copy re-execs so bash never keeps reading
#                             a replaced script mid-run)
#   anything else          -> YOUR untracked version wins (review + commit it)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
YES=0
TLS=0
for a in "$@"; do
    case "$a" in
        --yes) YES=1 ;;
        --tls) TLS=1 ;;
        v*) TAG="$a" ;;
        *) echo "unknown arg: $a (usage: scripts/update.sh [--yes] [--tls] [tag])" >&2; exit 2 ;;
    esac
done
say()  { printf '\033[36;1mupdate:\033[0m %s\n' "$*"; }
warn() { printf '\033[33;1mupdate:\033[0m %s\n' "$*"; }
fail() { printf '\033[31;1mupdate:\033[0m %s\n' "$*" >&2; exit 1; }

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
if [[ -n "$(git diff --name-only HEAD 2>/dev/null)" ]]; then
    warn "uncommitted edits in tracked files -- git pull --autostash carries them through and restores them"
fi
git fetch origin --tags --quiet
if git merge-base --is-ancestor HEAD origin/main 2>/dev/null; then
    colliders="$(comm -12 \
        <(git diff --name-only HEAD origin/main 2>/dev/null | sort) \
        <(git ls-files --others --exclude-standard 2>/dev/null | sort))"
    aside=""
    if [[ -n "$colliders" ]]; then
        aside="$(mktemp -d "${TMPDIR:-/tmp}/sbxd-update-aside.XXXXXX")"
        warn "untracked files collide with incoming tree changes -- moving them aside for the merge:"
        while IFS= read -r f; do
            [[ -n "$f" ]] || continue
            mkdir -p "$aside/$(dirname "$f")"
            mv "$ROOT/$f" "$aside/$f"
            say "  $f"
        done <<<"$colliders"
    fi
    old_head="$(git rev-parse HEAD)"
    if git pull --ff-only --autostash --quiet origin main; then
        if [[ "$(git rev-parse HEAD)" != "$old_head" ]]; then
            say "kit tree updated ($(git rev-parse --short HEAD))"
        fi
        rerun=0
        if [[ -n "$aside" ]]; then
            while IFS= read -r f; do
                [[ -n "$f" ]] || continue
                if cmp -s "$aside/$f" "$ROOT/$f"; then
                    say "  $f: incoming version identical -- stale copy dropped"
                elif [[ "$f" == scripts/update.sh ]]; then
                    say "  $f: tracked version installed (previous bootstrap copy kept at $aside/scripts/update.sh)"
                    rerun=1   # my own file just changed under me -- exec the fresh one
                else
                    mv "$aside/$f" "$ROOT/$f"
                    warn "  $f: kept YOUR untracked version over the incoming one (review it -- autostash carries it from here on)"
                fi
            done <<<"$colliders"
            if [[ $rerun -eq 0 ]]; then
                rm -rf "$aside"
            fi
        fi
        if [[ $rerun -eq 1 ]]; then
            say "scripts/update.sh was updated mid-run -- restarting from the fresh version"
            exec bash "$ROOT/scripts/update.sh" "$@"
        fi
    else
        if [[ -n "$aside" ]]; then
            while IFS= read -r f; do
                [[ -n "$f" && -f "$aside/$f" ]] || continue
                mv "$aside/$f" "$ROOT/$f"
            done <<<"${colliders:-}"
            rm -rf "$aside"
        fi
        fail "tree update failed (merge refused or autostash pop conflicted) -- check git status / git stash list"
    fi
else
    say "kit tree already at origin/main (or diverged -- skipping tree update; binary update continues)"
fi
git rev-parse -q --verify "refs/tags/$TAG" >/dev/null \
    || warn "tag $TAG not on the fetched tree yet (asset install proceeds anyway)"

# ---- release asset for the daemon binary ---------------------------------------
mkdir -p dist
rm -f "dist/sbxd-$TAG-linux-$KA" "dist/SHA256SUMS"
say "downloading release asset (sbxd-$TAG-linux-$KA + SHA256SUMS)..."
gh release download "$TAG" --repo "$(gh repo view --json nameWithOwner --jq .nameWithOwner)" \
    --pattern "sbxd-$TAG-linux-$KA" --pattern SHA256SUMS --dir dist --clobber
( cd dist && sha256sum -c SHA256SUMS --ignore-missing ) >/dev/null 2>&1 \
    || fail "SHA256SUMS verification failed -- refusing to install the binary"
install -m 755 "dist/sbxd-$TAG-linux-$KA" sbxd/bin/sbxd
say "daemon binary installed: sbxd/bin/sbxd <- dist/sbxd-$TAG-linux-$KA"

# ---- prereqs, wrappers, base image pre-build -----------------------------------
./install.sh --yes

# ---- optional daemon restart (bind + TLS preserved via honest detection) ---------
# lsn prints "<addr>:<port> <pid>" for the given port's listener; the pid rides
# INSIDE ss's users:(("sbxd",pid=N,...)) token, so match() it anywhere in the
# tail fields -- a naive grep for "0.0.0.0:7770 twice on one line" never matches
# and always misread the real bind.
lsn() {
    ss -ltnp 2>/dev/null | awk -v p="$1" '
        $4 ~ ":"p"$" {
            addr=$4; pid=""
            for (i=5; i<=NF; i++) if (match($i, /pid=[0-9]+/)) { pid=substr($i, RSTART+4, RLENGTH-4); break }
            print addr " " pid
            exit
        }'
}
if [[ $YES -eq 1 ]]; then
    lread="$(lsn 7770)"
    OLD_ADDR="${lread%% *}"
    OLD_PID="${lread#* }"
    if [[ "$lread" == "$OLD_PID" ]]; then OLD_PID=""; fi
    base="${OLD_ADDR%:*}"
    case "$base" in
        "")                         BIND="127.0.0.1" ;;
        "127.0.0.1"|"::1"|"[::1]")  BIND="127.0.0.1" ;;
        "0.0.0.0"|"*"|"[::]"|"::")  BIND="0.0.0.0" ;;
        *)                          BIND="$base" ;;
    esac
    OLD_TLS=0
    if [[ -n "$OLD_PID" && -r "/proc/$OLD_PID/environ" ]] \
        && tr '\0' '\n' <"/proc/$OLD_PID/environ" 2>/dev/null | grep -q '^SBXD_TLS=1$'; then
        OLD_TLS=1
    fi
    TLS_RUN="$OLD_TLS"
    if [[ $TLS -eq 1 ]]; then
        TLS_RUN=1
        say "TLS: requested via --tls flag"
    elif [[ $OLD_TLS -eq 1 ]]; then
        say "TLS: preserving the old daemon's SBXD_TLS=1"
    fi
    if [[ -n "$OLD_PID" ]]; then
        TLSNOTE=""
        if [[ $TLS_RUN -eq 1 ]]; then TLSNOTE=" SBXD_TLS=1"; fi
        say "resident daemon: PID $OLD_PID on $OLD_ADDR -> restarting with SBXD_BIND=$BIND$TLSNOTE"
    fi
    if [[ -n "$OLD_PID" ]]; then
        say "stopping old daemon (PID $OLD_PID)..."
        kill "$OLD_PID" 2>/dev/null || true
        sleep 1
        if [[ -n "$(lsn 7770)" ]]; then
            fail "port 7770 still answered after stopping PID $OLD_PID -- not double-starting; check: ss -ltnp | grep 7770"
        fi
    fi
    SDE_LOG="${TMPDIR:-/tmp}/sbxd-update-serve.log"
    : >"$SDE_LOG"
    if [[ $TLS_RUN -eq 1 ]]; then
        SBXD_BIND="$BIND" SBXD_TLS=1 nohup "$ROOT/sbxd/bin/sbxd" serve >"$SDE_LOG" 2>&1 &
    else
        SBXD_BIND="$BIND" nohup "$ROOT/sbxd/bin/sbxd" serve >"$SDE_LOG" 2>&1 &
    fi
    sleep 3
    nread="$(lsn 7770)"
    NEW_ADDR="${nread%% *}"
    NEW_PID="${nread#* }"
    if [[ "$nread" == "$NEW_PID" ]]; then NEW_PID=""; fi
    say "daemon restarted (PID ${NEW_PID:-?} on ${NEW_ADDR:-nothing} -- log $SDE_LOG)"
    if [[ $TLS_RUN -eq 1 ]]; then
        proto="https"
        fp="$(grep -ao 'cert sha256 fingerprint: [0-9a-f]\{64\}' "$SDE_LOG" | grep -o '[0-9a-f]\{64\}' | head -1)"
        if [[ -n "$fp" ]]; then
            say "cert fingerprint: $fp  (pinned -- browser warns once)"
        else
            warn "no snakeoil fingerprint in $SDE_LOG -- TLS did not come up; log tail:"
            tail -n 6 "$SDE_LOG" | sed 's/^/    /'
        fi
    else
        proto="http"
    fi
    if curl -sk --max-time 5 "$proto://127.0.0.1:7770/api/health" | grep -q '"version"'; then
        say "daemon healthy ($proto)"
    else
        warn "daemon not answering $proto://127.0.0.1:7770/api/health -- log tail:"
        tail -n 6 "$SDE_LOG" | sed 's/^/    /'
    fi
else
    say "done. restart the daemon to pick up the new binary:"
    say "  kill \$(ss -ltnp 2>/dev/null | grep :7770 | grep -o 'pid=[0-9]*' | cut -d= -f2 | head -1)"
    say "  SBXD_BIND=0.0.0.0 SBXD_TLS=1 sbxd-serve   # https for the clipboard (see README)"
    say "  SBXD_BIND=0.0.0.0 sbxd-serve              # plain http (clipboard degrades honestly)"
fi
say "update complete ($TAG)"
