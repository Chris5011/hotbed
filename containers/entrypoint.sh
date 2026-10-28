#!/bin/bash
# hotbed-entry — entrypoint for hotbed sandboxes.
#
# Runs as root at container start and prepares the environment for the
# runtime user (HBD_UID/HBD_GID, normally the host user's ids so that files
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

HBD_UID="${HBD_UID:-1000}"
HBD_GID="${HBD_GID:-1000}"
HOME_DIR="/home/hbd"

log() { printf '[hotbed-init] %s\n' "$*" >&2; }

# --- 1. user/group existence ------------------------------------------------
GROUP_NAME="$(getent group "$HBD_GID" | cut -d: -f1)"
if [ -z "$GROUP_NAME" ]; then
    GROUP_NAME="hotbed-g$HBD_GID"
    groupadd -g "$HBD_GID" "$GROUP_NAME"
    log "created group $GROUP_NAME ($HBD_GID)"
fi

USER_NAME="$(getent passwd "$HBD_UID" | cut -d: -f1)"
if [ -z "$USER_NAME" ]; then
    USER_NAME="hotbed-u$HBD_UID"
    useradd -M -d "$HOME_DIR" -g "$GROUP_NAME" -u "$HBD_UID" -s /bin/bash "$USER_NAME"
    log "created user $USER_NAME ($HBD_UID)"
fi

# --- 2. passwordless sudo (container-local only) ----------------------------
if [ "$HBD_UID" != "0" ]; then
    SUDOERS="/etc/sudoers.d/hotbed-u$HBD_UID"
    if [ ! -f "$SUDOERS" ]; then
        printf '%s ALL=(ALL) NOPASSWD:ALL\n' "$USER_NAME" > "$SUDOERS"
        chmod 0440 "$SUDOERS"
    fi
fi

# --- 3. ownership fixups (skip recursion when already correct) --------------
own_path() {
    local p="$1" stale
    [ -e "$p" ] || return 0
    stale="$(find "$p" \! \( -user "$HBD_UID" -a -group "$HBD_GID" \) -print -quit 2>/dev/null)"
    if [ -n "$stale" ]; then
        chown -R "$HBD_UID:$HBD_GID" "$p" 2>/dev/null || true
        log "fixed ownership on $p"
    fi
}
own_path "$HOME_DIR"
own_path /usr/local/cargo/registry
# no project bind-mounted (dashboard/recipe sandboxes without a mount): the
# image's root-owned /workspace must still be the user's to write into
if ! mountpoint -q /workspace 2>/dev/null; then
    own_path /workspace
fi

# --- 4. drop privileges and exec --------------------------------------------
export HOME="$HOME_DIR"
export USER="$USER_NAME"
export LOGNAME="$USER_NAME"

if [ "$#" -eq 0 ]; then
    exec setpriv --reuid="$HBD_UID" --regid="$HBD_GID" --init-groups sleep infinity
else
    exec setpriv --reuid="$HBD_UID" --regid="$HBD_GID" --init-groups "$@"
fi
