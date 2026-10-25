# OpenCode-SBX

Disposable sandboxed dev environments for **OpenCode** and other AI coding
agents — container-first, with a headless VM escape hatch — so that everything
an agent installs, downloads or breaks while it works stays off your main
machine.

```text
        ┌─────────────────────────────────────────────────────────────┐
 host ──│ ~/OpenCode-SBX (this kit)                                   │
        │                                                             │
        │  bin/sbx     throwaway Docker container per project dir     │
        │               project bind-mounted at /workspace            │
        │               toolchains die with the container             │
        │                                                             │
        │  bin/sbxvm    headless KVM VM (libvirt or docker)           │
        │               for what containers can't do: systemd,        │
        │               nested docker, kernel-level work              │
        │                                                             │
        │  containers/  image definitions (1 Dockerfile, 5 stacks)    │
        │  vm/          master image, per-VM disks, seeds             │
        │  tests/       container · VM · SBX Desktop test suites      │
        └─────────────────────────────────────────────────────────────┘
```

**Design rule of thumb:** if the work is "code, build, test, install dev
tools" → `sbx` (containers). If the work needs systemd, a real kernel,
nested Docker, or a full distro → `sbxvm`. Both keep your host clean.

---

## Table of contents

1. [Why](#why)
2. [The two layers at a glance](#the-two-layers-at-a-glance)
3. [Requirements](#requirements)
4. [Install: step by step](#install-step-by-step)
5. [One-time setup](#one-time-setup)
6. [sbx — container sandboxes](#sbx--container-sandboxes)
7. [sbxvm — the VM escape hatch](#sbxvm--the-vm-escape-hatch)
8. [The docker backend (sbxvmd)](#the-docker-backend-sbxvmd)
9. [sbxd — SBX Desktop (daemon, dashboard, docker-style CLI)](#sbxd--sbx-desktop-daemon-dashboard-docker-style-cli)
10. [How it works under the hood](#how-it-works-under-the-hood)
11. [Field debugging recipes](#field-debugging-recipes)
12. [Tests](#tests)
13. [File map](#file-map)
14. [Disk footprint & housekeeping](#disk-footprint--housekeeping)
15. [Security posture (honest)](#security-posture-honest)

---

## Why

AI agents happily `npm install -g`, `pip install`, `apt install`,
`go install`, `cargo install`… edit dotfiles, run servers, and generally paint
the town. Doing that on the machine you eat and sleep next to is a mess
waiting to happen. This kit gives every project a throwaway environment:

- **Per-project.** The project directory is bind-mounted at `/workspace` —
  that directory is the *blast radius*. Nothing else on the host is reachable.
- **Everything dies with the sandbox.** `sbx rm` / `sbxvm destroy` deletes
  the container/VM and every tool the agent installed. The host stays clean.
- **Fast iteration.** Shared package caches (npm/pip/go/cargo) live in named
  Docker volumes — wiping sandboxes is instant, reinstalling is cache-fast.
- **opencode works inside.** Your host opencode binary, config and auth are
  mounted in — the TUI works right away with your models and settings.

## The two layers at a glance

|                       | `sbx` (containers)                | `sbxvm` (VMs)                       |
|-----------------------|-----------------------------------|-------------------------------------|
| start/attach latency  | ~0.5 s                            | create ~2–3 min, then attach ~1 s   |
| blast radius          | one project bind mount            | one project share + the VM disk     |
| can do systemd        | no                                | yes                                 |
| can run Docker inside | no (well, no)                     | yes (nested docker)                 |
| kernel experiments    | no                                | yes (modules, sysctls, netstacks)   |
| snapshot / rollback   | no (*but* `rm` + cache volumes)   | yes, whole-machine, seconds         |
| resource cost idle    | ~0 (stopped container = none)     | RAM only while running              |
| durations             | daily driver                      | when containers can't do it         |

---

## Requirements

### Hosts

- Any Linux host works; on Windows run the kit inside **WSL2** — the notes
  below that mention Docker Desktop apply to any WSL2 distro.
- CPU with nested virtualization (`nested=Y` under
  `/sys/module/kvm_intel/parameters/nested`), `/dev/kvm` writable.
- RAM budget: ≥ 8 GB for the docker engine plus 2–4 GB per running VM.

### Container side (sbx)

- **A running docker engine.** Docker Desktop on Windows/WSL2: enable
  *Settings → Resources → WSL Integration* for your distro (so its paths are
  bind-mountable into containers).
- Engine memory ≥ 8 GB (`Settings → Resources → MemoryMiB`) for comfy builds.
- The `docker` CLI shim that ships with Desktop (`/usr/bin/docker`) must stay
  first in `$PATH` — it matches the engine version. Don't shadow it with a
  newer static CLI or commands start failing with odd `409`s.
- ~10 GB disk for images and shared caches.
- Optional but nice: host `opencode` at `~/.opencode/bin/opencode`
  (bind-mounted into sandboxes), `~/.gitconfig` and `~/.ssh` (mounted
  read-only for commits and git-over-ssh).

### VM side (sbxvm)

Packages (Debian-family names):

```bash
sudo apt install -y libvirt-clients virtinst qemu-system-x86 qemu-utils \
                    e2fsprogs xorriso
# virsh / virt-install / qemu-img live in the first three,
# debugfs (used by the image patcher) in e2fsprogs
```

Two one-time systemd/libvirt settings (once per host):

```bash
# 1. keep libvirtd resident (needed because spawns go through the unix socket)
sudo systemctl enable --now libvirtd

# 2. run qemu unprivileged as *your* user instead of the libvirt-qemu system
#    user — this is what makes host-directory binds and 9p shares Just Work
#    with normal ownership (use your own username):
sudo tee -a /etc/libvirt/qemu.conf >/dev/null <<EOF
user  = "$(id -un)"
group = "libvirt-qemu"
EOF
sudo systemctl restart libvirtd
```

`create` also checks and (if needed) sets the `o+x` traverse bit on `$HOME`
automatically, so libvirt can reach images under your home.

The [docker backend](#the-docker-backend-sbxvmd) skips everything above:
no libvirt packages, no qemu on the host, no xorriso — it needs only the
docker engine with nested KVM working behind it.

---

## Install: step by step

`install.sh` does the work; the steps below tell you exactly what it does
and what *you* need in place first. It is safe to re-run (idempotent) and
never touches existing sandboxes.

### 1 · Prerequisites

Required for the container side (and the daemon):

| you need | for | quick check |
|---|---|---|
| **bash, tmux, ssh, curl, jq** | kit CLIs, attach, tunnels, config | `command -v bash docker tmux ssh curl jq` |
| **a running docker engine** | containers + the docker VM backend | `docker ps` |
| ~10 GB disk | stack images, shared caches, master image | `df -h ~` |

Add this only for the **VM · libvirt** backend (the docker VM backend runs
without any of it):

```bash
sudo apt install -y libvirt-clients virtinst qemu-system-x86 qemu-utils \
                    e2fsprogs xorriso
```

VMs of either backend need **nested KVM** (a writable `/dev/kvm`) — see
[Hosts](#hosts) above.

The daemon itself has one of two routes (installer or binary):

- **Prebuilt binary** (no Go needed): take `sbxd-vX.Y.Z-linux-<arch>` +
  `SHA256SUMS` from the
  [Releases](https://github.com/Chris5011/OpenCode-SBX/releases) page
  (arch map: `x86_64` → `amd64`, `aarch64` → `arm64`), verify, and drop the
  binary anywhere in the kit tree — `install.sh` adopts it into
  `sbxd/bin/sbxd` itself (the explicit equivalent is
  `install -m755 sbxd-vX.Y.Z-linux-amd64 sbxd/bin/sbxd`):
  ```bash
  sha256sum -c SHA256SUMS --ignore-missing
  mv sbxd-vX.Y.Z-linux-amd64 dist/           # or the kit root — both work
  ```
  *What the binary is:* every SBX Desktop piece — the `sbxd` CLI, the REST
  + SSE controller and the embedded dashboard — in one static, stdlib-only
  Go file. Not included: the `sbx`/`sbxvm` CLIs, which are bash scripts
  living in the kit tree (see step 2). The daemon alone can inventory,
  start/stop, log and attach to everything that already exists and spawn
  new *containers* natively — but *creating VMs* and their journal
  fallback shell out to the kit's `vm/sbxvm.sh`, so you'll want the tree
  too.
- **Go 1.26+ toolchain**: `install.sh` (next steps) builds `sbxd` itself.

### 2 · Get the kit tree

```bash
git clone https://github.com/Chris5011/OpenCode-SBX.git
cd OpenCode-SBX
```

Cloning is just the easiest way to fetch: **nothing in the kit or the
daemon runs git at runtime** — any plain copy of the tree works, e.g. the
release page's *Source code (zip / tar.gz)* archive unpacks into exactly
this shape. In daily use you only need the directory; the git history is
for developing.

On a second (e.g. headless) machine, any of these fetches is fine:

```bash
# a) has the CLI tooling and auth →
gh release download vX.Y.Z -R Chris5011/OpenCode-SBX      # binary + SHA256SUMS
gh repo clone Chris5011/OpenCode-SBX                      # the tree

# b) minimal: raw curl against the release URLs — needs a token while the
#    repo is private (PAT with repo scope); token-free once public:
curl -fL -H "Authorization: Bearer $TOKEN" \
     -O https://github.com/Chris5011/OpenCode-SBX/releases/download/vX.Y.Z/sbxd-vX.Y.Z-linux-amd64

# c) the two boxes reach each other → skip GitHub entirely:
rsync -av --exclude vm/images --exclude vm/vms --exclude dist \
      --exclude sbxd/bin SRC_TREE/ newbox:apps/OpenCode-SBX/
```

Then `sha256sum -c SHA256SUMS --ignore-missing`, drop the binary into the
tree (`dist/` or the root — `install.sh` adopts either), and continue at
step 3.

### 3 · Run the installer

```bash
./install.sh            # interactive; add --yes to take all defaults
```

It will:

- symlink `sbx`, `sbxd` and a `sbxd-serve` wrapper (pins `SBX_ROOT` so the
  daemon always finds this tree) into `~/.local/bin`. That dir is on your
  PATH on Debian-family setups once it exists (they auto-add it on the next
  login shell; in an open terminal: `export PATH="$HOME/.local/bin:$PATH"`).
  The installer prints a note if it isn't on PATH; without it, run the
  binaries straight from the tree;
- gate-check the stack above and print exactly what's missing, split into
  required vs. optional;
- offer the one-time Debian master-image fetch (~330 MB download, ~950 MB
  on disk). Skipping is fine — retry any time with `sbxvm fetch-image`;
- with `--config-init`, import LLM providers from your host opencode
  config (do it later instead: `sbxd config init`).

### 4 · Start the daemon

```bash
sbxd-serve
# → dashboard + API on http://127.0.0.1:7770 (loopback only)
```

`sbxd-serve` is a plain foreground process — run it in a terminal, a tmux
pane or a systemd unit. The CLIs (`sbx`, `sbxvm`, `sbxd`) also work
without the daemon (they fall back to direct engine calls); the dashboard
obviously doesn't.

```bash
curl -s http://127.0.0.1:7770/api/health
# {"docker":true,"libvirt":true, ...}
```

### 5 · Verify + first sandbox

```bash
sbx doctor                # engine, bind mounts, images, opencode bits
sbxd ps                   # unified inventory (the dashboard shows the same, live)
cd some/project && sbx    # shell in — the stack image builds on first use
```

Point a browser at `http://127.0.0.1:7770`: the sidebar should list your
sandbox (and the `＋ New sandbox` wizard is right next to it).

### 6 · Optional: the accounting proxy

One command wires your providers into the token-accounting proxy, then
per-sandbox keys and $-wallets as needed — full walkthrough in
[P2](#p2--the-llm-proxy-wallets-and-one-global-opencode-config).

---

## One-time setup

The scripted [install above](#install-step-by-step) already does
`install-self` and can do the image fetch. If you instead operate the kit
manually, the three things it automates are:

```bash
# put sbx on your PATH (symlinks ~/.local/bin/sbx → this kit's bin/sbx)
sbx install-self

# check the container environment: engine, bind mounts, images, opencode bits
sbx doctor

# fetch + auto-patch the Debian cloud master image for sbxvm (one-time;
# ~326 MB download, ~950 MB on disk)
sbxvm fetch-image
```

After this you never need root again for daily work (qemu runs unprivileged
as you; every VM is per-user data).

---

## sbx — container sandboxes

### Mental model

`sbx` makes **one persistent, throwaway Docker container per project
directory**. The container name is derived from the project's absolute path
(`sbx-<stack>-<hash8>`), so re-running `sbx` anywhere in that project
reattaches to the *same* sandbox. Inside it you are a user with **your host
uid**, the project is mounted at `/workspace`, passwordless sudo works
(container-local!), and the internet is reachable — the workflow an agent
expects.

Why persistent: containers keep running detached (`sbx stop` to pause), so
the agent's state survives your terminal and reboots (restart policy
`unless-stopped`), while tooling it installs still dies with `sbx rm`.

### Quick start

```bash
cd ~/some/project
sbx                       # shell in (creates & builds the sandbox on first use)
sbx opencode              # opencode TUI inside the sandbox
sbx --fresh               # one-shot ephemeral sandbox instead; dies on exit
sbx status                # what exists for this project right now
sbx rm --yes              # nuke this project's sandbox and everything in it
```

From inside you (or an agent) can `apt install`, `pip install`, `npm i -g`,
compile, run servers — none of it lands on the host.

### Stacks

| stack    | image        | tooling you get                                 |
|----------|--------------|-------------------------------------------------|
| `node`   | `sbx:node`   | Node 22 LTS + npm + corepack                    |
| `python` | `sbx:python` | python3-dev, pipx, uv                           |
| `go`     | `sbx:go`     | latest stable Go + module cache volume          |
| `rust`   | `sbx:rust`   | rustup stable, minimal profile                  |
| `base`   | `sbx:base`   | fallback: git, build tools, python3             |

All stacks also get: git, curl/wget, jq, ripgrep, openssh-client, build
toolchain (gcc/make/pkg-config), python3+pip, sudo (passwordless *inside*
only), and a baked opencode fallback binary (an official release).

Auto detection (`sbx stack` to print; override with `--stack NAME`):

- `go.mod`, `go.work` → go
- `Cargo.toml` → rust
- `package.json` → node
- `pyproject.toml`, `requirements.txt`, `setup.py`, `setup.cfg`, `Pipfile`,
  `uv.lock` → python
- otherwise → base

Images build on first use and are cached afterwards. Rebuild explicitly:
`sbx build [stacks...]`, fully fresh: `sbx --no-cache build`.

### What's mounted in

| host path                        | sandbox path                | mode | purpose                        |
|----------------------------------|-----------------------------|------|--------------------------------|
| the project dir                  | `/workspace`                | rw   | the project (blast radius)     |
| `~/.local/share/opencode`        | `/home/sbx/.local/share/opencode` | rw | opencode state & auth (sessions, logins) |
| `~/.config/opencode`             | `/home/sbx/.config/opencode`| rw   | opencode config                |
| `~/.opencode/bin/opencode`       | `/usr/local/bin/opencode`   | ro   | *your* opencode binary wins over the baked one |
| `~/.gitconfig`                   | `/home/sbx/.gitconfig`      | ro   | commits carry your identity    |
| `~/.ssh`                         | `/home/sbx/.ssh`            | ro   | git push/pull over ssh         |
| named volume `sbx-npm`           | `/home/sbx/.npm`            | rw   | npm cache (node)               |
| named volume `sbx-pip`           | `/home/sbx/.cache/pip`      | rw   | pip/uv cache (python)          |
| named volume `sbx-go`            | `/home/sbx/go`              | rw   | Go cache (go)                  |
| named volume `sbx-cargo`         | `/usr/local/cargo`          | rw   | cargo cache (rust)             |

Opt out: `--no-auth`, `--no-occonf`, `--no-git`, `--no-ssh`. Add more:
`--mount HOST:CT[:ro]` and `--env KEY=VAL` (repeatable, must come before the
command, e.g. `sbx --mount /data:/data shell`).

Helpers for the strict case: `sbx --fresh --no-auth --no-ssh --net none`.

### Command reference

```text
sbx (no args) | shell     interactive shell in this project's sandbox
sbx opencode [-- args]    opencode TUI inside the sandbox
sbx exec [--] cmd...      one-shot command in the sandbox (tty-aware)
sbx root [--] cmd...      as root inside the sandbox (apt-get update ...)
sbx status | stop | rm    manage THIS project's sandbox (--yes skips prompt)
sbx ls | ps               all sbx sandboxes (stack, state, project)
sbx prune [--yes]         remove all sbx containers (asks, or --yes)
sbx caches [ls|nuke]      list / wipe sbx-* cache volumes (--yes)
sbx build [stacks...]     (re)build images; sbx --no-cache build to be sure
sbx stack                 print the auto-detected stack for this cwd
sbx doctor                environment diagnosis
sbx --fresh [cmd...]      ephemeral mode: --rm container, dies on exit
sbx install-self          symlink into ~/.local/bin
sbx version | help        meta
```

Global options (place **before** the command): `--stack NAME`, `--fresh`,
`--net MODE`, `--no-auth`, `--no-occonf`, `--no-git`, `--no-ssh`,
`--mount HOST:CT[:ro]`, `--env KEY=VAL`, `--yes`, `--no-cache`.

---

## sbxvm — the VM escape hatch

Some things a container can't do well: systemd services, nested Docker,
kernel modules, anything needing "a real machine". `sbxvm` creates headless
**KVM-accelerated Debian 13 VMs** through libvirt/virt-install, provisioned
tail-to-back by cloud-init. The same CLI also runs a second backend with
**no libvirt at all** — plain Docker containers with qemu inside — see
[the docker backend](#the-docker-backend-sbxvmd).

### What you get

- SSH in as user `dev` with a dedicated auto-generated key
  (`~/.ssh/sbxvm_key`), **passwordless sudo**, docker group membership.
- Preinstalled via cloud-init: `docker.io`, `build-essential`, git, jq,
  ripgrep, pipx/venv, openssh-server and the official opencode release
  at `/usr/local/bin/opencode`.
- Your chosen host directory shared at `/workspace` via virtio 9p — files the
  VM writes appear host-owned (`you`:`libvirt-qemu` mapping), and vice versa.
- A full private distro: it has network (NAT), its own kernel (full
  `linux-image-amd64`, so 9p *and* your kernel experiments both work), and
  its own libvirt network lease (`192.168.122.0/24`).

### Quick start

```bash
sbxvm create agent1 --cpu 2 --mem 4096 --disk 20 --share ~/projects/agent1
#   ~2–3 min: boot → cloud-init provisions → automatic reboot into the full
#   kernel → /workspace mount verified, then returns control

sbxvm ssh agent1                          # shell inside
sbxvm opencode agent1                     # opencode TUI (cwd /workspace)
sbxvm snapshot agent1 save before-agent   # freeze the moment
sbxvm ssh agent1                          # agent does its (messy) work…
sbxvm snapshot agent1 revert before-agent # …and the machine goes back in time
sbxvm start agent1
sbxvm destroy agent1 --yes                # reclaim everything
```

`create` defaults: `--cpu 4 --mem 6144 --disk 20`. Name rules:
`^[a-zA-Z][a-zA-Z0-9._-]*$`; the libvirt domain is `sbxvm-<name>`, all VM data
lives in `vm/vms/<name>/`.

### The provisioning pipeline (`create` blocks until done)

1. qcow2 **overlay disk** on the shared master image (thin — grows to usage).
2. **cloud-init seed** (`seed.iso`, volume id `cidata`) with `user-data` /
   `meta-data` (exact NoCloud filenames — a `.yaml` suffix breaks detection).
   Contents of user-data: `dev` user + your SSH pubkey + passwordless sudo;
   package list; a systemd `workspace.mount` unit for the 9p share (only when
   `--share` given); opencode install; cloud-kernel purge (below).
3. Domain boots (BIOS/SeaBIOS path, pty serial console,
   `--noautoconsole`); `wait_ssh` polls the DHCP lease.
4. `wait_ready` waits for **cloud-init done**, then — because Debian's
   `-cloud` kernel ships **without 9p modules** — reboots once into the full
   `linux-image-amd64` kernel that provisioning installed (purging the cloud
   flavor so GRUB has exactly one kernel), and finally verifies the
   `/workspace` mount.
5. Done. The VM is fully usable. Later `update-grub`/kernel upgrades inside
   the guest keep working normally (see [the GRUB patch](#how-it-works-under-the-hood)).

Tip: no `--share`? Then the VM works in a VM-local `/workspace` (a
container-free machine for experiments); live snapshots then also work.

### Getting files in and out

```bash
# host → VM (rsync over the VM's SSH; the key and the lease IP do the rest)
rsync -av -e "ssh -i ~/.ssh/sbxvm_key" ~/my/dir/ \
      "dev@$(sbxvm ip agent1):/workspace/mydir/"

# VM → host: with a share, just cp to /workspace and it appears on the host.
# Without a share: symmetrical rsync (same command, other direction).
```

### Snapshots

Backed by qcow2 **internal snapshots** in the per-VM overlay — instant save,
instant revert, no external files.

- **VMs with a 9p share can't take *live* snapshots** — QEMU hard-forbids
  state capture while a VirtFS device is mounted. So `snapshot save` on a
  running shared VM does a quick **stop → offline snapshot → restart** (disk
  state preserved, RAM state not). No-share VMs snapshot live, RAM included.
- `snapshot revert` drops all post-snapshot changes and leaves the VM in the
  snapshot's own power state — typically `shut off`, so follow with
  `sbxvm start <name>` (a message in the output reminds you).
- `destroy` refuses nothing: it stops the VM, deletes *all* snapshots first
  (virsh otherwise refuses to undefine a domain with snapshots), undefines
  with NVRAM cleanup and removes `vm/vms/<name>/` entirely.

### Command reference

```text
sbxvm fetch-image                 download + auto-patch the master image (once)
sbxvm create NAME [opts]          create + fully provision a VM
      [--cpu N] [--mem MB] [--disk GB] [--share DIR]
sbxvm ssh NAME [-] command        ssh as dev@ (interactive shell w/o command)
sbxvm shell NAME                  alias of ssh
sbxvm opencode NAME [--] args     opencode TUI inside the VM (cwd /workspace)
sbxvm ip NAME                     current lease IP (from virsh domifaddr)
sbxvm start | stop NAME           power control (stop = ACPI, then forced)
sbxvm destroy NAME [--yes         delete VM: stop + snapshots + disk + dir
        required when non-interactive]
sbxvm ls                          list VMs: state, IP, snapshot count
sbxvm snapshot NAME save|list|revert|delete [...]
sbxvm console NAME                attach the serial console (needs a real TTY)
sbxvm version | help              meta
```

Env flag: `SBXVM_SERIAL_FILE=1` before `create` logs the serial console to
`vm/vms/<name>/serial.log` (a mode-0666 file readable by you) instead of
only a pty.

---

## The docker backend (sbxvmd)

The same `sbxvm` CLI can drive VMs **without libvirt at all**: each VM is one
plain Docker container running qemu. Select it per invocation:

```bash
SBXVM_BACKEND=docker sbxvm version        # sbxvm 0.1.0 (backend: docker)
SBXVM_BACKEND=docker sbxvm create demo --cpu 2 --mem 2048 --disk 12 \
    --share ~/projects/demo
SBXVM_BACKEND=docker sbxvm ssh demo
```

(or `export SBXVM_BACKEND=docker` once per shell. **libvirt stays the
default** so the original path is never switched by accident.) The same
command reports which backend a given invocation will use:
`SBXVM_BACKEND=docker sbxvm version` → `(backend: docker)`.

### Shape

- **One container = one VM** (`sbxvmd-<name>`, `--restart unless-stopped`).
  Lifecycle is plain `docker run/stop/rm`; `stop` traps SIGTERM in PID-1 and
  hands the guest an ACPI shutdown (then a hard kill after 50 s — a stopped
  guest always exits cleanly).
- **State lives in a named volume** (`sbxvmd-store`), not in bind mounts: the
  patched master (`sbxvm fetch-image`, the same one-time fetch) copied in as
  `/store/master.qcow2`, and per-VM
  `/store/vms/<name>/{disk.qcow2, seed.iso, serial.log}` — the qcow2 gets
  native ext4 I/O inside the engine instead of the Desktop bind-mount tax.
- The runtime image `sbxvmd:latest` is **built automatically** on first
  `create` (Debian + qemu + xorriso; sources in `vm/sbxvmd/`). The host side
  writes the seed files as usual into `vm/vms/<name>/seedwork/` and leaves
  the rest to the container: the seed dir is bind-mounted read-only at
  `/seed-src` and packed into `seed.iso` *inside* (no host xorriso).
- **SSH is a published port, not a DHCP lease**: the container publishes
  `127.0.0.1::<random>` and **that port changes on every stop/start** (and on
  the restart each snapshot save does). `sbxvm ssh|ip|ls` re-resolve it via
  `docker port`, so always go through the CLI; `sbxvm ip demo` prints
  `127.0.0.1:<port>` for hand-rolled ssh/rsync.
- `--share DIR` is bind-mounted at `/share` in the container and exposed to
  the guest as the same 9p virtfs as the libvirt path (`hostshare` →
  `/workspace`).
- Serial console: `SBXVM_BACKEND=docker sbxvm console demo` streams it via
  `docker exec` (`sbxvmd-entry serial`); it also lands in
  `/store/vms/demo/serial.log` (rotated to `.1` per boot). Debug helpers:
  `docker logs sbxvmd-demo` (PID-1 side) and a qemu monitor under the hood —
  `docker exec sbxvmd-demo sbxvmd-entry ctl 'info status'`.
- **KVM is mandatory**: `create`/`start` probe nested `/dev/kvm` through a
  container first and **fail loudly on a TCG-only engine** — there is no
  silent fallback; software emulation would make the VM worthless.

### The one trap: guest `docker0` must not get `172.17.0.0/16`

Hard-won field knowledge, worth reading once. When the *host* side is Docker
Desktop, every published connection lands in the VM container sourced from
`172.17.0.1`. If the guest itself runs docker with its default bridge
(`docker0 = 172.17.0.1/16`), the guest starts believing `172.17.x.x` is *its
own* subnet — replies to inbound SYNs route into a dead, linkdown `docker0`,
and **inbound TCP from the host (ssh!) hangs forever**, while localhost and
outbound keep working. It is *not* iptables (flushing changes nothing), it
survives a dockerd restart, and it heals by exactly one thing: never putting
`172.17.0.0/16` on the guest's docker0 in the first place.

The seed therefore plants `/etc/docker/daemon.json` into every docker-backend
VM:

```json
{
  "bip": "172.28.0.1/16",
  "default-address-pools": [ { "base": "172.28.0.0/12", "size": 24 } ]
}
```

**Don't remove that file from the seed.** With it, nested `docker run` works
inside the VM and ssh survives dockerd startup indefinitely; without it the
VM goes mute the second dockerd comes up. Forensics of this shape, for the
record: in-guest `tcpdump -i ens3 'tcp port 22'` shows SYNs from `172.17.0.1`
arriving with zero replies leaving, and `ip -br a` shows
`docker0 … 172.17.0.1/16 DOWN`. If the host docker itself ever owns
`172.28.x`, move both ranges in the seed.

### Kernel, 9p and snapshots

Same provisioning shape as the libvirt path: `create` runs cloud-init, then
**reboots once** into the full `linux-image-amd64` kernel (Debian's `-cloud`
kernel ships without 9p modules) — the `/workspace` share only exists after
`create` returns (it verifies the mount), and the seeded `workspace.mount`
unit brings it back on every boot.

Snapshots are **always offline** on this backend (the qcow2 must be unlocked,
and running-VM state can't be captured through a stopped container anyway):
`save` does `docker stop` (ACPI grace) → `qemu-img snapshot -c` in a
throwaway toolbox container → `docker start`; `revert` needs the VM stopped
and **leaves it stopped** — follow with `sbxvm start <name>` (the CLI says
so); `list` reads with `-U` while the VM runs. RAM state is never captured —
the same trade-off a 9p-shared VM has on libvirt.

---

## sbxd — SBX Desktop (daemon, dashboard, docker-style CLI)

`sbxd` is the resident control plane for the whole kit: one Go binary that
serves a **unified inventory** of every sandbox kind (bin/sbx containers,
sbxvmd docker-backend VMs, libvirt domains), a **web dashboard** and a
**docker-lexicon CLI** — GUI and CLI are both clients of the same REST/SSE
API. No SDKs: engines are driven through `docker`/`virsh` and the kit's own
CLIs, so the binary stays stdlib-only and single-file.

```text
        Windows Terminal / browser / Electron shell
              │  (quick-connect, copy-connection)
┌─────────────▼────────────────────────────────────────┐
│ sbxd — localhost-only daemon (default port 7770)     │
│  • inventory scan: docker ps/stats, virsh domstats   │
│  • REST + SSE API for dashboard & CLI                │
│  • attach desk: one persistent tmux session each     │
│  • embedded dashboard (no build step, vanilla JS)    │
│  • LLM proxy :7775 — token & dollar accounting       │
│    (the ONLY listener reachable from sandboxes)      │
└──────────────────────────────────────────────────────┘
```

### Quick start

```bash
cd sbxd && go build -o bin/sbxd ./cmd/sbxd
./bin/sbxd serve            # dashboard at http://127.0.0.1:7770
./bin/sbxd ps               # unified table (containers + both VM backends)
./bin/sbxd stats            # with live CPU/MEM (docker stats / virsh domstats)
./bin/sbxd attach smoke3    # persistent tmux session, shell inside (detach: Ctrl-b d)
./bin/sbxd attach agent2 --oc   # same session but with the opencode TUI
./bin/sbxd connect smoke3   # print connection variants (ssh/shell first)
./bin/sbxd logs smoke3      # docker logs / serial / file-tail, streamed
./bin/sbxd start|stop NAME  # engine start/stop (docker / kit sbxvm semantics)
./bin/sbxd rm NAME [--yes]  # fully delete a sandbox (VM: disk+snapshots w/ --yes)
./bin/sbxd doctor
```

CLI verbs talk to the daemon when it runs (`SBXD_PORT`, default 7770) and
fall back to direct engine calls otherwise.

### The desktop shell

`sbxd/desktop/` is a thin Electron wrapper (~200 lines of `main.js`): a
native window pointing at the dashboard, which auto-boots `sbxd` inside WSL
via `wsl.exe` if unreachable — Docker-Desktop-style. The webapp itself stays
portable: any browser gets the identical UI at the same URL. Run instructions
in `sbxd/desktop/README.md`.

### P2 — the LLM proxy, wallets and one global OpenCode config

P2 turns sbxd into the **single money-aware gate** for every agent call a
sandbox makes. The upstream provider key never leaves sbxd; sandboxes hold
only their own minted `sbxk-…` bearer key, and each sandbox gets a budget
(USD/day wallet) enforced *before* any request is forwarded.

```text
sandbox (opencode TUI)                                   
  │ Authorization: Bearer sbxk-<sandbox>-<rand>          
  ▼                                                      
sbxd proxy :7775  ── 401 unknown/revoked key        
  │     ┝─ 429 wallet dry (today ≥ budget)          
  │     ┝─ re-auth shoulder: real provider key never in the sandbox
  ▼                                                      
upstream (openai or anthropic style) ── SSE streams pass
through verbatim; usage parsed from final chunk /
message_start/_delta, else estimated (bytes/4, est=true)
  ▼
~/.local/state/sbxd/usage.jsonl  →  burn dashboard tab
```

Setup, once:

```bash
./bin/sbxd config init     # import providers from host ~/.config/opencode/opencode.json
./bin/sbxd key mint NAME   # per-sandbox proxy key (provider default)
./bin/sbxd budget NAME 5   # $/day wallet (0 = unlimited, default)
./bin/sbxd sync NAME       # push proxy-backed opencode config INTO the sandbox
#   or in the dashboard: Burn tab → per-sandbox sync button
```

After a sync, the sandbox's opencode only ever sees one provider
(`sbx-proxy`) whose `apiKey` is the sandboxes' minted key — edit the global
config at `~/.config/sbxd/config.json` (`sbxd config edit` searches for open
editors, `sbxd config set proxy.port 7775` etc.) and **running daemons pick
it up without a restart** (stores are stateless file views; the proxy
re-reads config on every request).

#### Reachability: the in-engine proxy relay (Docker Desktop)

On Docker Desktop engines the containers run in a separate utility VM whose
sandboxes *cannot* reach the WSL distro's proxy listener on any route
(distro IP, bridge gateway or `host.docker.internal`); sbxd fixes this
instead of failing loudly:

- the **first container sync** spawns `sbx-proxy-relay`, a `--network host`
  container running this same binary in a dedicated **`sbxd proxy`**
  (standalone, dashboard-less) mode, with the daemon's config/keys/state
  dirs bind-mounted (sbx:base image or any local glibc base; `--restart
  unless-stopped`; host-uid — it is **not** labeled `dev.sbx`, so it never
  shows in the inventory);
- the sandbox then dials `http://<bridge-gateway>:<proxy-port>/v1`
  (typically `172.17.0.1:7775`) — reachable by construction;
- the relay re-reads config/keys per request: later `key mint`, wallet
  edits and `budget` changes apply live; usage receipts append to the same
  `usage.jsonl` the dashboard reads (shared `/state` mount);
- the relay is **owned by its daemon** (mount sources = its state dirs): a
  foreign relay left by a hermetic test daemon is replaced automatically
  (`relay.replace` in the audit trail); suite cleanup removes suite-owned
  relays and the real daemon re-adopts on the next sync;
- `doctor` shows the relay line on Docker Desktop engines ("proxy relay: …
  (running at gw …)"), every declined/failed relay attempt is audited
  (`relay.skip` with the reason), and `SBXD_RELAY_DISABLE=1` turns the
  feature off (non-Docker-Desktop engines never spawn it: native dockerd,
  bridge-mode WSL setups and VM sandboxes have host-reachable routes
  already).

The sweeper only ever stops idle sandboxes its own daemon spawned: kit
containers carry a `dev.sbx.owner` label (stable hash of the spawning
daemon's config dir) and foreign-owned sandboxes are skipped — a hermetic
test daemon can never idle-stop the user's real sandboxes.

What lives where (all local, all `chmod 600`, env-overridable for tests):

| Path (default) | Contents |
|---|---|
| `~/.config/sbxd/config.json` | providers (base_url + **upstream key**), default model, proxy port/bind, `prices` overrides |
| `~/.local/share/sbxd/keys.json` | per-sandbox `sbxk-…` tokens + per-sandbox budgets |
| `~/.local/share/sbxd/synced/<name>.json` | what each sandbox was last synced with |
| `~/.local/state/sbxd/usage.jsonl` | one line per proxied call: tokens in/out, cost, est flag, dur, status |
| `~/.local/state/sbxd/tunnel-<name>.pid/.log` | the ssh -R tunnels keeping guest loopback on the proxy |

Accounting rules: upstream usage wins when reported (OpenAI final chunk /
non-stream `usage`, Anthropic `message_start`/`message_delta`); otherwise
tokens are estimated from payload bytes (in = request bytes/4, out = emitted
delta text/4) and flagged `est`. Pricing uses a builtin longest-prefix table
(GPT/Claude/Gemini/Qwen/GLM/…) with `$`/Mtok, overridden by the config's
`prices: { "<model|prefix>": [in, out] }`; unknown models cost 0.

Budgets are wallets per sandbox *name*: the proxy checks `today ≥ budget`
**before** forwarding and answers `429` with today's burn when dry — a
revoked key or an empty wallet stops the spend instantly even mid-session.

**Reachability** — how sandboxes find the proxy (base_url written into their
opencode config; sbxd verifies it actually answers before):

| Sandbox kind | Path | base_url |
|---|---|---|
| VMs (sbxvmd, libvirt, docker-in-docker) | persistent `ssh -R` tunnel, kept alive by sbxd (pid/log in state dir) | `http://127.0.0.1:7775/v1` |
| Containers (probed in order) | WSL eth0 IP → `host.docker.internal` | first that answers |
| nothing answers | sync reports the failure + how to fix (e.g. kit `SBX_NET=host`) | — |

The tunnel belongs to the daemon: it respawns automatically if ssh drops or
sbxd restarts. Note on Docker-Desktop WSL setups: containers cannot reach
WSL-side ports directly (they run in a separate VM), so VMs always go through
their ssh tunnel and plain containers either reach the probed route or tell
you what to change at creation time.

New CLI verbs (all file-backed — work with or without the daemon):

```bash
./bin/sbxd config init|print|get|set|path|edit
./bin/sbxd usage [NAME]      # global per-sandbox table, or a full drill-down
./bin/sbxd key mint|ls|revoke NAME
./bin/sbxd budget NAME [USD] # show or set; proxy trips 429 when dry
./bin/sbxd sync NAME|all [--provider P]
```

New dashboard surfaces: a per-sandbox **Burn** tab (tokens, calls, est
flags), and a global Burn+Config section behind the sidebar links — wallet
inputs, per-sandbox drilldowns (model split + last-10 calls + proxy info)
and a provider config editor with masked secrets (`<masked>` values survive
saves; re-import from the host file with one button).

API additions: `GET/POST /api/config` (+ `/api/config/import`),
`GET /api/usage[?name=]`, `POST /api/usage/budget`,
`POST /api/usage/purge`, `POST /api/usage/pin`,
`POST /api/sandboxes/<kind>/<name>/sync`.

The dashboard **Burn view** (sidebar link) now leads with big burn cards
(today, all-time, live/stale counts) and a wider table. Rows whose sandbox
no longer exists are **stale**: hidden by default (toggle *show stale*) and
greyed with a chip when shown — their history lives on in
`usage.jsonl` until you delete it, per-row (stale rows carry
*delete records*) or as a **bulk purge stale history** (double-confirm;
pinned ★ rows are always exempt, and a pin protects a live sandbox's
history too). Purge and pin changes are audit-logged (`ledger.purge`,
`ledger.pin`/`unpin`), pins persist in `config.json:pinned_sandboxes`.

### P3 — recipes, config-file budgets, mounts, egress, sweeper, audit, create wizard

P3 turns sbxd from observer into **operator**: everything the kit used to
only do through `bin/sbx` becomes declarative and daemon-managed, with an
audit trail for the control plane.

**Everything settable, lives in `config.json`** (`sbxd config edit`):
constant wallets are no longer per-invocation flags but configuration, plus
token pricing for honest burn math:

```jsonc
{
  "opencode": { "providers": { "yourprovider": {
      "base_url": "https://api.anthropic.com/v1", "api_key": "sk-…",
      "models": ["claude-sonnet-4"] } } },
  "proxy": {
    "port": "7775",
    "default_budget_usd": 5,          // wallet every sandbox starts with
    "default_budget_tokens": 100000   // token wallet (0 = unlimited)
  },
  "prices": {                          // $ per 1M tokens, [input, output]
    "claude-sonnet-4":   [3, 15],
    "openai/completions/": [0.15, 0.6] // "prefix/" matches all models underneath
  },
  "budgets":       { "myproj": 5, "sbx-side-tests": 0 },    // USD wallets
  "budget_tokens": { "myproj": 250000, "sbx-side-tests": 0 }, // token wallets
  "sweeper":       { "idle_stop_minutes": 45 }   // auto-stop dead sandboxes
}
```

`sbxd budgets` shows every wallet against actual burn; `sbxd config set
sweeper.idle_stop_minutes N` re-reads live — no restart. The **sweeper**
(daemon goroutine, samples every 30s) stops kit-labeled sandboxes whose CPU
stayed under 1% for the configured window (VM-backed `sbxvmd-*` containers
are always spared); reversible with `sbxd start NAME`; every stop lands in
the audit trail.

**Recipes — declarative sandboxes** (`sbx.yaml` next to your project):

```yaml
name: myproj          # container becomes sbx-myproj
stack: go             # needs the kit image once: bin/sbx build go
mounts:
  - ~/projects/core:/workspace/core   # ~ expands; ":ro" suffix keeps it read-only
  - /var/data/cache:/cache:ro
env:
  FOO: bar
ports: ["8080:8080"]  # published for real at container create
budget: 5             # USD wallet
budget_tokens: 250000
egress: proxy-only    # config-time fence, see below
sync: true            # opencode → sbx proxy wiring after spawn
attach: --oc          # extra flags for the suggested attach command
```

```bash
./bin/sbxd recipe print sbx.yaml     # honest plan of the exact spawn
./bin/sbxd recipe validate sbx.yaml  # checks: name/stack/dup targets/egress/budgets
./bin/sbxd recipe up sbx.yaml [--yes]  # create+start → wallets → egress → sync
```

`recipe up` recreates the kit's container contract faithfully (dev.sbx
labels, opencode auth mounts, git/ssh identity read-only, per-stack package
cache volume, restart policy) — so `bin/sbx attach sbx-myproj` works
unchanged. Duplicate names are refused; non-interactive runs need `--yes`.

**Mounts after the fact** (`sbxd mount`): `mount ls NAME` / `mount add NAME
/h:/x[:ro] --yes` / `mount rm NAME /x --yes`. Docker bakes volumes at
creation, so add/rm does a guarded recreate (same image, same flags,
original mounts carried, state in named volumes/paths keeps) and skipping
`--yes` in a non-TTY means "do nothing". VM-backed `sbxvmd-*` containers
refuse live recreation with an explanation.

**Egress presets** — a convenience fence (not a security boundary):
`sbxd egress NAME proxy-only` fences public addresses out of the sandbox
netns (iptables OUTPUT chain SBX_EGRESS; loopback, established flows, RFC1918
and DNS stay open) so LLM traffic must ride the sbx proxy on the host.
`sbxd egress NAME none` keeps only loopback+established; `allow` resets to
the Docker default. Needs no restart: rules live in the netns, so re-apply
after any recreate. Containers that already run iptables get it via plain
exec; everything else uses a tiny auto-built helper image (`sbxd-egress-helper:1`, built once on first use).

**Audit trail** — `state/audit.jsonl` records the control plane: sandbox
start/stop/spawn, key mint/revoke, wallet changes, mount recreates, egress
changes, sweeper stops. `sbxd audit [N]` renders the last N as a table.
Best-effort by design: a failed audit write never blocks the action.

**Create wizard** — sidebar `＋ New sandbox` on the dashboard (identical
POST `/api/create` API): a sectioned form (basics / network / environment /
mounts / opencode / budgets) with the same validation as the CLI (unique
name, absolute targets, known stacks/presets) and the same kit-faithful
container shape. ENV is a dynamic key→value row editor (Portainer-style),
ports are host→container rows, and mount sources can be picked via a real
folder-browser pop-up (`GET /api/fs` lists directories under `$HOME` —
loopback-only dashboard, traversal outside home is rejected).

### House rules & security posture (P2 included)


- Dashboard binds `127.0.0.1` only, never touches `~/.ssh/sbxvm_key`, never
  falls back to a TCG-only engine.
- The proxy **must** be reachable from sandboxes, so it binds `0.0.0.0:7775`
  — the rest is gated: every request needs a minted, non-revoked `sbxk-…`
  key (401 otherwise), wallets can return 429, and config/keys files are
  `0600`. Upstream provider keys live only in sbxd's config file and are
  rewritten into the outbound request server-side.
- A synced sandbox config is written into a *shared-write* path the kit
  already mounts for VMs, or a container-only XDG shim path — never over a
  location the kit mounts writable for other purposes.

### Sequel phases

- **P1 (shipped)**: inventory + stats/inspect, logs/serial, persistent tmux
  attach desk + quick-connect/copy-connection, doctor, dashboard, desktop shell.
- **P2 (shipped)**: token-accounting LLM proxy — per-sandbox keys, per-sandbox
  burn/cost ledger, budget ceilings, SSE-aware token counting — plus the
  global OpenCode settings file pushed into sandboxes, with verified
  reachability paths per sandbox kind (ssh tunnel for VMs, probe for containers).
- **P3 (shipped)**: recipes (`sbx.yaml` print/validate/up), wallets + token
  pricing + sweeper knob in `config.json`, mounts after creation (guarded
  recreate), egress presets (iptables in the sandbox netns with helper
  fallback), end-to-end control-plane audit trail + `sbxd audit`, the
  dashboard create wizard (POST `/api/create`) and tokens in the burn view.
  The create wizard now covers **all three vessels**: `container` (sbx) and
  both VM kinds (`vm` + `vm_backend: libvirt|docker`) — kind radios, VM
  sizing (vCPU/MEM/disk/optional 9p share dir), backend auto-picked by
  engine reachability, and duplicate/charset validation matching the kit's
  own rules (`sbxvm.sh create` runs underneath, so provisioning semantics
  are identical to the CLI). Start/Stop/Delete in the dashboard and CLI
  (`sbxd start|stop|rm NAME`) already span all three kinds.

- **VM boot self-registration**: the kit signals the daemon when a guest
  boot starts (`POST /api/vm/boot {"name":…,"phase":"begin"}`) and again
  once it is truly up (ssh + cloud-init provisioning confirmed;
  `"phase":"end"`). Between the two signals the dashboard shows an amber
  **booting** state instead of the engine's premature `running`, attach is
  politely refused with a hint, and endpoint security is the daemon's own:
  loopback-only listener + CSRF origin guard (the kit's curl has no Origin
  header, i.e. a native client). A begin that never gets its end expires
  after ~30 minutes (`boot_pending.json` in the state dir, survives daemon
  restarts). On boot-end a synced VM's ssh proxy tunnel is re-established
  immediately, so opencode in the guest works right after boot rather than
  waiting for the daemon's periodic tunnel-sweep loop.
  Snapshot timeline: pending.
- **P4+**: autoscaling ideas, more stacks, team features.

---

## How it works under the hood

### Container side

- One Dockerfile (`containers/Dockerfile`), one shared `common` stage
  (Debian `trixie-slim`: git, curl, jq, ripgrep, build-essential, python3,
  pip-csafety override, `git safe.directory '*'`, opencode fallback) and one
  minimal per-stack target (`node`, `python`, `go`, `rust`, `base`).
- The image bakes user `sbx` (uid 1000); at **runtime** the entrypoint
  (`containers/entrypoint.sh`) creates/adjusts a user matching your *actual*
  host uid (`SBX_UID`/`SBX_GID`), grants passwordless sudo, fixes ownership
  of `$HOME`/cargo registry if stale (only when needed), then `setpriv`s to
  your uid and execs the command. Persistent mode (no command) execs
  `sleep infinity` so you attach/detach at will.
- No side effects on the host beyond the bind mounts + named cache volumes.

### VM side

- **Master image**: official `debian-13-genericcloud-amd64.qcow2`
  (cloud.debian.org, trixie). `fetch-image` → downloads → **patches** → puts
  a `.sbxvm-patched` marker next to it, so patching is idempotent and
  re-download safe. The patch (applied offline with `debugfs`, no booting):
  - strips every GRUB `load_video` call from `/boot/grub/grub.cfg`, and
  - writes `GRUB_GFXPAYLOAD_LINUX=text` and
    `GRUB_TERMINAL_OUTPUT="console serial"` into `/etc/default/grub`.
- **Why patch at all:** on WSL2/Hyper-V nested KVM with QEMU ≥ 11, GRUB's
  video-mode-set (the `int 0x10`/VBE call behind `load_video`) hard-resets
  the VM — an endless `"Booting 'Debian GNU/Linux'"` loop at ~30–110
  resets/minute burning CPU. Verified by differential boot tests (KVM, CPU
  model, machine type, TCG all exonerated; direct `-kernel` boots work; the
  patched image boots to login). Setting the two `/etc/default/grub` keys in
  addition to editing grub.cfg matters because in-guest `update-grub` (any
  kernel upgrade) *re-generates* grub.cfg — the generator would otherwise
  re-emit `load_video` from `10_linux`/`00_header` and brick itself again.
- **Per-VM state**: `vm/vms/<name>/` contains the overlay `disk.qcow2`,
  `user-data.yaml`/`meta-data.yaml` (generated seed, kept for reference),
  `seed.iso`, and the serial pty (or `serial.log` in file mode). Snapshots
  live inside the overlay's qcow2 metadata.
- **Networking**: libvirt `default` NAT network (`virbr0`,
  `192.168.122.0/24`) — the VM reaches the internet through your host;
  the host talks to the VM via SSH (`sbxvm ip` reads the dhcp lease).
- **9p share**: `--filesystem type=mount,…,target=hostshare` +
  accessmode `mapped`. Guest side mounts it with a systemd `.mount` unit
  (`options=trans=virtio,version=9p2000.L`; requested `msize=104857600`
  negotiates down to ~512 KiB — normal virtio behavior).
  Host-side ownership lands as `you:libvirt-qemu` because qemu runs as you.

---

## Field debugging recipes

These recipes were hard-won — they live here so you don't have to rediscover
them:

**Capture the serial console** (what `sbxvm console` needs a TTY for):

```bash
PTY=$(virsh --connect qemu:///system ttyconsole sbxvm-<name>)
timeout 8 cat "$PTY"          # read
printf '\n' > "$PTY"          # press Enter, if you must talk to a getty
```

**VM reset-loops at boot** (serial shows endless `"Booting …"`):
the master image lost its patch. Check & re-patch:

```bash
ls vm/images/*.sbxvm-patched || sbxvm fetch-image   # re-fetch (auto-patches)
# boot shape (outside libvirt, in vm/.work — NOT /tmp, it's a RAM tmpfs):
mkdir -p vm/.work && cd vm/.work
qemu-img convert -O raw ../images/debian-13-genericcloud-amd64.qcow2 v.raw
S=$(fdisk -l v.raw | awk '/Linux root/ {print $2; exit}')   # rootfs sector
dd if=v.raw of=v.ext4 bs=512 skip=$S status=none
G=$(debugfs -R "cat /boot/grub/grub.cfg" v.ext4 2>/dev/null)
echo "$G" | grep -cE '^[[:space:]]*load_video[[:space:]]*$'    # want: 0 (unpatched: 4)
echo "$G" | grep -c  'disabled by sbxvm'                       # want: 4 (the strips)
debugfs -R "cat /etc/default/grub" v.ext4 | grep -E 'GRUB_(GFXPAYLOAD|TERMINAL)'
# expect: GRUB_GFXPAYLOAD_LINUX=text / GRUB_TERMINAL_OUTPUT="console serial"
```

**Isolate a boot problem from libvirt directly under QEMU** (fast iteration):

```bash
qemu-img create -f qcow2 -o backing_file=…/master.qcow2,backing_fmt=qcow2 t.qcow2 20G
timeout 60 qemu-system-x86_64 -enable-kvm -machine pc -cpu host -smp 2 -m 2048 \
  -drive file=t.qcow2,format=qcow2,if=virtio -snapshot \
  -display none -serial file:serial.log -monitor none -nodefaults
grep -ac 'Booting' serial.log        # counts reset loops
# SeaBIOS phase tracing (proves BIOS hands over when it does):
-chardev file,id=biosdbg,path=bios.log -device isa-debugcon,iobase=0x402,chardev=biosdbg
```

**"ssh did not come up"**: check `sbxvm ls` for the lease IP, and the serial
for cloud-init progress. First-boot provisioning needs ~60–90 s at fastest.

**tmpfs trap:** `/tmp` in WSL2 setups is a *RAM-backed* tmpfs. Multi-GB
qcow2/raw conversions go to `vm/.work` (disk-backed), never `/tmp`.

**Docker Desktop on Windows**: engine down → "docker engine unreachable";
newer CLI in `$PATH` → weird `409`s; both are Desktop-side settings, see
[container requirements](#requirements).

**Docker-backend VM: ssh fine at boot, dead after the guest starts docker**
(SYNs arrive, no SYN-ACKs leave the guest): the guest's `docker0` took
`172.17.0.1/16`, which collides with Docker Desktop's publisher source IP.
The seed's `/etc/docker/daemon.json` `bip` fix exists for exactly this — see
[the docker backend](#the-docker-backend-sbxvmd).

---

## Tests

```bash
tests/smoke.sh      # container sandboxes — stacks, mounts, fresh runs, rm/prune
tests/vmsmoke.sh    # VMs (libvirt backend) — boots a real throwaway VM
tests/vmdsmoke.sh   # VMs (docker backend) — boots a VM inside a container
tests/sbxddsmoke.sh # SBX Desktop suite — the comprehensive gate (see below)
```

Every suite prints its own tally (`== result: N passed, 0 failed ==`); the
gate is **0 failed**. The daemon suite runs hermetic scratch daemons on
scratch ports but needs ≥ 1 running sandbox visible to the inventory for its
P1 section — the invocation dance is documented in `docs/RELEASING.md` §4.

`smoke.sh`: doctor sane, all four stack autodetections (`package.json` → node,
`pyproject.toml` → python, `go.mod` → go, `Cargo.toml` → rust, empty → base),
one smoke-sandbox per stack with its toolchain running (node+npm, python+uv,
go, rust), fresh runs leave nothing behind, container-written files are
host-user owned, exec/status/reattach/exit-code propagation, `sbx opencode`
works, `rm`/`ls` bookkeeping, `prune`, `caches ls`.

`vmsmoke.sh`: creates a real throwaway VM (`sbxvmsmoke-<epoch>`) with a
mktemp share and exercises the whole loop — master + libvirt present,
create+ssh, user/sudo, 9p host→guest and guest→host, opencode in VM, docker
service, snapshot save → rogue-writes → revert → restart → file resurrected,
then destroy + vm dir cleaned + domain undefined (this last check caught a
real bug: undefine silently skipped while snapshots existed).

The reset-loop / boot investigation also produced a one-line live test
embedded in vmsmoke: if a fresh VM can't ssh, it prints the serial log path.

`vmdsmoke.sh`: the same loop against the docker backend (the suite presets
`SBXVM_BACKEND=docker`) — creates `sbxvmdsmoke-<epoch>`, a VM that is itself
a Docker container, then checks ssh/sudo, 9p host→guest and guest→host,
opencode, full kernel, **a real `docker run --rm hello-world` inside the VM**
(the check that dies first if the guest-bridge collision ever returns),
snapshot save → rogue-write → revert → restart → file resurrected, then
destroy + vm dir + container-undefined checks.

`sbxddsmoke.sh`: spins the daemon on a scratch port (7790, auto-stopped
again) and verifies the SBX Desktop surface — health probes, unified `ps`,
`stats` with CPU/MEM, all `connect` variants (primary copy-connection first),
the persistent tmux attach lifecycle (rolled → alive → live shell in pane →
cleaned up), instant SSE snapshot, serial-log frames, dashboard assets and
`doctor`. Then a **hermetic P2 section** (scratch dirs, python mock upstream,
scratch daemon + proxy ports): 401 without key, CLI-minted key instantly
live, non-stream + SSE passthrough, ledger rows with upstream-counted usage
and `est` estimates, wallet 429 trip + unblock, config get/set round-trip,
masked API config, and key-revoke cut-off. It never mutates any sandbox; it
needs at least one *running* sandbox in the inventory for the P1 part.

---

## File map

```text
bin/sbx                    container sandbox CLI (the daily driver)
bin/sbxvm → vm/sbxvm.sh    VM CLI (symlinked by install-self)
containers/Dockerfile      5 stack images from one shared base layer
containers/entrypoint.sh   runtime uid/sudo/ownership bootstrap
tests/smoke.sh             container suite (all stacks; no VMs needed)
tests/vmsmoke.sh           VM suite (libvirt; boots a real throwaway VM)
tests/vmdsmoke.sh          VM suite (docker backend; VM = container)
tests/sbxddsmoke.sh        SBX Desktop suite — comprehensive gate (P1+P2+P3)
sbxd/                      SBX Desktop: Go daemon + dashboard + docker-style CLI
SECURITY.md                full security survey: fixed findings + accepted risks
  cmd/sbxd/                entrypoint (main.go)
  internal/engine/         docker/virsh/kit-CLI wrappers, file tail
  internal/sandbox/        unified entity + cross-runtime collector
  internal/attach/         tmux sessions, ConnInfo, wt.exe launcher, ssh -R
                           proxy tunnels, sync write transports, probes
  internal/api/            REST + SSE server, embedded dashboard (web.go)
  internal/cli/            serve + ps/stats/logs/attach/connect/start/stop +
                           doctor + config/usage/key/budget/sync (p2.go)
  internal/config/         global settings store + masked merge + host import
  internal/keys/           per-sandbox proxy keys + budget wallets
  internal/ledger/         usage journal, token/cost summaries, pricing
  internal/proxy/          LLM proxy: auth, budgets, usage-aware SSE pass
  internal/cfgsync/        per-sandbox opencode config generation + stale markers
  internal/env/            shared constants, kit-root resolution, SBXD_* dirs
  bin/sbxd                 built binary
  desktop/                 thin Electron shell (see sbxd/desktop/README.md)
vm/images/
  debian-13-genericcloud-amd64.qcow2        patched master (shared by all VMs)
  debian-13-genericcloud-amd64.qcow2.sbxvm-patched   idempotency marker
vm/sbxvmd/                 docker backend runtime: Dockerfile + PID-1 `entry`
vm/vms/<name>/             per-VM seed files (+ disk & serial on libvirt; the
                           docker backend keeps disk/serial in the
                           `sbxvmd-store` volume, seeds here)
vm/.work/                  scratch convention for multi-GB disk work
                           (never /tmp — RAM-backed tmpfs)
```

---

## Disk footprint & housekeeping

- **Master image**: ~950 MB on disk (~3 GiB virtual), shared read-only by all VMs.
- **Per VM**: overlay starts ≈ 200 KB and grows only with actual writes
  (sparse up to `--disk` size). Twenty-GB default means typical 2–3 GB usage.
- Reclaim: `sbxvm destroy <name> --yes` (everything of that VM, incl.
  snapshots), `sbx rm --yes` / `sbx prune --yes` (containers),
  `sbx caches nuke --yes` (cache volumes), `docker rmi sbx:*`
  (images will rebuild on demand). To fully restore from zero:
  delete `vm/images/` and `vm/vms/`, then `fetch-image` again.
- **Docker backend extra**: VM state lives in the named docker volume
  `sbxvmd-store` (the ~950 MB master shared by all VMs + one
  `/store/vms/<name>/` subdir per VM). `SBXVM_BACKEND=docker sbxvm destroy <name> --yes` removes the
  container and that VM's store dir; the volume and the `sbxvmd:latest`
  runtime image remain until you `docker volume rm sbxvmd-store` /
  `docker rmi sbxvmd:latest` (the image is rebuilt automatically on the next
  `create`). Housekeeping rule of thumb: `docker system df` shows the real
  picture when the engine also holds the sandbox images and caches.

---

## Security posture (honest)

This kit makes *misuse messes* cheap — it is not a malware containment cell.

- The sandbox user has **your uid + passwordless sudo inside the sandbox**
  (container or VM), and the internet is reachable — that is the workflow, not
  an incident in itself.
- The agent can read what has been deliberately mounted in: host opencode
  state/token, `~/.gitconfig`, `~/.ssh` (ro). A determined misbehaving agent
  could exfiltrate those. If that worries you for a given task:
  `sbx --fresh --no-auth --no-ssh --net none` or a no-share `sbxvm create`.
- **P2 reduces one of these surfaces**: after `sbxd sync NAME`, the sandbox's
  opencode holds only its own `sbxk-…` proxy key (scoped to that sandbox,
  revocable, budget-capped). The upstream provider key stays in
  `~/.config/sbxd/config.json` on the host and is rewritten server-side —
  it no longer has to ride along inside the sandbox at all.
- The P2 proxy listener binds `0.0.0.0` *by design* (sandboxes must reach
  it). Its own gate: authentications via per-sandbox bearer keys, wallet
  ceilings that 429, and masked config over the dashboard-only API.
- **Full security survey**: see [SECURITY.md](SECURITY.md) — protection
  model, fixed findings (token entropy, constant-time compare, auth
  backoff, CSRF origin guard, symlink-hardened `/api/fs`, method hygiene)
  and the honest accepted-risks table (TLS absent, budget race, upstream
  key custody).
- The container's write surface is the project dir; the VM's is the shared
  dir + the VM disk. Everything outside stays untouched in both cases.
- Root-level "trust the container image" rules apply: anything the agent
  installs is only as trustworthy as the apt/npm/pypi registries it pulls
  from — same as your other installs, just *not on your machine*.
- Good for: "agent ate my node_modules / blew up globals / pip'd into
  system" — none of it survives `sbx rm` or `sbxvm destroy`.
  Not designed against: a malicious agent actively attacking the Docker or
  libvirt socket (root-equivalent), kernel-level container escapes, or
  piecing together your mounted credentials.

### Ship / distribution

Source ships; the heavy Debian cloud master (~1 GB unpacked) is fetched at
runtime with `sbxvm fetch-image` — that is why `vm/images/` is gitignored, and
per-VM seed ISOs are generated, not committed. Concretely:

- **Install on a new host**: prerequisites + scripted walkthrough in
  [Install: step by step](#install-step-by-step) — `./install.sh` gates the
  deps, installs `sbx` + `sbxd-serve`, and offers the one-time image fetch.
- **Release artifacts**: `make release` builds version-stamped
  `dist/sbxd-v<ver>-linux-{amd64,arm64}` + `SHA256SUMS` (Go toolchain only).
- **Agents doing the release**: follow `docs/RELEASING.md` to the letter —
  it encodes the verified build/suite/git flow and the known pitfalls.

### Contributing & repo hygiene

- Day-to-day rules live in **`CONTRIBUTING.md`**: conventional commits
  (`feat|fix|docs|refactor|test|chore(scope): summary`), one intent per
  `type/<topic>` branch, squash-merged PRs into an always-releasable `main`,
  tags only on `main`, hotfix flow from shipped tags.
- PRs get the repo's `.github/PULL_REQUEST_TEMPLATE.md` (what / how-verified /
  evidence checklists) — titles must be valid conventional commit subjects,
  because squashing turns them into history.
