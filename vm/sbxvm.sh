#!/usr/bin/env bash
# sbxvm — headless KVM-accelerated Debian VMs for the cases sandbox containers
# can't handle (systemd services, nested docker, kernel modules...).
#
# Cloud image + cloud-init bootstrap: SSH in with a dedicated key, your host
# share is mounted at /workspace via 9p, opencode is preinstalled for a
# first-run login inside the VM (its auth lives in the VM disk, survives
# across reboots).
#
# Two backends, selected with SBXVM_BACKEND (default: libvirt):
#   libvirt  — classic virt-install/virsh VM on the host's qemu          (below)
#   docker   — qemu inside a plain Docker container ("sbxvmd"), no libvirt;
#              one container per VM, lifecycle is 100% docker run/stop/rm
#
# Part of OpenCode-SBX.  See README.md.
set -euo pipefail

SBX_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VM_ROOT="${SBX_ROOT}/vm"
IMAGES_DIR="${VM_ROOT}/images"
VMS_DIR="${VM_ROOT}/vms"
MASTER_IMG="${IMAGES_DIR}/debian-13-genericcloud-amd64.qcow2"
MASTER_URL="https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-amd64.qcow2"
OCCODE_URL="https://github.com/sst/opencode/releases/download/v1.19.4/opencode-linux-x64.tar.gz"
SSH_KEY="${HOME}/.ssh/sbxvm_key"
VIRSH=(virsh --connect qemu:///system)
VIRT_INSTALL=(virt-install)
VERSION="0.1.0"

# --- backend selection --------------------------------------------------------
# SBXVM_BACKEND=docker  → qemu inside a container (image sbxvmd, one container
#                         per VM, store volume sbxvmd-store; see vm/sbxvmd/)
# SBXVM_BACKEND=libvirt → virt-install/virsh on the host (original path)
BACKEND="${SBXVM_BACKEND:-libvirt}"

die()   { printf '\e[31;1msbxvm: FAIL\e[0m %s\n' "$*" >&2; exit 1; }
warn()  { printf '\e[33;1msbxvm:\e[0m %s\n' "$*" >&2; }
info()  { printf '\e[36;1msbxvm:\e[0m %s\n' "$*" >&2; }
ok()    { printf '\e[32;1msbxvm:\e[0m %s\n' "$*" >&2; }

require_cmds() {
    local c missing=0
    if [[ $BACKEND == docker ]]; then
        for c in docker ssh; do
            command -v "$c" >/dev/null 2>&1 || { warn "missing: $c"; missing=1; }
        done
        [[ $missing -eq 0 ]] || die "SBXVM_BACKEND=docker needs the docker CLI (and ssh) on the host"
        return 0
    fi
    for c in virsh virt-install qemu-img; do
        command -v "$c" >/dev/null 2>&1 || { warn "missing: $c"; missing=1; }
    done
    # the libvirt socket spawns the daemon on demand (systemd socket activation);
    # give it a few chances if it just shut down (libvirtd --timeout)
    local i reachable=0
    for i in 1 2 3 4 5; do
        if "${VIRSH[@]}" list --all >/dev/null 2>&1; then reachable=1; break; fi
        sleep 2
    done
    if [[ $reachable -ne 1 ]]; then
        die "libvirt system daemon not reachable — start it with:
    sudo systemctl enable --now libvirtd"
    fi
    [[ $missing -eq 0 ]] || die "install the missing packages: virsh/virt-install/qemu-img are in 'libvirt-clients', 'virtinst', 'qemu-img'"
}

require_image() {
    [[ -f "$MASTER_IMG" ]] || {
        info "master image not present at $MASTER_IMG"
        die "run:  sbxvm fetch-image    (downloads ~700MB from cloud.debian.org)"
    }
}

ensure_ssh_key() {
    if [[ ! -f "$SSH_KEY" ]]; then
        mkdir -p "$(dirname "$SSH_KEY")"
        info "generating dedicated ssh key $SSH_KEY"
        ssh-keygen -t ed25519 -N "" -C "sbxvm" -f "$SSH_KEY" -q
    fi
}

domain() { printf 'sbxvm-%s' "$1"; }
vmdir()  { printf '%s/%s' "$VMS_DIR" "$1"; }

# =================== docker backend (sbxvmd: qemu-in-container) ==============
# One container = one VM; lifecycle is plain docker run/stop/rm; the storage
# lives in a named volume so the qcow2 files get native ext4 I/O (no Desktop
# bind-mount tax). See vm/sbxvmd/{Dockerfile,entry}.
SBXVMD_IMAGE="sbxvmd:latest"
SBXVMD_STORE="sbxvmd-store"

dcont()     { printf 'sbxvmd-%s' "$1"; }
dvm_state() { docker inspect -f '{{.State.Status}}' "$(dcont "$1")" 2>/dev/null || printf ''; }
dvm_port()  {
    local p
    p="$(docker port "$(dcont "$1")" 22 2>/dev/null | grep -oE '[0-9]+$' || :)"
    [[ -n "$p" ]] && printf '%s' "$p"
    return 0
}

dneed_kvm() {
    local out rc=0
    out="$(docker run --rm --device /dev/kvm "$SBXVMD_IMAGE" kvmok 2>&1)" || rc=1
    [[ $rc -eq 0 ]] || die "KVM-in-container is not usable: $out
SBXVM_BACKEND=docker requires nested KVM behind the docker engine; a TCG-only
engine would make VMs unusable (no fallback in the docker backend)."
}

# build the runtime image once + seed the store volume with the patched master
ensure_sbxvmd() {
    if ! docker image inspect "$SBXVMD_IMAGE" >/dev/null 2>&1; then
        info "building sbxvmd runtime image (one-time)"
        docker build -t "$SBXVMD_IMAGE" -f "$VM_ROOT/sbxvmd/Dockerfile" "$SBX_ROOT" \
            || die "sbxvmd image build failed"
    fi
    docker volume inspect "$SBXVMD_STORE" >/dev/null 2>&1 \
        || docker volume create "$SBXVMD_STORE" >/dev/null
    if ! docker run --rm -v "$SBXVMD_STORE:/store" "$SBXVMD_IMAGE" \
            test -f /store/master.qcow2 >/dev/null 2>&1; then
        require_image
        info "seeding the store volume with the patched master image (one-time)"
        docker run --rm -v "$SBXVMD_STORE:/store" -v "$IMAGES_DIR:/masterdir:ro" \
            "$SBXVMD_IMAGE" cp "/masterdir/$(basename "$MASTER_IMG")" /store/master.qcow2 \
            || die "copying the master image into the store volume failed"
    fi
}

# --------------------------------------------------------------- connection ----
# Backend-agnostic SSH plumbing. vm_conn_set NAME populates VT_HOST, VT_PORT
# (empty for libvirt; a published 127.0.0.1 port for docker) or returns 1 if
# the VM is not reachable yet. Callers compose their own option arrays.
vm_conn_set() {
    local t
    t="$(vm_ip "$1")" || return 1
    if [[ $BACKEND == docker ]]; then
        VT_HOST="127.0.0.1"; VT_PORT="${t#*:}"
    else
        VT_HOST="$t"; VT_PORT=""
    fi
    return 0
}

# one-shot BatchMode ssh for polling (wait_ssh / wait_ready). Requires a prior
# successful vm_conn_set in the same shell.
conn_raw() {
    local -a parr=()
    [[ -n "${VT_PORT:-}" ]] && parr=(-p "$VT_PORT")
    ssh -i "$SSH_KEY" -o BatchMode=yes -o ConnectTimeout=5 \
        -o StrictHostKeyChecking=accept-new "${parr[@]}" "dev@${VT_HOST}" "$@"
}


require_vm() {
    local name="$1"
    if [[ $BACKEND == docker ]]; then
        [[ -n "$(dvm_state "$name")" ]] || die "no VM named '$name' (see 'sbxvm ls')"
        return 0
    fi
    "${VIRSH[@]}" dominfo "$(domain "$name")" >/dev/null 2>&1 \
        || die "no VM named '$name' (see 'sbxvm ls')"
}

# guest IP via libvirt leases / qemu-agent-less probing
# (docker backend: not an IP but the published 127.0.0.1:<port> endpoint —
#  everything that says "dev@ip" works unchanged with it)
vm_ip() {
    local ip
    if [[ $BACKEND == docker ]]; then
        ip="$(dvm_port "$1")"
        [[ -n "$ip" ]] || return 1
        printf '127.0.0.1:%s' "$ip"
        return 0
    fi
    ip="$("${VIRSH[@]}" domifaddr "$(domain "$1")" --source lease 2>/dev/null \
        | awk '/ipv4/{sub(/\/.*/,"",$4); print $4; exit}')"
    [[ -n "$ip" ]] || return 1
    printf '%s' "$ip"
}

# notify_controller PHASE NAME: self-registration with the sbxd controller.
# Called at boot-begin and again once the guest is confirmed up; the daemon
# overlays an honest "booting" state between the two signals and restores
# proxy tunnels on boot-end. Strictly best-effort: a missing or unreachable
# controller never fails a boot (standalone sbxvm usage, foreign boots).
#
# Scheme is resolved ONCE per sbxvm.sh run: an SBXD_TLS=1 listener answers
# plain http with Go's "Client sent an HTTP request to an HTTPS server" 400 —
# which silently ate every registration and made fresh boots show an instant
# fake "running" instead of an honest "booting".
_SBXVM_CTL=""
notify_controller() {
    local phase="$1" name="$2"
    [[ -n "$name" ]] || return 0
    local port="${SBXD_PORT:-7770}"
    if [[ -z "$_SBXVM_CTL" ]]; then
        if curl -sk --max-time 2 "https://127.0.0.1:${port}/api/health" 2>/dev/null | grep -q '"version"'; then
            _SBXVM_CTL="https"
        else
            _SBXVM_CTL="http"
        fi
    fi
    curl -sk --max-time 2 -X POST "${_SBXVM_CTL}://127.0.0.1:${port}/api/vm/boot" \
        -H 'Content-Type: application/json' \
        -d "{\"name\":\"${name}\",\"phase\":\"${phase}\"}" >/dev/null 2>&1 || true
}

wait_ssh() {
    local name="$1" deadline=$2
    info "waiting for sshd on $name"
    for ((; SECONDS < deadline; )); do
        if vm_conn_set "$name" && conn_raw true >/dev/null 2>&1; then
            ok "ssh ready: dev@${VT_HOST}${VT_PORT:+:$VT_PORT} (after ~${SECONDS}s)"
            return 0
        fi
        sleep 3
    done
    if [[ $BACKEND == docker ]]; then
        die "ssh did not come up within ${deadline}s. Boot logs:
    docker logs sbxvmd-$name
    docker exec sbxvmd-$name sbxvmd-entry serial"
    fi
    die "ssh did not come up within ${deadline}s. Check boot logs:  sbxvm console $name"
}

# ensure_guest_opencode NAME: guests must not depend on their OWN internet
# egress for provisioning — filtered/half-blocked egress (ICMP+DNS pass,
# TCP 443 blackholed — seen behind policy routers/VPNs) left the seed unit's
# GitHub download spinning forever. The kit instead pushes the fallback
# binary over its OWN ssh channel right after cloud-init: the host already
# holds egress (it built the kit images from the same URL) and this channel
# is the exact one `sbxvm ssh` uses. The seeded unit stays as belt-and-
# suspenders for the cases where egress DOES work.
ensure_guest_opencode() {
    local name="$1"
    conn_raw "command -v opencode >/dev/null 2>&1" >/dev/null 2>&1 && return 0
    local tgz="/tmp/sbx-opencode-stage.tgz"
    curl -fsSL --retry 3 --connect-timeout 10 "$OCCODE_URL" -o "$tgz" \
        || { warn "opencode stage: host download failed — guest keeps its in-guest retrying unit"; rm -f "$tgz"; return 0; }
    info "pushing opencode into the guest over the kit ssh channel (no guest egress needed)"
    if cat "$tgz" | conn_raw "sudo sh -c 'cat > /tmp/opencode.tgz && tar -xz -C /usr/local/bin -f /tmp/opencode.tgz opencode && chmod 0755 /usr/local/bin/opencode && rm -f /tmp/opencode.tgz'" >/dev/null 2>&1 \
        && rm -f "$tgz" \
        && conn_raw "opencode --version" >/dev/null 2>&1; then
        ok "guest opencode staged: $(conn_raw 'opencode --version' 2>/dev/null | head -1)"
    else
        rm -f "$tgz"
        warn "opencode push failed — guest keeps its in-guest retrying unit"
    fi
}

# sync_guest_models NAME: wire the sbx proxy into the guest (mint key + write
# the opencode.json that lists the models + establish the ssh -R tunnel) by
# asking the local controller — exactly what the dashboard's sync button
# does for dashboard-created sandboxes. CLI-created VMs used to skip it
# entirely: opencode came up with an EMPTY model list. Best-effort + honest:
# no controller → say so; controller says no providers → name the cure.
sync_guest_models() {
    local name="$1" res
    [[ -n "$name" ]] || return 0
    local port="${SBXD_PORT:-7770}"
    if [[ -z "${_SBXVM_CTL:-}" ]]; then
        if curl -sk --max-time 2 "https://127.0.0.1:${port}/api/health" 2>/dev/null | grep -q '"version"'; then
            _SBXVM_CTL="https"
        else
            _SBXVM_CTL="http"
        fi
    fi
    res="$(curl -sk --max-time 90 -X POST "${_SBXVM_CTL}://127.0.0.1:${port}/api/sandboxes/vm/${name}/sync" \
        -H 'Content-Type: application/json' -d '{}' 2>/dev/null || true)"
    if [[ "$res" == *'"ok":true'* ]]; then
        ok "guest models wired to the sbx proxy ($(grep -o '"via" *: *"[^"]*"' <<<"$res" | head -1 | cut -d'"' -f4))"
    elif [[ -z "$res" ]]; then
        warn "no controller listening on ${port} — models stay unwired; run later: sbxd config init && sbxd sync ${name}"
    else
        warn "guest proxy sync failed — models need: sbxd config init && sbxd sync ${name}  (${res:0:110})"
    fi
}

# wait_ready NAME: poll cloud-init until provisioning is done; then, since the
# first boot runs the cloud-flavor kernel and packages just installed the full
# one, issue one reboot into it when needed; finally verify the workspace 9p
# mount where applicable.
wait_ready() {
    local name="$1" ci k
    info "waiting for cloud-init provisioning to finish (apt + kernel install...)"
    SECONDS=0
    while (( SECONDS < 720 )); do
        if vm_conn_set "$name" \
           && ci="$(conn_raw "cloud-init status 2>/dev/null | head -1" 2>/dev/null | awk '{print $2}')" \
           && [[ "$ci" == done || "$ci" == warning ]]; then
            break
        fi
        sleep 8
    done
    if [[ "${ci:-}" != done && "${ci:-}" != warning ]]; then
        warn "cloud-init still not done after ~720s — the VM may still be provisioning"
        return 0
    fi
    ok "cloud-init done (after ~${SECONDS}s)"
    k="$(conn_raw 'uname -r' 2>/dev/null || :)"
    if [[ "$k" == *cloud* ]]; then
        info "switching to the full kernel (one reboot into linux-image-amd64)"
        conn_raw 'sudo reboot' >/dev/null 2>&1 || true
        SECONDS=0; sleep 8
        wait_ssh "$name" 600 || true
    fi
    if vm_conn_set "$name" \
       && conn_raw 'mount | grep -q "hostshare on /workspace "' >/dev/null 2>&1; then
        ok "workspace share mounted at /workspace"
    fi
}

vm_ssh_cmd() {  # vm_ssh_cmd NAME [args...] — ssh into dev@
    local name="$1"; shift
    vm_conn_set "$name" || die "no connection for $name — is it running? (sbxvm ls)"
    mkdir -p /tmp/opencode
    printf '\e[36;1msbxvm:\e[0m %s\n' "ssh dev@${VT_HOST}${VT_PORT:+:$VT_PORT}" >&2
    local -a parr=()
    [[ -n "${VT_PORT:-}" ]] && parr=(-p "$VT_PORT")
    exec ssh "${parr[@]}" -i "$SSH_KEY" -o StrictHostKeyChecking=accept-new \
        -o ServerAliveInterval=30 \
        -o ControlMaster=auto -o ControlPath=/tmp/opencode/sbxvm-ssh-%r@%h:%p -o ControlPersist=10 \
        "dev@${VT_HOST}" "$@"
}

# ______________________________________________ write cloud-init user data ___
seed_userdata() {
    cat <<EOF
#cloud-config
hostname: "${1}"
users:
  - name: dev
    sudo: "ALL=(ALL) NOPASSWD:ALL"
    shell: /bin/bash
    lock_passwd: true
    ssh_authorized_keys:
      - $(cat "${SSH_KEY}.pub")
ssh_pwauth: false
package_update: true
packages:
  - docker.io
  - build-essential
  - git
  - curl
  - wget
  - jq
  - less
  - unzip
  - ripgrep
  - python3-pip
  - python3-venv
  - pipx
  - ca-certificates
  - openssh-server
  # the cloud-amd64 kernel lacks 9p modules — full generic kernel has them
  - linux-image-amd64
write_files:
  - path: /etc/profile.d/sbxvm-path.sh
    permissions: '0755'
    content: |
      #!/bin/sh
      export GOPATH="\$HOME/go"
      export PATH="\$GOPATH/bin:\$PATH"
  # docker daemon config. CRITICAL (docker backend): with the default bip
  # 172.17.0.1/16 the guest's docker0 SUBNET COLLIDES with the host-side
  # docker bridge (Docker Desktop's publisher connects from 172.17.0.1) — the
  # guest starts believing 172.17.x.x is local and inbound TCP to :22 dies
  # (SYN-ACKs route into the link-down docker0). Hooks via 9p/SSH die.
  - path: /etc/docker/daemon.json
    permissions: '0644'
    content: |
      {
        "bip": "172.28.0.1/16",
        "default-address-pools": [
          { "base": "172.28.0.0/12", "size": 24 }
        ]
      }
  - path: /etc/systemd/system/workspace.mount
    permissions: '0644'
    content: |
      [Unit]
      Description=9p host workspace share (sbxvm)
      [Mount]
      What=hostshare
      Where=/workspace
      Type=9p
      Options=trans=virtio,version=9p2000.L,msize=104857600
      [Install]
      WantedBy=multi-user.target
  - path: /etc/systemd/system/sbx-opencode-install.service
    permissions: '0644'
    content: |
      # self-heal: runs at every boot and, since systemd restarts the failed
      # oneshot every minute (StartLimitIntervalSec=0), also CONTINUES inside
      # a single boot until the binary exists. A transient TLS miss at boot
      # used to leave guests without opencode until the next reboot.
      [Unit]
      Description=sbxvm: ensure the opencode binary is installed
      Wants=network-online.target
      After=network-online.target
      StartLimitIntervalSec=0
      [Service]
      Type=oneshot
      RemainAfterExit=yes
      Restart=on-failure
      RestartSec=60
      ExecStart=/bin/sh -c 'test -x /usr/local/bin/opencode || { i=0; while [ "\$i" -lt 5 ]; do curl -fsSL ${OCCODE_URL} -o /tmp/opencode.tgz && tar -xz -C /usr/local/bin -f /tmp/opencode.tgz opencode && chmod 0755 /usr/local/bin/opencode && rm -f /tmp/opencode.tgz && exit 0; i=\$(( i + 1 )); sleep 5; done; exit 1; }'
      [Install]
      WantedBy=multi-user.target
# NOTE: no cloud-init "mounts" module — it writes fstab and fails the whole
# first boot while the (pre-installed) kernel lacks 9p. The .mount unit above
# activates on any boot (with the full kernel installed by packages:).
runcmd:
  - mkdir -p /workspace /home/dev/go
  - usermod -aG docker dev
  - systemctl enable docker$([[ -n "${2:-}" ]] && printf ' workspace.mount')
  - systemctl enable sbx-opencode-install.service
  # make the full kernel the (only) GRUB default: both flavors may share the
  # same version — grub would keep the cloud one otherwise
  - [ "sh", "-c", 'PKGS=\$(dpkg -l | awk ''/linux-image-[0-9].*cloud-amd64 / {print \$2}''); [ -n "\$PKGS" ] && DEBIAN_FRONTEND=noninteractive apt-get purge -y \$PKGS' ]
EOF
}

patch_master() {
    # (1) strip load_video calls from /boot/grub/grub.cfg — the grub
    #     video-mode-set hard-resets the machine under nested KVM
    #     (WSL2/Hyper-V + QEMU >= 11). verified guest boots to login after.
    # (2) set GRUB_GFXPAYLOAD_LINUX=text in /etc/default/grub so *later*
    #     update-grub runs (e.g. kernel upgrades inside the guest) do not
    #     re-introduce load_video.
    local img="$1" marker="${1}.sbxvm-patched"
    [[ -f "$marker" ]] && return 0
    command -v debugfs >/dev/null 2>&1 || die "debugfs (e2fsprogs) missing"
    command -v fdisk >/dev/null 2>&1 || die "fdisk (util-linux) missing"
    local tmp start
    tmp="${img}.patchwork.$$"
    mkdir -p "$tmp"
    qemu-img convert -O raw "$img" "$tmp/master.raw"
    start="$(fdisk -l "$tmp/master.raw" 2>/dev/null | awk '/Linux root/ {print $2; exit}')"
    [[ -n "$start" ]] || { rm -rf "$tmp"; warn "could not locate rootfs partition — skipping grub patch"; return 1; }
    dd if="$tmp/master.raw" of="$tmp/root.ext4" bs=512 skip="$start" status=none
    # (1) grub.cfg
    debugfs -R "dump /boot/grub/grub.cfg $tmp/grub.cfg" "$tmp/root.ext4" 2>/dev/null
    sed 's/^[[:space:]]*load_video[[:space:]]*$/\t# load_video disabled by sbxvm (nested-KVM video-mode-set resets machine)/' \
        "$tmp/grub.cfg" > "$tmp/grub-new.cfg"
    debugfs -w -R "rm /boot/grub/grub.cfg" "$tmp/root.ext4" >/dev/null 2>&1
    debugfs -w -R "write $tmp/grub-new.cfg /boot/grub/grub.cfg" "$tmp/root.ext4" 2>/dev/null | tail -1 >/dev/null
    # (2) /etc/default/grub — keep update-grub from re-introducing the
    #     load_video video-mode-set: text payload + no gfxterm terminal
    #     (the 00_header gfxterm block re-emits load_video unless gfxterm
    #     is dropped from GRUB_TERMINAL_OUTPUT)
    debugfs -R "dump /etc/default/grub $tmp/defgrub" "$tmp/root.ext4" 2>/dev/null
    { grep -v '^GRUB_GFXPAYLOAD_LINUX=' "$tmp/defgrub" 2>/dev/null || echo "GRUB_CMDLINE_LINUX_DEFAULT=\"\""; } > "$tmp/defgrub-new"
    printf 'GRUB_GFXPAYLOAD_LINUX=text\n' >> "$tmp/defgrub-new"
    sed -i 's/^GRUB_TERMINAL_OUTPUT=.*/GRUB_TERMINAL_OUTPUT="console serial"/' "$tmp/defgrub-new"
    debugfs -w -R "rm /etc/default/grub" "$tmp/root.ext4" >/dev/null 2>&1
    debugfs -w -R "write $tmp/defgrub-new /etc/default/grub" "$tmp/root.ext4" 2>/dev/null | tail -1 >/dev/null
    cp "$tmp/master.raw" "$tmp/master-patched.raw"
    dd if="$tmp/root.ext4" of="$tmp/master-patched.raw" bs=512 seek="$start" conv=notrunc status=none
    qemu-img convert -O qcow2 "$tmp/master-patched.raw" "$img"
    touch "$marker"
    rm -rf "$tmp"
    chmod 0644 "$img" "$marker"
    ok "master image patched (load_video disabled, GRUB_GFXPAYLOAD_LINUX=text)"
}

# ______________________________________________________ commands _____________
fetch_image() {
    require_cmds
    mkdir -p "$IMAGES_DIR"
    info "downloading Debian 13 genericcloud master image (~700MB)"
    curl -fSL --retry 3 "$MASTER_URL" -o "${MASTER_IMG}.tmp" \
        && mv "${MASTER_IMG}.tmp" "$MASTER_IMG"
    patch_master "$MASTER_IMG"
    qemu-img info "$MASTER_IMG" | head -3
    ok "image ready: $MASTER_IMG"
}

# ------------------------------------------------------------- docker create ---
dcreate() {
    ensure_ssh_key

    local name="" cpu=4 mem=6144 disk=20 share="" a
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --cpu)  cpu="$2";  shift 2 ;;
            --mem)  mem="$2";  shift 2 ;;
            --disk) disk="$2"; shift 2 ;;
            --share) share="$2"; shift 2 ;;
            *) name="$1"; shift ;;
        esac
    done
    [[ -n "$name" ]] || die "usage: sbxvm create NAME [--cpu N] [--mem MB] [--disk GB] --share /path"
    if [[ "$name" =~ ^[a-zA-Z][a-zA-Z0-9._-]*$ ]]; then :; else die "bad name '$name'"; fi
    [[ -n "$(dvm_state "$name")" ]] && die "VM '$name' already exists (container sbxvmd-$name)"
    notify_controller begin "$name"

    if [[ -z "$share" ]]; then
        warn "--share not given — no host directory will be mounted (agent works in VM-local /workspace)"
        share=""
    elif [[ ! -d "$share" ]]; then
        die "--share path '$share' is not a directory"
    else
        share="$(readlink -f "$share")"
    fi

    ensure_sbxvmd
    dneed_kvm

    local dir; dir="$(vmdir "$name")"
    mkdir -p "$dir/seedwork"

    info "writing cloud-init seed (packed to seed.iso by the container itself)"
    local ud="$dir/user-data.yaml"
    seed_userdata "$name" "$( [[ -n "$share" ]] && echo share )" > "$ud"
    printf 'local-hostname: %s\n' "$name" > "$dir/meta-data.yaml"
    chmod 0644 "$ud" "$dir/meta-data.yaml"
    # cloud-init NoCloud requires file names EXACTLY "user-data" / "meta-data";
    # this dir stays and is bind-mounted read-only at /seed-src so the container
    # builds seed.iso itself (the host never needs xorriso on this path)
    cp "$ud" "$dir/seedwork/user-data"
    cp "$dir/meta-data.yaml" "$dir/seedwork/meta-data"

    info "starting container sbxvmd-$name"
    local -a run=(
        docker run -d --name "sbxvmd-$name"
        -e "SBXVMD_NAME=$name" -e "SBXVMD_CPU=$cpu" -e "SBXVMD_MEM=$mem" -e "SBXVMD_DISK=$disk"
        -v "$SBXVMD_STORE:/store"
        -v "$dir/seedwork:/seed-src:ro"
        -p 127.0.0.1::22
        --device /dev/kvm
        --restart unless-stopped
    )
    [[ -n "$share" ]] && run+=(-v "$share:/share")
    "${run[@]}" "$SBXVMD_IMAGE" boot >/dev/null \
        || die "docker run failed for sbxvmd-$name (details: docker logs sbxvmd-$name)"

    SECONDS=0
    if [[ "${SBXVM_SPAWN_ONLY:-0}" == "1" ]]; then
        # daemon-driven create: provisioning ends here; the post-boot
        # (ssh wait + opencode stage + sync) runs via `sbxvm postboot`
        # from the daemon's background goroutine — the create API must not
        # idle on the creation page until the machine is booted.
        return 0
    fi
    wait_ssh "$name" 900
    wait_ready "$name"
    ensure_guest_opencode "$name"
    sync_guest_models "$name"
    notify_controller end "$name"   # guest confirmed up → controller drops the "booting" state
}

