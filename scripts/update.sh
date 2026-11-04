#!/usr/bin/env bash
# scripts/update.sh -- self-update a deployed hotbed kit tree.
#
#   scripts/update.sh              # latest release + latest tree, keep daemon running
#   scripts/update.sh --yes        # also restart the resident daemon afterwards
#   scripts/update.sh --yes --tls  # restart AND flip HOTBEDD_TLS (https) in the same shot
#   scripts/update.sh v0.2.5       # pin a tag
#
# Order matters: tree first (git pull), then the release ASSET for the binary
# (tagged, SHA-summed, never a rebuild), then wrappers/prereqs via
# install.sh, then optionally the daemon restart.
#
# No gh CLI needed: releases come from the GitHub REST API with curl,
# anonymously for the public repo. A token is used when one is around
# (GITHUB_TOKEN / GH_TOKEN, or `gh auth token` if gh happens to be logged
# in) - that keeps private forks working, same as quick-install.sh.
#
# Your local working tree is preserved automatically: `git pull --autostash`
# stashes uncommitted edits in tracked files and pops them back after the
# merge -- no manual `git stash` dance anymore.
#
# Fresh boxes: a bootstrap may fetch THIS script (e.g. curl from the raw
# URL into scripts/update.sh) BEFORE any git pull exists, so the file lands untracked
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

command -v curl >/dev/null 2>&1 || fail "curl required"
command -v git >/dev/null 2>&1 || fail "git required"
[[ -f vm/hotbedvm.sh && -f bin/hbd && -f hotbed/go.mod ]] || fail "run from the kit tree"

# ---- arch + latest release ----------------------------------------------------
case "$(uname -m)" in
    x86_64)  KA=amd64 ;;
    aarch64) KA=arm64 ;;
    *)       fail "unsupported arch $(uname -m) (assets: amd64/arm64)" ;;
esac
# owner/repo from the clone's origin (forks update from themselves)
REPO="$(git remote get-url origin 2>/dev/null | sed -E 's#^.*github\.com[:/]##; s#\.git$##')"
[[ "$REPO" == */* ]] || REPO="Chris5011/hotbed"
TOKEN="${GITHUB_TOKEN:-${GH_TOKEN:-}}"
if [[ -z "$TOKEN" ]] && command -v gh >/dev/null 2>&1; then
    TOKEN="$(gh auth token 2>/dev/null || true)"
fi
api() { # api PATH — GitHub REST call, authenticated only when we have a token
    curl -fsSL ${TOKEN:+-H "Authorization: Bearer $TOKEN"} \
         -H "Accept: application/vnd.github+json" "https://api.github.com/repos/$REPO$1"
}
json_field() { # json_field KEY < json — first "KEY": "value" (no jq; sed quits
    # at the first match, so pipefail never sees a SIGPIPE)
    sed -n "/\"$1\": *\"/{s/.*\"$1\": *\"\([^\"]*\)\".*/\1/p;q;}"
}
fetch_asset() { # fetch_asset NAME DEST — release asset via the API (public + private)
    local id
    # one line per JSON object; an asset's "url" (the API url) precedes its
    # nested "uploader" object, so the line naming the asset holds its url
    id="$(printf '%s' "$REL_JSON" | tr -d '\n' | tr '{' '\n' | grep -F "\"name\": \"$1\"" | json_field url | sed 's#.*/assets/##')" || true
    [[ -n "$id" ]] || return 1
    curl -fsSL ${TOKEN:+-H "Authorization: Bearer $TOKEN"} -H "Accept: application/octet-stream" \
        -o "$2" "https://api.github.com/repos/$REPO/releases/assets/$id"
}
if [[ -n "${TAG:-}" ]]; then
    REL_JSON="$(api "/releases/tags/$TAG" 2>/dev/null || true)"
else
    REL_JSON="$(api /releases/latest 2>/dev/null || true)"
    TAG="$(printf '%s' "$REL_JSON" | json_field tag_name || true)"
