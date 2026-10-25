#!/bin/bash
# sbx-entry — entrypoint for OpenCode-SBX sandboxes.
#
# Runs as root at container start and prepares the environment for the
# runtime user (SBX_UID/SBX_GID, normally the host user's ids so that files
# created in bind-mounted dirs have sane host ownership):
#
#   1. ensure a passwd/group entry exists for the runtime uid/gid
#   2. grant passwordless sudo inside the sandbox (it is disposable)
#   3. fix ownership of $HOME and cache volumes (fast check, only if stale)
#   4. drop privileges and exec the container command
#
# With no command given it sleeps forever (persistent sandbox mode);
# with e.g. `bash` it execs an interactive shell (ephemeral --fresh mode).
set -u

SBX_UID="${SBX_UID:-1000}"
SBX_GID="${SBX_GID:-1000}"
HOME_DIR="/home/sbx"

log() { printf '[sbx-init] %s\n' "$*" >&2; }

# --- 1. user/group existence ------------------------------------------------
GROUP_NAME="$(getent group "$SBX_GID" | cut -d: -f1)"
if [ -z "$GROUP_NAME" ]; then
    GROUP_NAME="sbx-g$SBX_GID"
    groupadd -g "$SBX_GID" "$GROUP_NAME"
    log "created group $GROUP_NAME ($SBX_GID)"
fi

USER_NAME="$(getent passwd "$SBX_UID" | cut -d: -f1)"
if [ -z "$USER_NAME" ]; then
    USER_NAME="sbx-u$SBX_UID"
    useradd -M -d "$HOME_DIR" -g "$GROUP_NAME" -u "$SBX_UID" -s /bin/bash "$USER_NAME"
    log "created user $USER_NAME ($SBX_UID)"
fi

# --- 2. passwordless sudo (container-local only) ----------------------------
if [ "$SBX_UID" != "0" ]; then
    SUDOERS="/etc/sudoers.d/sbx-u$SBX_UID"
    if [ ! -f "$SUDOERS" ]; then
        printf '%s ALL=(ALL) NOPASSWD:ALL\n' "$USER_NAME" > "$SUDOERS"
        chmod 0440 "$SUDOERS"
    fi
fi

# --- 3. ownership fixups (skip recursion when already correct) --------------
own_path() {
    local p="$1" stale
    [ -e "$p" ] || return 0
    stale="$(find "$p" \! \( -user "$SBX_UID" -a -group "$SBX_GID" \) -print -quit 2>/dev/null)"
    if [ -n "$stale" ]; then
        chown -R "$SBX_UID:$SBX_GID" "$p" 2>/dev/null || true
        log "fixed ownership on $p"
    fi
}
own_path "$HOME_DIR"
own_path /usr/local/cargo/registry

# --- 4. drop privileges and exec --------------------------------------------
export HOME="$HOME_DIR"
export USER="$USER_NAME"
export LOGNAME="$USER_NAME"

if [ "$#" -eq 0 ]; then
    exec setpriv --reuid="$SBX_UID" --regid="$SBX_GID" --init-groups sleep infinity
else
    exec setpriv --reuid="$SBX_UID" --regid="$SBX_GID" --init-groups "$@"
fi