# postboot NAME: the second half of a daemon-spawned create (the kit's own
# `create` runs everything when SBXVM_SPAWN_ONLY is unset). Blocks until the
# guest is confirmed up, stages opencode without guest egress, wires the
# proxy sync, then fires the controller's boot-end.
postboot() {
    require_cmds
    local name="$1"
    [[ -n "$name" ]] || die "usage: sbxvm postboot NAME"
    require_vm "$name"
    notify_controller begin "$name"   # daemon re-drive may start ahead of the spawn signal
    wait_ssh "$name" 900
    wait_ready "$name"
    ensure_guest_opencode "$name"
    sync_guest_models "$name"
    notify_controller end "$name"
}

create() {
    require_cmds
    if [[ $BACKEND == docker ]]; then
        dcreate "$@"; return 0
    fi
    require_image
    ensure_ssh_key

    local name="" cpu=4 mem=6144 disk=20 share="" a
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --cpu)  cpu="$2";  shift 2 ;;
            --mem)  mem="$2";  shift 2 ;;
            --disk) disk="$2"; shift 2 ;;
            --share) share="$2"; shift 2 ;;
            *) name="$1"; shift ;;
        esac
    done
    [[ -n "$name" ]] || die "usage: sbxvm create NAME [--cpu N] [--mem MB] --share /path"
    if [[ "$name" =~ ^[a-zA-Z][a-zA-Z0-9._-]*$ ]]; then :; else die "bad name '$name'"; fi
    if "${VIRSH[@]}" dominfo "$(domain "$name")" >/dev/null 2>&1; then die "VM '$name' already exists"; fi
    notify_controller begin "$name"

    if [[ -z "$share" ]]; then
        warn "--share not given — no host directory will be mounted (agent works in VM-local /workspace)"
        share=""
    elif [[ ! -d "$share" ]]; then
        die "--share path '$share' is not a directory"
    else
        share="$(readlink -f "$share")"
    fi

    # libvirt-qemu (uid/gid 64055 on this system) must be able to reach and
    # open the disks under $HOME — traverse bit on home + rw disks.
    local hm; hm="$(stat -c %a "$HOME")"
    if [[ "${hm: -1:1}" == "0" ]]; then
        chmod o+x "$HOME" && ok "granted o+x on \$HOME (libvirt-qemu needs the traverse bit)"
    fi
    local dir vm
    dir="$(vmdir "$name")"
    mkdir -p "$dir"
    chmod 0755 "$VM_ROOT" "$VMS_DIR" "$dir"

    info "creating overlay disk (${disk}G) on top of the master image"
    qemu-img create -f qcow2 \
        -o backing_file="$MASTER_IMG",backing_fmt=qcow2 \
        "$dir/disk.qcow2" "${disk}G" | tail -1
    chmod 0666 "$dir/disk.qcow2"

    info "writing cloud-init seed"
    local ud="$dir/user-data.yaml"
    seed_userdata "$name" "$( [[ -n "$share" ]] && echo share )" > "$ud"
    printf 'local-hostname: %s\n' "$name" > "$dir/meta-data.yaml"
    chmod 0644 "$ud" "$dir/meta-data.yaml"
    touch "$dir/serial.log" && chmod 0666 "$dir/serial.log"
    # cloud-init NoCloud requires file names EXACTLY "user-data" / "meta-data"
    local seedwork="$dir/seedwork"
    mkdir -p "$seedwork"
    cp "$ud" "$seedwork/user-data"
    cp "$dir/meta-data.yaml" "$seedwork/meta-data"
    info "packing seed.iso (cidata)"
    rm -f "$dir/seed.iso"
    xorriso -as mkisofs -o "$dir/seed.iso" \
        -volid cidata -joliet -rock "$seedwork" >/dev/null 2>&1 \
        || die "failed to build seed.iso (xorriso)"
    rm -rf "$seedwork"
    chmod 0644 "$dir/seed.iso"

    local args=(
        --import
        --connect qemu:///system
        --name "$(domain "$name")"
        --memory "$mem" --vcpus "$cpu"
        --disk "path=$dir/disk.qcow2,format=qcow2,bus=virtio"
        --disk "path=$dir/seed.iso,device=cdrom,bus=sata,readonly=on"
        --network network=default,model=virtio
        --graphics none
        ${SBXVM_SERIAL_FILE:+--serial "file,path=$dir/serial.log"}
        --console pty,target_type=serial
        --osinfo debian13
        --noautoconsole
        --check path_in_use=off
    )
    if [[ -n "$share" ]]; then
        # qemu runs unprivileged as this user (qemu.conf user=), so a plain
        # rwx check mirrors what the 9p server can really do; no interactive
        # prompts — vmsmoke and piped callers have no controlling TTY.
        if [[ ! -r "$share" || ! -x "$share" ]]; then
            warn "share '$share' is not traversable by $USER — 9p mount will likely fail"
        elif [[ ! -w "$share" ]]; then
            warn "share '$share' is not writable by $USER — guest writes to /workspace will fail"
        fi
        info "sharing host $share -> guest /workspace (9p)"
        args+=(--filesystem "type=mount,source=$share,target=hostshare")
    fi

    "${VIRT_INSTALL[@]}" "${args[@]}"
    SECONDS=0
    if [[ "${SBXVM_SPAWN_ONLY:-0}" == "1" ]]; then
        # daemon-driven create: provisioning ends here; the post-boot
        # (ssh wait + opencode stage + sync) runs via `sbxvm postboot`
        # from the daemon's background goroutine — same split as the
        # docker arm above.
        return 0
    fi
    wait_ssh "$name" 900
    wait_ready "$name"
    ensure_guest_opencode "$name"
    sync_guest_models "$name"
    notify_controller end "$name"   # guest confirmed up → controller drops the "booting" state
}

