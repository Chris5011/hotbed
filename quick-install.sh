#!/usr/bin/env bash
# quick-install.sh — Hotbed in one command.
#
#   curl -fsSL https://raw.githubusercontent.com/Chris5011/hotbed/main/quick-install.sh | bash
#   curl -fsSL …/quick-install.sh | bash -s -- --deps --service
#
# What it does (idempotent — re-run it to update):
#   1. fetches the kit tree into --dir (git clone/pull, or a release tarball
#      when git is missing)
#   2. installs the daemon binary: the checksum-verified release asset for
#      this arch, or a source build when the ref has no release / Go is there
#   3. runs the kit's install.sh --yes (PATH wrappers, prereq gate,
#      hotbed:base image) — with --deps it apt-installs missing prereqs first
#   4. with --service: a systemd *user* unit for the daemon, started now
#      (otherwise prints how to start it)
#
# Options:
#   --dir DIR      install location (default: $HOTBED_DIR or ~/hotbed)
#   --ref REF      release tag (vX.Y.Z) or branch (default: latest release,
#                  else main)
#   --deps         apt-install missing host prerequisites (sudo; Debian/Ubuntu)
#   --service      install + start the systemd user unit hotbedd.service
#   --vm-image     also fetch the Debian master image for VMs (~330 MB)
#   --build        always build the daemon from source (needs Go)
#   -h, --help     this text
#
# Private repo: export GITHUB_TOKEN (repo scope) or have `gh` logged in.
# Never touches existing sandboxes; never restarts a daemon it didn't start.
set -euo pipefail

REPO="${HOTBED_REPO:-Chris5011/hotbed}"
DIR="${HOTBED_DIR:-$HOME/hotbed}"
REF=""
DEPS=0; SERVICE=0; VMIMG=0; BUILD=0

while (( $# )); do
    case "$1" in
        --dir)      DIR="${2:?--dir needs a path}"; shift ;;
        --ref)      REF="${2:?--ref needs a tag or branch}"; shift ;;
        --deps)     DEPS=1 ;;
        --service)  SERVICE=1 ;;
        --vm-image) VMIMG=1 ;;
        --build)    BUILD=1 ;;
        -h|--help)  sed -n '2,/^set -euo/p' "${BASH_SOURCE[0]:-/dev/null}" 2>/dev/null | sed '$d; s/^# \{0,1\}//' \
                        || echo "see the header of quick-install.sh"; exit 0 ;;
        *) echo "unknown option: $1 (see --help)" >&2; exit 2 ;;
    esac
    shift
done

say()  { printf '\033[36;1mhotbed:\033[0m %s\n' "$*"; }
warn() { printf '\033[33;1mhotbed:\033[0m %s\n' "$*" >&2; }
fail() { printf '\033[31;1mhotbed:\033[0m %s\n' "$*" >&2; exit 1; }

# ---- preflight ----------------------------------------------------------------
[[ "$(uname -s)" == Linux ]] || fail "Linux only (on Windows: run this inside a WSL2 distro)"
case "$(uname -m)" in
    x86_64)  ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    *) ARCH="" ; warn "no release binary for $(uname -m) — the daemon will be built from source" ;;
esac
[[ $(id -u) -ne 0 ]] || warn "running as root: Hotbed is meant to run as your normal user (sandboxes get your uid)"
command -v curl >/dev/null 2>&1 || fail "curl is required"
command -v tar  >/dev/null 2>&1 || fail "tar is required"
DIR="${DIR/#\~/$HOME}"

# GitHub auth (only needed while the repo is private)
TOKEN="${GITHUB_TOKEN:-${GH_TOKEN:-}}"
if [[ -z "$TOKEN" ]] && command -v gh >/dev/null 2>&1; then
    TOKEN="$(gh auth token 2>/dev/null || true)"
fi
api() { # api PATH [extra curl args...] — GitHub REST call, auth when we have it
    local p="$1"; shift
    curl -fsSL ${TOKEN:+-H "Authorization: Bearer $TOKEN"} \
         -H "Accept: application/vnd.github+json" "$@" "https://api.github.com/repos/$REPO$p"
}
json_field() { # json_field KEY < json — first "KEY": "value" (no jq needed)
    sed -n "s/.*\"$1\": *\"\\([^\"]*\\)\".*/\\1/p" | head -1
}

