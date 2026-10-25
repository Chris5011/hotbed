#!/usr/bin/env bash
# install.sh — one-command setup for SBX Desktop on a Linux host.
#
# Run this from the OpenCode-SBX repo root (or a downloaded release tree).
# It is idempotent and never starts, stops or touches running sandboxes.
#
#   ./install.sh                     interactive defaults (asks about the image)
#   ./install.sh --yes               accept defaults non-interactively
#   ./install.sh --fetch-image       pre-fetch the ~1 GB cloud master image now
#   ./install.sh --config-init       import LLM providers once, non-interactively
#
# After installing:
#   sbxd-serve            start the daemon (dashboard/API at 127.0.0.1:7770)
#   sbx  …                classic container CLI (self-locates the kit)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN_DIR="$HOME/.local/bin"
YES=0; DO_IMAGE=0; DO_CONFIG=0

for a in "$@"; do
    case "$a" in
        --yes)         YES=1 ;;
        --fetch-image) DO_IMAGE=1 ;;
        --config-init) DO_CONFIG=1 ;;
        *) echo "unknown flag: $a (see --help)" >&2; exit 2 ;;
    esac
done

say()  { printf '\033[36;1minstall:\033[0m %s\n' "$*"; }
fail() { printf '\033[31;1minstall:\033[0m %s\n' "$*" >&2; exit 1; }
ask()  { # ask QUESTION [default]
    local dflt="${2:-n}"
    if [[ $YES -eq 1 || ! -t 0 ]]; then echo "$dflt"; return; fi
    read -r -p "$1 [$dflt] " REPLY; echo "${REPLY:-$dflt}"
}

# ---- sanity: run from a real kit tree ---------------------------------------
[[ -f "$ROOT/vm/sbxvm.sh" && -f "$ROOT/bin/sbx" && -d "$ROOT/sbxd" ]] \
    || fail "run install.sh from the OpenCode-SBX repo root (or a release tree)"

mkdir -p "$BIN_DIR"
say "install root: $ROOT"

# ---- daemon binary: build if missing ----------------------------------------
SBXD="$ROOT/sbxd/bin/sbxd"
[[ -x "$SBXD" ]] || { command -v go >/dev/null 2>&1 || fail "no daemon binary and no Go toolchain — run 'make build' or download a release first"; }
if [[ ! -x "$SBXD" ]]; then
    ( cd "$ROOT/sbxd" && go build -o bin/sbxd ./cmd/sbxd ) && say "built sbxd from source"
fi

# ---- install entry points ----------------------------------------------------
ln -sf "$ROOT/bin/sbx" "$BIN_DIR/sbx"        # kit CLI self-locates its root
ln -sf "$SBXD" "$BIN_DIR/sbxd"               # CLI (needs SBX_ROOT, see wrapper)
cat > "$BIN_DIR/sbxd-serve" <<EOF
#!/usr/bin/env bash
# daemon entry point: pins SBX_ROOT to this checkout and runs sbxd serve
exec env SBX_ROOT="$ROOT" "$SBXD" serve "\$@"
EOF
chmod +x "$BIN_DIR/sbxd-serve"
case ":$PATH:" in *":$BIN_DIR:"*) ;; *) say "note: $BIN_DIR is not on your PATH — add it to shell profile" ;; esac

# ---- dependency gate ----------------------------------------------------------
coremiss=()
for c in bash docker tmux ssh curl jq; do
    command -v "$c" >/dev/null 2>&1 || coremiss+=( "$c" )
done
(( ${#coremiss[@]} )) && fail "missing required: ${coremiss[*]} (install them first — docker: https://docs.docker.com/engine/install/)"

virtmiss=()
for c in virsh virt-install qemu-img xorriso; do
    command -v "$c" >/dev/null 2>&1 || virtmiss+=( "$c" )
done
if (( ${#virtmiss[@]} )); then
    say "VM · libvirt backend needs: ${virtmiss[*]} (skip → docker-backend VMs still work: apt install libvirt-daemon-system libvirt-clients virtinst xorriso)"
else
    say "libvirt stack present — both VM backends available"
fi

# ---- cloud master image (the only big artifact; fetched, never shipped) --------
if [[ ! -f "$ROOT/vm/images/debian-13-genericcloud-amd64.qcow2" ]]; then
    ans="$(ask "fetch the Debian cloud master image now? (~330 MB download, ~1 GB on disk; without it no VM backends work) [y/N]" n)"
    if [[ $DO_IMAGE -eq 1 ]] && [[ ! $ans =~ ^[Yy]$ ]]; then ans=y; fi
    if [[ $ans =~ ^[Yy]$ ]]; then
        SBX_ROOT="$ROOT" bash "$ROOT/vm/sbxvm.sh" fetch-image
    else
        say "skipping image fetch — run: SBX_ROOT=\"$ROOT\" bash \"$ROOT/vm/sbxvm.sh\" fetch-image  (needed once before creating VMs)"
    fi
else
    say "cloud master image already present"
fi

# ---- optional first config (-–config-init imports host opencode providers) -----
if [[ ! -f "$HOME/.config/sbxd/config.json" ]]; then
    if [[ $DO_CONFIG -eq 1 ]]; then
        env SBX_ROOT="$ROOT" "$SBXD" config init || say "config init failed (non-fatal — run later: sbxd config init)"
    else
        say "config: none yet — create with: SBX_ROOT=\"$ROOT\" \"$SBXD\" config init"
    fi
fi

say "done. next:  $BIN_DIR/sbxd-serve   →  dashboard/API at http://127.0.0.1:7770"
