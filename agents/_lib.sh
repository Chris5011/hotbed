# agents/_lib.sh — shared helpers, sourced in front of every module's
# install.sh. Runs as root inside the sandbox container (bash).
#
# Environment: HOTBED_AGENT (module id), HOTBED_HOME (sandbox user's home).
# Convention: tools go to /opt/hotbed-agents/<id>, entry points to
# /usr/local/bin — everything dies with the container (that is the point).
set -euo pipefail

AGENTS_OPT=/opt/hotbed-agents
mkdir -p "$AGENTS_OPT"

log() { printf '[%s] %s\n' "${HOTBED_AGENT:-agent}" "$*"; }

# as_user CMD... — run as the sandbox's login user (owner of HOTBED_HOME),
# for tools that install per user (~/.something) rather than system-wide
as_user() {
    local u
    u="$(stat -c %U "$HOTBED_HOME")"
    runuser -u "$u" -- env HOME="$HOTBED_HOME" PATH="$HOTBED_HOME/.local/bin:$PATH" "$@"
}

# ensure_node [MIN] — Node.js >= MIN (default 20) on PATH. Reuses a
# matching node, else unpacks the newest official release of line
# max(MIN, 22) (stack images other than node lack Node entirely).
ensure_node() {
    local min="${1:-20}"
    if command -v node >/dev/null 2>&1 && command -v npm >/dev/null 2>&1; then
        local major
        major="$(node -p 'process.versions.node.split(".")[0]')"
        if (( major >= min )); then return 0; fi
    fi
    local arch v line=$(( min > 22 ? min : 22 ))
    case "$(uname -m)" in
        x86_64)  arch=x64 ;;
        aarch64) arch=arm64 ;;
        *) log "no Node.js build for $(uname -m)"; return 1 ;;
    esac
    v="$(curl -fsSL https://nodejs.org/dist/index.json \
        | jq -r --arg l "v$line." '[.[] | select(.version | startswith($l))][0].version')"
    [[ "$v" == "v$line."* ]] || { log "could not resolve the Node.js $line release"; return 1; }
    log "installing Node.js $v"
    rm -rf "$AGENTS_OPT/node"
    mkdir -p "$AGENTS_OPT/node"
    curl -fsSL "https://nodejs.org/dist/$v/node-$v-linux-$arch.tar.xz" \
        | tar -xJ -C "$AGENTS_OPT/node" --strip-components=1
    for b in node npm npx; do ln -sf "$AGENTS_OPT/node/bin/$b" "/usr/local/bin/$b"; done
}

# npm_global PKG... — global npm install with entry points in /usr/local/bin
npm_global() {
    ensure_node
    log "npm install -g $*"
    npm install -g --prefix /usr/local --no-fund --no-audit --loglevel=error "$@"
}

# ensure_uv — astral's uv (Python tool installer) in /usr/local/bin
ensure_uv() {
    command -v uv >/dev/null 2>&1 && return 0
    log "installing uv"
    curl -fsSL https://astral.sh/uv/install.sh \
        | env UV_INSTALL_DIR=/usr/local/bin INSTALLER_NO_MODIFY_PATH=1 sh >/dev/null
}