# ---- 1 · which ref -------------------------------------------------------------
REL_JSON=""
if [[ -z "$REF" ]]; then
    REL_JSON="$(api /releases/latest 2>/dev/null || true)"
    REF="$(printf '%s' "$REL_JSON" | json_field tag_name)"
    if [[ -z "$REF" ]]; then
        REF=main
        warn "no release found (private repo without GITHUB_TOKEN, or none published) — using branch main"
    fi
elif [[ "$REF" == v[0-9]* ]]; then
    REL_JSON="$(api "/releases/tags/$REF" 2>/dev/null || true)"
fi
IS_TAG=0; [[ "$REF" == v[0-9]* ]] && IS_TAG=1
say "installing $REPO@$REF into $DIR"

# ---- 2 · kit tree --------------------------------------------------------------
git_auth() { # git with the token as an HTTP header (never written to .git/config)
    if [[ -n "$TOKEN" ]]; then
        git -c http.extraHeader="Authorization: Basic $(printf 'x-access-token:%s' "$TOKEN" | base64 | tr -d '\n')" "$@"
    else
        git "$@"
    fi
}
if [[ -d "$DIR/.git" ]]; then
    if [[ -n "$(git -C "$DIR" status --porcelain --untracked-files=no 2>/dev/null)" ]]; then
        warn "$DIR has local edits — leaving the tree as it is (binary + wrappers still update)"
    else
        say "updating the existing checkout"
        git_auth -C "$DIR" fetch --quiet --tags origin
        if (( IS_TAG )); then
            git -C "$DIR" -c advice.detachedHead=false checkout --quiet "$REF"
        else
            git -C "$DIR" checkout --quiet "$REF" && git_auth -C "$DIR" pull --quiet --ff-only origin "$REF"
        fi
    fi
elif [[ -e "$DIR" && -n "$(ls -A "$DIR" 2>/dev/null)" ]]; then
    if [[ -f "$DIR/install.sh" && -f "$DIR/hotbed/go.mod" ]]; then
        say "$DIR holds a kit tree without git — refreshing it from the $REF tarball"
        FROM_TARBALL=1
    else
        fail "$DIR exists and is not a Hotbed tree — pick another --dir"
    fi
elif command -v git >/dev/null 2>&1; then
    say "cloning (shallow)"
    git_auth clone --quiet --depth 1 --branch "$REF" "https://github.com/$REPO.git" "$DIR"
else
    FROM_TARBALL=1
fi
if [[ "${FROM_TARBALL:-0}" -eq 1 ]]; then
    say "downloading the $REF source tarball"
    mkdir -p "$DIR"
    api "/tarball/$REF" | tar -xz -C "$DIR" --strip-components=1 \
        || fail "tarball download failed (private repo? export GITHUB_TOKEN)"
fi
[[ -f "$DIR/install.sh" && -f "$DIR/hotbed/go.mod" ]] || fail "fetched tree looks incomplete: $DIR"

# ---- 3 · daemon binary ----------------------------------------------------------
BIN="$DIR/hotbed/bin/hotbedd"
mkdir -p "$DIR/hotbed/bin" "$DIR/dist"
have_go() { command -v go >/dev/null 2>&1 || [[ -x /usr/local/go/bin/go ]]; }
build_src() {
    local go=go; command -v go >/dev/null 2>&1 || go=/usr/local/go/bin/go
    say "building hotbedd from source ($("$go" version | awk '{print $3}'))"
    ( cd "$DIR/hotbed" && CGO_ENABLED=0 "$go" build -trimpath \
        -ldflags "-X hotbed/internal/env.Version=${REF#v}" -o bin/hotbedd ./cmd/hotbedd )
}
fetch_asset() { # fetch_asset NAME DEST — release asset via the API (works public + private)
    local id
    # one line per JSON object; the asset object carries "url" before its
    # nested "uploader" object, so the name's line holds the asset API url
    id="$(printf '%s' "$REL_JSON" | tr -d '\n' | tr '{' '\n' | grep -F "\"name\": \"$1\"" | json_field url | sed 's#.*/assets/##')"
    [[ -n "$id" ]] || return 1
    curl -fsSL ${TOKEN:+-H "Authorization: Bearer $TOKEN"} -H "Accept: application/octet-stream" \
        -o "$2" "https://api.github.com/repos/$REPO/releases/assets/$id"
}
installed=0
if (( !BUILD && IS_TAG )) && [[ -n "$ARCH" && -n "$REL_JSON" ]]; then
    asset="hotbedd-$REF-linux-$ARCH"
    if fetch_asset "$asset" "$DIR/dist/$asset" && fetch_asset SHA256SUMS "$DIR/dist/SHA256SUMS"; then
        if ( cd "$DIR/dist" && sha256sum -c SHA256SUMS --ignore-missing --quiet ) 2>/dev/null; then
            install -m 755 "$DIR/dist/$asset" "$BIN"
            say "daemon binary: $asset (sha256 verified)"
            installed=1
        else
            rm -f "$DIR/dist/$asset"
            fail "checksum mismatch on $asset — refusing to install it"
        fi
    else
        warn "release asset $asset not downloadable — falling back to a source build"
    fi