fi
[[ -n "$REL_JSON" ]] || fail "no release info from github.com/$REPO (offline? private fork without GITHUB_TOKEN?)"
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
        aside="$(mktemp -d "${TMPDIR:-/tmp}/hotbedd-update-aside.XXXXXX")"
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
rm -f "dist/hotbedd-$TAG-linux-$KA" "dist/SHA256SUMS"
say "downloading release asset (hotbedd-$TAG-linux-$KA + SHA256SUMS)..."
fetch_asset "hotbedd-$TAG-linux-$KA" "dist/hotbedd-$TAG-linux-$KA" \
    || fail "release $TAG has no asset hotbedd-$TAG-linux-$KA"
fetch_asset SHA256SUMS dist/SHA256SUMS || fail "release $TAG has no SHA256SUMS"
( cd dist && sha256sum -c SHA256SUMS --ignore-missing ) >/dev/null 2>&1 \
    || fail "SHA256SUMS verification failed -- refusing to install the binary"
install -m 755 "dist/hotbedd-$TAG-linux-$KA" hotbed/bin/hotbedd
say "daemon binary installed: hotbed/bin/hotbedd <- dist/hotbedd-$TAG-linux-$KA"

# ---- prereqs, wrappers, base image pre-build -----------------------------------
bash ./install.sh --yes   # bash: works even where the exec bit got lost

# ---- optional daemon restart (bind + TLS preserved via honest detection) ---------
# lsn prints "<addr>:<port> <pid>" for the given port's listener; the pid rides
# INSIDE ss's users:(("hotbedd",pid=N,...)) token, so match() it anywhere in the
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
        && tr '\0' '\n' <"/proc/$OLD_PID/environ" 2>/dev/null | grep -q '^HOTBEDD_TLS=1$'; then
        OLD_TLS=1
    fi
    TLS_RUN="$OLD_TLS"
    if [[ $TLS -eq 1 ]]; then
        TLS_RUN=1
        say "TLS: requested via --tls flag"
    elif [[ $OLD_TLS -eq 1 ]]; then
        say "TLS: preserving the old daemon's HOTBEDD_TLS=1"
    fi
    if [[ -n "$OLD_PID" ]]; then
        TLSNOTE=""
        if [[ $TLS_RUN -eq 1 ]]; then TLSNOTE=" HOTBEDD_TLS=1"; fi
        say "resident daemon: PID $OLD_PID on $OLD_ADDR -> restarting with HOTBEDD_BIND=$BIND$TLSNOTE"
    fi
    if [[ -n "$OLD_PID" ]]; then
        say "stopping old daemon (PID $OLD_PID)..."
        kill "$OLD_PID" 2>/dev/null || true
        sleep 1
        if [[ -n "$(lsn 7770)" ]]; then
            fail "port 7770 still answered after stopping PID $OLD_PID -- not double-starting; check: ss -ltnp | grep 7770"
        fi
    fi
    SDE_LOG="${TMPDIR:-/tmp}/hotbedd-update-serve.log"
    : >"$SDE_LOG"
    if [[ $TLS_RUN -eq 1 ]]; then
        HOTBEDD_BIND="$BIND" HOTBEDD_TLS=1 nohup "$ROOT/hotbed/bin/hotbedd" serve >"$SDE_LOG" 2>&1 &
    else
        HOTBEDD_BIND="$BIND" nohup "$ROOT/hotbed/bin/hotbedd" serve >"$SDE_LOG" 2>&1 &
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
    say "  HOTBEDD_BIND=0.0.0.0 HOTBEDD_TLS=1 hotbedd-serve   # https for the clipboard (see README)"
    say "  HOTBEDD_BIND=0.0.0.0 hotbedd-serve              # plain http (clipboard degrades honestly)"
fi
say "update complete ($TAG)"