cmd_start() {
    require_cmds; require_vm "$1"
    notify_controller begin "$1"
    if [[ $BACKEND == docker ]]; then
        local stst; stst="$(dvm_state "$1")"
        if [[ "$stst" == "running" ]]; then ok "already running"; notify_controller end "$1"; return; fi
        docker start "$(dcont "$1")" >/dev/null && ok "started"
        SECONDS=0
        wait_ssh "$1" 600
        notify_controller end "$1"
        return 0
    fi
    local st; st="$("${VIRSH[@]}" domstate "$(domain "$1")" 2>/dev/null)"
    if [[ "$st" == "running" ]]; then ok "already running"; notify_controller end "$1"; return; fi
    "${VIRSH[@]}" start "$(domain "$1")" >/dev/null && ok "started"
    SECONDS=0
    wait_ssh "$1" 600
    notify_controller end "$1"
}
cmd_stop() {
    require_cmds; require_vm "$1"
    if [[ $BACKEND == docker ]]; then
        if [[ "$(dvm_state "$1")" != "running" ]]; then ok "already stopped"; return; fi
        info "ACPI shutdown queued (docker stop, 75s grace for the guest)"
        docker stop -t 75 "$(dcont "$1")" >/dev/null \
            && ok "stopped" || die "docker stop failed — see docker logs sbxvmd-$1"
        return 0
    fi
    "${VIRSH[@]}" shutdown "$(domain "$1")" >/dev/null
    info "ACPI shutdown queued: $(domain "$1")"
    local i; for i in $(seq 1 40); do
        [[ "$("${VIRSH[@]}" domstate "$(domain "$1")")" == "shut off" ]] \
            && { ok "stopped"; return; }
        sleep 3
    done
    warn "still not down after 120s — forcing"
    "${VIRSH[@]}" destroy "$(domain "$1")" >/dev/null && ok "stopped (forced)"
}
cmd_destroy() {
    require_cmds
    require_vm "$1"
    if [[ $BACKEND == docker ]]; then
        local a yes=0; for a in "$@"; do [[ "$a" == "--yes" ]] && yes=1; done
        if [[ $yes -ne 1 ]]; then
            if [[ ! -t 0 ]]; then die "non-interactive session: confirm with 'sbxvm destroy $1 --yes'"; fi
            read -r -p "sbxvm: fully delete VM '$1' INCLUDING snapshots and disk? [y/N] " a
            [[ "${a,,}" == y* ]] || { info "aborted"; return; }
        fi
        # rm -f kills the container (and releases the qcow2 file locks), then
        # the toolbox run wipes the VM's files in the shared store volume
        docker rm -f "$(dcont "$1")" >/dev/null 2>&1 || true
        docker run --rm -v "$SBXVMD_STORE:/store" "$SBXVMD_IMAGE" \
            rm -rf "/store/vms/$1" >/dev/null 2>&1 || true
        rm -rf "$(vmdir "$1")"
        ok "destroyed"
        return 0
    fi
    local a yes=0; for a in "$@"; do [[ "$a" == "--yes" ]] && yes=1; done
    if [[ $yes -ne 1 ]]; then
        if [[ ! -t 0 ]]; then die "non-interactive session: confirm with 'sbxvm destroy $1 --yes'"; fi
        read -r -p "sbxvm: fully delete VM '$1' INCLUDING snapshots and disk? [y/N] " a
        [[ "${a,,}" == y* ]] || { info "aborted"; return; }
    fi
    local d; d="$(domain "$1")"
    "${VIRSH[@]}" destroy "$d" >/dev/null 2>&1 || true
    # virsh undefine refuses to run while snapshots exist — drop them all first
    local sn
    while IFS= read -r sn; do
        [[ -n "$sn" ]] && "${VIRSH[@]}" snapshot-delete "$d" "$sn" >/dev/null 2>&1 || true
    done <<< "$("${VIRSH[@]}" snapshot-list "$d" --name 2>/dev/null)"
    "${VIRSH[@]}" undefine "$d" --nvram --remove-all-storage >/dev/null 2>&1 \
        || "${VIRSH[@]}" undefine "$d" --nvram >/dev/null 2>&1 || true
    rm -rf "$(vmdir "$1")"
    ok "destroyed"
}
cmd_ls() {
    require_cmds
    if [[ $BACKEND == docker ]]; then
        local c name st ip sn
        printf '%-28s %-10s %-15s %s\n' VM STATE IP SNAPSHOTS
        for c in $(docker ps -a --filter name=sbxvmd- --format '{{.Names}}'); do
            name="${c#sbxvmd-}"
            st="$(dvm_state "$name")"
            case "$st" in
                running|restarting|paused) : ;;
                *) st="shut off" ;;
            esac
            ip="-"
            [[ "$st" == running ]] && ip="$(vm_ip "$name" || true)"
            [[ -n "$ip" ]] || ip="-"
            sn=$(docker run --rm -v "$SBXVMD_STORE:/store" "$SBXVMD_IMAGE" \
                     qemu-img snapshot -l -U "/store/vms/$name/disk.qcow2" 2>/dev/null \
                     | awk 'NR>2 && $1 ~ /^[0-9]+$/ {n++} END {print n+0}') || sn=0
            printf '%-28s %-10s %-15s %s\n' "$name" "$st" "$ip" "$sn"
        done
        return 0
    fi
    local line name ip state snap
    printf '%-28s %-10s %-15s %s\n' VM STATE IP SNAPSHOTS
    for d in $("${VIRSH[@]}" list --all --name 2>/dev/null); do
        [[ "$d" == sbxvm-* ]] || continue
        name="${d#sbxvm-}"
        state="$("${VIRSH[@]}" domstate "$d" 2>/dev/null)"
        ip="$(vm_ip "$name" || true)"
        [[ -n "$ip" ]] || ip="-"
        snap="$("${VIRSH[@]}" snapshot-list "$d" --name 2>/dev/null | grep -c . || true)"
        printf '%-28s %-10s %-15s %s\n' "$name" "$state" "$ip" "$snap"
    done
}
# ------------------------------------------------------- docker snapshots ------
# Uniform offline snapshot policy (docker backend): the qcow2 file is locked by
# the running VM and QEMU VirtFS/mounted-share state capture is forbidden at
# the qemu level anyway, so every save/revert/delete round-trips a stop.
d_snap() {
    local name="$1" cmd="${2:-list}" tag="${3:-}"
    local st; st="$(dvm_state "$name")"
    local was_running=1
    [[ "$st" == "running" ]] && was_running=0
    local f="/store/vms/$name/disk.qcow2"
    local -a toolbox=(docker run --rm -v "$SBXVMD_STORE:/store" "$SBXVMD_IMAGE" qemu-img)
    case "$cmd" in
        save)
            [[ -n "$tag" ]] || tag="manual-$(date +%Y%m%d-%H%M%S)"
            if [[ $was_running -eq 0 ]]; then
                info "stopping sbxvmd-$name for the offline snapshot (RAM state is not captured)"
                docker stop -t 75 "$(dcont "$name")" >/dev/null
            fi
            "${toolbox[@]}" snapshot -c "$tag" "$f" >/dev/null 2>&1 \
                || die "qemu-img snapshot -c failed for tag '$tag'"
            ok "snapshot saved (offline): $tag"
            if [[ $was_running -eq 0 ]]; then
                info "restarting sbxvmd-$name"
                docker start "$(dcont "$name")" >/dev/null
                SECONDS=0
                wait_ssh "$name" 600
            fi
            ;;
        list)
            local out names
            if [[ $was_running -eq 0 ]]; then
                out="$("${toolbox[@]}" snapshot -l -U "$f" 2>/dev/null || :)"
            else
                out="$("${toolbox[@]}" snapshot -l "$f" 2>/dev/null || :)"
            fi
            names="$(printf '%s' "$out" | awk 'NR>2 && $1 ~ /^[0-9]+$/')"
            if [[ -z "${names// /}" ]]; then
                ok "no snapshots"
            else
                printf '%s\n' "$names"
            fi
            ;;
        revert)
            [[ -n "$tag" ]] || die "usage: sbxvm snapshot $name revert <name>"
            local have
            if [[ $was_running -eq 0 ]]; then
                have="$("${toolbox[@]}" snapshot -l -U "$f" 2>/dev/null | awk 'NR>2 && $1 ~ /^[0-9]+$/ {print $2}' | grep -xF "$tag")"
            else
                have="$("${toolbox[@]}" snapshot -l "$f" 2>/dev/null | awk 'NR>2 && $1 ~ /^[0-9]+$/ {print $2}' | grep -xF "$tag")"
            fi
            [[ -n "$have" ]] || die "snapshot '$tag' not found — see 'sbxvm snapshot $name list'"
            if [[ $was_running -eq 0 ]]; then
                info "stopping sbxvmd-$name (revert needs the qcow2 unlocked)"
                docker stop -t 75 "$(dcont "$name")" >/dev/null
            fi
            "${toolbox[@]}" snapshot -a "$tag" "$f" >/dev/null 2>&1 \
                || die "snapshot revert to '$tag' failed"
            ok "reverted to $tag (state: shut off; bring it up with: sbxvm start $name)"
            # intentionally left stopped — same semantics as the libvirt backend
            ;;
        delete)
            [[ -n "$tag" ]] || die "usage: sbxvm snapshot $name delete <name>"
            if [[ $was_running -eq 0 ]]; then
                info "stopping sbxvmd-$name (snapshot delete needs the qcow2 unlocked)"
                docker stop -t 75 "$(dcont "$name")" >/dev/null
            fi
            "${toolbox[@]}" snapshot -d "$tag" "$f" >/dev/null 2>&1 \
                || die "snapshot delete of '$tag' failed"
            ok "deleted $tag"
            if [[ $was_running -eq 0 ]]; then
                docker start "$(dcont "$name")" >/dev/null
                SECONDS=0
                wait_ssh "$name" 600
            fi
            ;;
        *) die "usage: sbxvm snapshot <name> [save|list|revert|delete] ..." ;;
    esac
}