fi
if (( !installed )); then
    if have_go; then
        build_src
    elif (( DEPS )); then
        rm -f "$BIN" # install.sh --with-deps installs Go, then builds the missing binary
        say "no Go toolchain yet — install.sh --with-deps installs it and builds the daemon"
    elif [[ -x "$BIN" ]]; then
        warn "could not refresh the daemon binary (no release asset, no Go) — keeping the existing one"
    else
        fail "no release binary for $REF and no Go toolchain — re-run with --deps, or pick a release with --ref vX.Y.Z"
    fi
fi

# ---- 4 · kit installer (wrappers, prereq gate, base image) ------------------------
args=(--yes)
(( DEPS ))  && args+=(--with-deps)
(( VMIMG )) && args+=(--fetch-image)
say "running install.sh ${args[*]}"
bash "$DIR/install.sh" "${args[@]}" </dev/null
[[ -x "$BIN" ]] || fail "no daemon binary after install — see the output above"

# ---- 5 · daemon ------------------------------------------------------------------
LOCAL_BIN="$HOME/.local/bin"
running() { curl -fsS --max-time 2 "http://127.0.0.1:${HOTBEDD_PORT:-7770}/api/health" >/dev/null 2>&1; }
if (( SERVICE )); then
    if systemctl --user show-environment >/dev/null 2>&1; then
        unit="$HOME/.config/systemd/user/hotbedd.service"
        mkdir -p "$(dirname "$unit")"
        cat > "$unit" <<EOF
[Unit]
Description=Hotbed daemon (dashboard, API, LLM proxy)
After=network-online.target docker.service

[Service]
ExecStart=$LOCAL_BIN/hotbedd-serve
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
EOF
        systemctl --user daemon-reload
        if running && ! systemctl --user is-active --quiet hotbedd.service; then
            warn "a hotbedd already runs outside systemd — unit installed + enabled, NOT started"
            warn "  stop the old one, then: systemctl --user start hotbedd"
            systemctl --user enable --quiet hotbedd.service
        else
            systemctl --user enable --quiet hotbedd.service
            systemctl --user restart hotbedd.service
            say "systemd user unit hotbedd.service enabled + (re)started"
            for _ in 1 2 3 4 5 6 7 8 9 10; do running && break; sleep 1; done
        fi
        if command -v loginctl >/dev/null 2>&1 && \
           [[ "$(loginctl show-user "$USER" -p Linger --value 2>/dev/null)" != yes ]]; then
            say "tip: keep it running without an open session: sudo loginctl enable-linger $USER"
        fi
    else
        warn "--service: no systemd user session here (WSL without systemd=true in /etc/wsl.conf?)"
        warn "  start the daemon by hand instead (see below)"
    fi
fi

# ---- summary ----------------------------------------------------------------------
echo
if running; then
    say "daemon up → http://127.0.0.1:${HOTBEDD_PORT:-7770}"
else
    say "start the daemon:  hotbedd-serve        (or re-run with --service)"
fi
case ":$PATH:" in
    *":$LOCAL_BIN:"*) ;;
    *) say "add to your shell profile:  export PATH=\"$LOCAL_BIN:\$PATH\"" ;;
esac
say "commands: hbd (containers) · hotbedvm (VMs) · hotbedd (daemon CLI: ps, doctor, egress, …)"
say "LLM providers (optional): hotbedd config init   (imports ~/.config/opencode/opencode.json)"
say "update later: re-run this installer"
