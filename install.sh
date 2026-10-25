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

# ---- daemon binary: adopt a release asset, else build from source ------------
SBXD="$ROOT/sbxd/bin/sbxd"
if [[ ! -x "$SBXD" ]]; then
    # No binary yet? A downloaded release asset (sbxd-v…-linux-<arch>) is
    # perfectly usable — look for the one matching this host's arch in the
    # places people drop it: dist/ and the kit root.
    case "$(uname -m)" in
        x86_64)  KA=amd64 ;;
        aarch64) KA=arm64 ;;
        *)       KA=""    ;;
    esac
    if [[ -n "$KA" ]]; then
        shopt -s nullglob
        local_globs=( "$ROOT"/dist/sbxd-v*-linux-"$KA" "$ROOT"/sbxd-v*-linux-"$KA" )
        shopt -u nullglob
        if (( ${#local_globs[@]} > 0 )); then
            pick="$(printf '%s\n' "${local_globs[@]}" | sort -V | tail -1)"
            mkdir -p "$ROOT/sbxd/bin"
            install -m 755 "$pick" "$SBXD"
            say "adopted release binary: ${pick#"$ROOT"/} → sbxd/bin/sbxd"
        fi
    fi
fi
if [[ ! -x "$SBXD" ]]; then
    command -v go >/dev/null 2>&1 || fail "no daemon binary and no Go toolchain — download a release asset into dist/ or the kit root, or run 'make build'"
fi
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

# ---- docker usable-check: "installed" ≠ "usable by the user who runs sbxd" ----
# The classic on a fresh/followed-the-docs box: docker is installed, the CLI
# answers root fine, yet the dashboard shows docker as "down" — because the
# user running sbxd can't reach /var/run/docker.sock (socket group) or the
# engine isn't running. Non-fatal: warn with the exact remedy.
duser="${SUDO_USER:-$USER}"
dusable=0
if timeout 10 docker info >/dev/null 2>&1; then
    # launched via sudo? root reaches the socket even when sbxd's real user
    # cannot — check that user's group membership despite root's success.
    if [[ -n "${SUDO_USER:-}" ]] && ! id -nG "$duser" 2>/dev/null | tr ' ' '\n' | grep -qxi docker; then
        say "docker reachable — but '$duser' (the user who will run sbxd) is NOT in the"
        say "  docker socket group → the dashboard's health badge says \"down\". Remedy:"
        say "    sudo usermod -aG docker \"$duser\"   # re-login for the group change"
        dusable=1
    else
        say "docker engine reachable — container sandboxes are ready"
        dusable=1
    fi
else
    derr="$(timeout 10 docker info 2>&1 || true)"
    if echo "$derr" | grep -qi "permission denied"; then
        say "docker installed, but '$duser' cannot reach /var/run/docker.sock → the dashboard"
        say "  will honestly show docker as \"down\". Remedy (socket group + re-login):"
        say "    sudo usermod -aG docker \"$duser\""
        say "    # then verify as your own user:  docker info  (no sudo)"
    elif echo "$derr" | grep -qi "cannot connect"; then
        say "docker installed, but the ENGINE is not reachable. Remedy:"
        say "    sudo systemctl enable --now docker     # then: docker info (no sudo)"
    else
        say "docker installed, but 'docker info' did not answer cleanly (first line:"
        say "  ${derr%%$'\n'*}) — check: sudo systemctl status docker"
    fi
fi

# ---- container stack image: build sbx:base once so dashboard create works ----
# 'sbx:<stack>' is a kit-built image with no registry upstream. On a fresh
# box the first dashboard create would otherwise dangle on 'pull access
# denied for sbx'. Any other stack builds on demand in the same path.
if [[ ${dusable:-0} = 1 ]] && ! timeout 10 docker image inspect sbx:base >/dev/null 2>&1; then
    say "stack image sbx:base missing — building it now (one-time, layer-cached)…"
    if SBX_ROOT="$ROOT" "$ROOT/bin/sbx" build base >/dev/null 2>&1; then
        say "sbx:base ready — container creation from the dashboard works out of the box"
    else
        say "sbx:base build failed (non-fatal) — the first dashboard create retries it"
        say "  automatically; manual build: $ROOT/bin/sbx build base"
    fi
fi

virtmiss=()
for c in virsh virt-install qemu-img xorriso; do
    command -v "$c" >/dev/null 2>&1 || virtmiss+=( "$c" )
done
if (( ${#virtmiss[@]} )); then
    say "VM · libvirt backend needs: ${virtmiss[*]} (skip → docker-backend VMs still work: apt install libvirt-daemon-system libvirt-clients virtinst xorriso)"
else
    if virsh -c qemu:///system uri >/dev/null 2>&1; then
        say "libvirt stack present — both VM backends available"
    else
        say "libvirt tools found, but the SYSTEM DAEMON is not running/reachable — the"
        say "  VM · libvirt backend will fail until this is settled (docker backend won't):"
        say "    sudo apt install -y libvirt-daemon-system libvirt-daemon-config-network"
        say "    sudo usermod -aG libvirt \"\$USER\"   # re-login for the group change"
        say "    sudo systemctl enable --now libvirtd            # classic distros"
        say "      # or, on modular distros (Debian 12+, Ubuntu 24.04+):"
        say "    sudo systemctl enable --now virtqemud.socket virtnetworkd.socket"
    fi
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