snap() {
    require_cmds; require_vm "$1"
    if [[ $BACKEND == docker ]]; then
        d_snap "$@"; return 0
    fi
    local name="$1" cmd="${2:-list}" tag="${3:-}"
    local d; d="$(domain "$name")"
    local has9p; has9p="$("${VIRSH[@]}" dumpxml "$d" 2>/dev/null | grep -c "<filesystem")" || true
    case "$cmd" in
        save)
            [[ -n "$tag" ]] || tag="manual-$(date +%Y%m%d-%H%M%S)"
            local st i out
            st="$("${VIRSH[@]}" domstate "$d" 2>/dev/null)"
            if [[ "$st" == "running" && "$has9p" -gt 0 ]]; then
                # QEMU forbids live state capture while a 9p device is
                # attached — quiesce via ACPI, snapshot offline, restart.
                info "9p share attached: quick stop -> snapshot -> restart (RAM state is not captured)"
                "${VIRSH[@]}" shutdown "$d" >/dev/null 2>&1
                out=""
                for i in $(seq 1 24); do
                    out="$("${VIRSH[@]}" domstate "$d" 2>/dev/null)"
                    [[ "$out" == "shut off" ]] && break
                    sleep 5
                done
                [[ "$out" != "shut off" ]] && "${VIRSH[@]}" destroy "$d" >/dev/null 2>&1 || true
                "${VIRSH[@]}" snapshot-create-as "$d" --name "$tag" --description "$tag (offline; 9p)" >/dev/null \
                    && ok "snapshot saved (offline): $tag"
                info "restarting $name"
                "${VIRSH[@]}" start "$d" >/dev/null
                wait_ssh "$name" 600
            else
                "${VIRSH[@]}" snapshot-create-as "$d" --name "$tag" --description "$tag" >/dev/null \
                    && ok "snapshot saved: $tag"
            fi
            ;;
        list)
            local names
            names="$("${VIRSH[@]}" snapshot-list "$d" --name 2>/dev/null)"
            if [[ -z "${names// /}" ]]; then
                ok "no snapshots"
            else
                while IFS= read -r n; do [[ -n "$n" ]] && printf '  %s\n' "$n"; done <<< "$names"
            fi
            ;;
        revert)
            [[ -n "$tag" ]] || die "usage: sbxvm snapshot $name revert <name>"
            local snapname="$("${VIRSH[@]}" snapshot-list "$d" --name 2>/dev/null | grep -xF "$tag" | head -1)"
            [[ -n "$snapname" ]] || die "snapshot '$tag' not found — see 'sbxvm snapshot $name list'"
            info "reverting $d to $snapname (RAM state is lost; the VM returns to the snapshot's own power state)"
            "${VIRSH[@]}" destroy "$d" >/dev/null 2>&1 || true
            "${VIRSH[@]}" snapshot-revert "$d" "$snapname" >/dev/null \
                && ok "reverted to $snapname (state: $("${VIRSH[@]}" domstate "$d"); bring it up with: sbxvm start $name)"
            ;;
        delete)
            [[ -n "$tag" ]] || die "usage: sbxvm snapshot $name delete <name>"
            "${VIRSH[@]}" snapshot-delete "$d" "$tag" >/dev/null && ok "deleted $tag"
            ;;
        *) die "usage: sbxvm snapshot <name> [save|list|revert|delete] ..." ;;
    esac
}
_run_vm() {  # _run_vm NAME command...
    local name="$1"; shift
    vm_conn_set "$name" || die "no connection for $name — is it running? (sbxvm ls)"
    mkdir -p /tmp/opencode
    local -a parr=()
    [[ -n "${VT_PORT:-}" ]] && parr=(-p "$VT_PORT")
    exec ssh "${parr[@]}" -i "$SSH_KEY" -o StrictHostKeyChecking=accept-new -tt \
        -o ControlMaster=auto -o ControlPath=/tmp/opencode/sbxvm-ssh-%r@%h:%p -o ControlPersist=10 \
        "dev@${VT_HOST}" "$@"
}
opencode() {
    require_cmds
    local name="$1"; shift
    require_vm "$name"
    [[ "${1:-}" == "--" ]] && shift
    [[ -t 0 && -t 1 ]] || warn "no TTY on this stream — opencode TUI needs a real terminal"
    local c="cd /workspace && /usr/local/bin/opencode"
    if [[ $# -gt 0 ]]; then c="$(printf '%s %q ' "$c" "$@")"; fi
    _run_vm "$name" "$c"
}
help() {
    cat <<EOF
sbxvm $VERSION — headless KVM VMs for AI-agent work (OpenCode-SBX phase 2)

The VMs are Debian 13 with docker + build toolchain + opencode preinstalled,
SSH via a dedicated key, host directory shared at /workspace via 9p.

USAGE: sbxvm COMMAND ...

  fetch-image                 download the Debian cloud master image (once)
  create NAME [opts]          create + boot a VM (––cpu N ––mem MB ––disk GB ––share DIR)
  postboot NAME               finish a daemon-spawned create (wait ssh + stage opencode + sync)
  ssh NAME [-] command        ssh as dev@ (interactive shell if no command)
  shell NAME                  alias of ssh NAME
  opencode NAME [--] [args]   run opencode TUI inside the VM (cwd /workspace)
  ip NAME                     print the VM's current IP
  start | stop | NAME         power control
  destroy NAME [--yes]        delete the VM and its disks
  ls                          list sbxvm VMs (+IPs +snapshot counts)
  snapshot NAME save|list|revert|delete
  console NAME                attach to the serial console (boot debugging)
  help                        this help

Backend: SBXVM_BACKEND=libvirt (default)  — virt-install/virsh VMs on the host.
         SBXVM_BACKEND=docker    — each VM is a Docker container running qemu
         ("sbxvmd", image: sbxvmd:latest, store volume: sbxvmd-store). No
         libvirt used at all; snapshots are offline (stop -> snapshot -> start).

Typical flow:
  sbxvm fetch-image
  sbxvm create agent1 --share ~/projects/agent1    # one VM per workspace
  sbxvm snapshot agent1 save before-agent
  sbxvm opencode agent1                            # TUI inside the VM
  sbxvm snapshot agent1 revert before-agent        # roll back everything the agent did
EOF
}

# ---- dispatch ---------------------------------------------------------------
SUBCMD="${1:-help}"; shift || true

case "$SUBCMD" in
    fetch-image) require_cmds; fetch_image ;;
    create)      require_cmds; create "$@" ;;
    postboot)    require_cmds; postboot "$@" ;;
    start)       require_cmds; cmd_start "$@" ;;
    stop)        require_cmds; cmd_stop "$@" ;;
    destroy)     require_cmds; cmd_destroy "$@" ;;
    ssh|shell)
        require_cmds; require_vm "$1"
        name="$1"; shift
        [[ "${1:-}" == "--" ]] && shift
        if [[ $# -eq 0 ]]; then
            vm_ssh_cmd "$name"
        else
            vm_ssh_cmd "$name" "$@"
        fi
        ;;
    opencode)    require_cmds; opencode "$@" ;;
    ip)          require_cmds; require_vm "$1"; shift; vm_ip "$1" ;;
    ls)          cmd_ls ;;
    snapshot)    require_cmds; snap "$@" ;;
    console)
        require_cmds; require_vm "$1"
        if [[ $BACKEND == docker ]]; then
            exec docker exec -i "$(dcont "$1")" sbxvmd-entry serial
        fi
        exec "${VIRSH[@]}" console "$(domain "$1")" ;;
    help|-h|--help) help ;;
    version|-V)  printf 'sbxvm %s (backend: %s)\n' "$VERSION" "$BACKEND" ;;
    *)           help >&2; die "unknown command '$command'" ;;
esac
