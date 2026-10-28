# Hotbed

Disposable sandboxed dev environments for **OpenCode** and other AI coding
agents — container-first, with a headless VM escape hatch — so that everything
an agent installs, downloads or breaks while it works stays off your main
machine.

```text
        ┌─────────────────────────────────────────────────────────────┐
 host ──│ ~/hotbed (this kit)                                    │
        │                                                        │
        │  bin/hbd     throwaway Docker container per project    │
        │               project bind-mounted at /workspace       │
        │               toolchains die with the container        │
        │                                                        │
        │  bin/hotbedvm    headless KVM VM (libvirt or docker)   │
        │               for what containers can't do: systemd,   │
        │               nested docker, kernel-level work         │
        │                                                        │
        │  containers/  image definitions (1 Dockerfile, 5 stacks)│
        │  vm/          master image, per-VM disks, seeds        │
        │  tests/       container · VM · daemon test suites      │
        └─────────────────────────────────────────────────────────────┘
```

**Design rule of thumb:** if the work is "code, build, test, install dev
tools" → `hbd` (containers). If the work needs systemd, a real kernel,
nested Docker, or a full distro → `hotbedvm`. Both keep your host clean.

---

## Table of contents

1. [Why](#why)
2. [The two layers at a glance](#the-two-layers-at-a-glance)
3. [Requirements](#requirements)
4. [Install: step by step](#install-step-by-step)
5. [One-time setup](#one-time-setup)
6. [hbd — container sandboxes](#hotbed--container-sandboxes)
7. [hotbedvm — the VM escape hatch](#hotbedvm--the-vm-escape-hatch)
8. [The docker backend (hotbedvmd)](#the-docker-backend-hotbedvmd)
9. [hotbedd — Hotbed (daemon, dashboard, docker-style CLI)](#hotbedd--hotbed-desktop-daemon-dashboard-docker-style-cli)
10. [How it works under the hood](#how-it-works-under-the-hood)
11. [Field debugging recipes](#field-debugging-recipes)
12. [Tests](#tests)
13. [File map](#file-map)
14. [Disk footprint & housekeeping](#disk-footprint--housekeeping)
15. [Security posture (honest)](#security-posture-honest)
16. [License](#license)

---

## Why

AI agents happily `npm install -g`, `pip install`, `apt install`,
`go install`, `cargo install`… edit dotfiles, run servers, and generally paint
the town. Doing that on the machine you eat and sleep next to is a mess
waiting to happen. This kit gives every project a throwaway environment:

- **Per-project.** The project directory is bind-mounted at `/workspace` —
  that directory is the *blast radius*. Nothing else on the host is reachable.
- **Everything dies with the sandbox.** `hbd rm` / `hotbedvm destroy` deletes
  the container/VM and every tool the agent installed. The host stays clean.
- **Fast iteration.** Shared package caches (npm/pip/go/cargo) live in named
  Docker volumes — wiping sandboxes is instant, reinstalling is cache-fast.
- **opencode works inside.** Your host opencode binary, config and auth are
  mounted in — the TUI works right away with your models and settings.

## The two layers at a glance

|                       | `hbd` (containers)                | `hotbedvm` (VMs)                       |
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

### Container side (hbd)

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

### VM side (hotbedvm)

Packages (Debian-family names):

```bash
sudo apt install -y libvirt-daemon-system libvirt-daemon-config-network \
                    libvirt-clients virtinst qemu-system-x86 qemu-utils \
                    e2fsprogs xorriso
# first two: the system daemon + the default NAT network config;
# next three: virsh / virt-install / qemu-img; debugfs (image patcher) is
# in e2fsprogs
```

Get the daemon running and reachable as your user (without these, `virsh`
finds no `qemu:///system` and every VM op fails):

```bash
sudo usermod -aG libvirt "$USER"   # socket access without sudo (then re-login!)
sudo systemctl enable --now libvirtd   # classic distros
# Debian 12+/Ubuntu 24.04+ use modular daemons instead — the network one
# is needed for NAT/DHCP:
sudo systemctl enable --now virtqemud.socket virtnetworkd.socket
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

The [docker backend](#the-docker-backend-hotbedvmd) skips everything above:
no libvirt packages, no qemu on the host, no xorriso — it needs only the
docker engine with nested KVM working behind it.

---

## Install: step by step

### Quick install (one command)

```bash
curl -fsSL https://raw.githubusercontent.com/Chris5011/hotbed/main/quick-install.sh | bash
# fresh Debian/Ubuntu box, daemon as a systemd user service:
curl -fsSL https://raw.githubusercontent.com/Chris5011/hotbed/main/quick-install.sh | bash -s -- --deps --service
```

It clones the kit to `~/hotbed` (`--dir` to change), installs the
checksum-verified release binary for your arch (source build for branches
or with `--build`), runs `install.sh --yes` and — with `--service` —
installs and starts `hotbedd.service` (systemd user unit). Re-run it to
update. Other flags: `--ref vX.Y.Z|branch`, `--vm-image` (fetch the VM
master image too). While the repo is private, export `GITHUB_TOKEN` or have
`gh` logged in. The rest of this section is what happens underneath.

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
sudo apt install -y libvirt-daemon-system libvirt-daemon-config-network \
                    libvirt-clients virtinst qemu-system-x86 qemu-utils \
                    e2fsprogs xorriso
# + start the daemon (systemctl/format varies by distro) — see
#   the VM side section under Requirements
```

VMs of either backend need **nested KVM** (a writable `/dev/kvm`) — see
[Hosts](#hosts) above.

The daemon itself has one of two routes (installer or binary):

- **Prebuilt binary** (no Go needed): take `hotbedd-vX.Y.Z-linux-<arch>` +
  `SHA256SUMS` from the
  [Releases](https://github.com/Chris5011/hotbed/releases) page
  (arch map: `x86_64` → `amd64`, `aarch64` → `arm64`), verify, and drop the
  binary anywhere in the kit tree — `install.sh` adopts it into
  `hotbed/bin/hotbedd` itself (the explicit equivalent is
  `install -m755 hotbedd-vX.Y.Z-linux-amd64 hotbed/bin/hotbedd`):
  ```bash
  sha256sum -c SHA256SUMS --ignore-missing
  mv hotbedd-vX.Y.Z-linux-amd64 dist/           # or the kit root — both work
  ```
  *What the binary is:* every Hotbed piece — the `hotbedd` CLI, the REST
  + SSE controller and the embedded dashboard — in one static, stdlib-only
  Go file. Not included: the `hbd`/`hotbedvm` CLIs, which are bash scripts
  living in the kit tree (see step 2). The daemon alone can inventory,
  start/stop, log and attach to everything that already exists and spawn
  new *containers* natively — but *creating VMs* and their journal
  fallback shell out to the kit's `vm/hotbedvm.sh`, so you'll want the tree
  too.
- **Go 1.26+ toolchain**: `install.sh` (next steps) builds `hotbedd` itself.
  (`--with-deps` can fetch and install the toolchain for you.)

### 2 · Get the kit tree

```bash
git clone https://github.com/Chris5011/hotbed.git
cd hotbed
```

Cloning is just the easiest way to fetch: **nothing in the kit or the
daemon runs git at runtime** — any plain copy of the tree works, e.g. the
release page's *Source code (zip / tar.gz)* archive unpacks into exactly
this shape. In daily use you only need the directory; the git history is
for developing.

On a second (e.g. headless) machine, any of these fetches is fine:

```bash
# a) has the CLI tooling and auth →
gh release download vX.Y.Z -R Chris5011/hotbed      # binary + SHA256SUMS
gh repo clone Chris5011/hotbed                      # the tree

# b) minimal: raw curl against the release URLs — needs a token while the
#    repo is private (PAT with repo scope); token-free once public:
curl -fL -H "Authorization: Bearer $TOKEN" \
     -O https://github.com/Chris5011/hotbed/releases/download/vX.Y.Z/hotbedd-vX.Y.Z-linux-amd64

# c) the two boxes reach each other → skip GitHub entirely:
rsync -av --exclude vm/images --exclude vm/vms --exclude dist \
      --exclude hotbed/bin SRC_TREE/ newbox:apps/hotbed/
```

Then `sha256sum -c SHA256SUMS --ignore-missing`, drop the binary into the
tree (`dist/` or the root — `install.sh` adopts either), and continue at
step 3.

### 3 · Run the installer

```bash
./install.sh            # interactive; add --yes to take all defaults
```

On a fresh Debian/Ubuntu box you can do it in one line: the flag below
apt-installs every missing prerequisite first (docker, tmux, git, curl, jq,
ripgrep, ssh, build tools, and the Go toolchain for the source build), then
runs the normal install:

```bash
git clone https://github.com/Chris5011/hotbed.git \
  && cd hotbed && ./install.sh --with-deps --yes
```

It will:

- symlink `hbd`, `hotbedd` and a `hotbedd-serve` wrapper (pins `HOTBED_ROOT` so the
  daemon always finds this tree) into `~/.local/bin`. That dir is on your
  PATH on Debian-family setups once it exists (they auto-add it on the next
  login shell; in an open terminal: `export PATH="$HOME/.local/bin:$PATH"`).
  The installer prints a note if it isn't on PATH; without it, run the
  binaries straight from the tree;
- gate-check the stack above and print exactly what's missing, split into
  required vs. optional;
- offer the one-time Debian master-image fetch (~330 MB download, ~950 MB
  on disk). Skipping is fine — retry any time with `hotbedvm fetch-image`;
- with `--config-init`, import LLM providers from your host opencode
  config (do it later instead: `hotbedd config init`). Mind: it is an
  **import** — on a fresh box without `~/.config/opencode/opencode.json`
  there is nothing to import and it exits with that error. Either copy
  that file (plus `~/.opencode/bin/opencode` and `~/.local/share/opencode`
  for the in-sandbox TUI) over from another machine, or write
  `~/.config/hotbedd/config.json` directly (`hotbedd config print` shows the
  shape). Unconfigured is fine for pure sandbox/VM testing — only the
  accounting proxy stays idle.

### 4 · Start the daemon

```bash
hotbedd-serve
# → dashboard + API on http://127.0.0.1:7770 (loopback by default)
```

`hotbedd-serve` is a plain foreground process — run it in a terminal, a tmux
pane or a systemd unit. The CLIs (`hbd`, `hotbedvm`, `hotbedd`) also work
without the daemon (they fall back to direct engine calls); the dashboard
obviously doesn't.

**On a remote/headless box** the loopback default leaves two, both honest:

- *tunnel* (no exposure): from your workstation, `ssh -L 7770:127.0.0.1:7770
  <box>` and browse `http://127.0.0.1:7770` there; or
- *opt-in LAN exposure*: `HOTBEDD_BIND=0.0.0.0 hotbedd-serve` (or edit the
  `hotbedd-serve` wrapper). Loud ping: **the dashboard/API has no login** —
  the CSRF guard protects browsers only, so with a non-loopback bind your
  *firewall position* is the gate (same trust model as the proxy listener,
  which must be network-reachable for sandboxes anyway). Prefer
  `ufw allow from <trusted-subnet>` style scoping, not world-open ports.
- *snakeoil TLS — makes the dashboard a secure context*: `HOTBEDD_TLS=1
  hotbedd-serve` (or `--tls`). Browsers only grant `navigator.clipboard`
  over https — over plain http on a LAN box the copy buttons degrade to a
  fallback with a pointed toast. The daemon auto-generates a self-signed
  cert and pins it at `~/.local/state/hotbedd/tls/` (fingerprint printed,
  stable across restarts, delete the files to regenerate). Combine with
  `HOTBEDD_BIND` for `https://<box>:7770`.

```bash
curl -s http://127.0.0.1:7770/api/health
# {"docker":true,"libvirt":true, ...}
```

### 5 · Verify + first sandbox

```bash
hbd doctor                # engine, bind mounts, images, opencode bits
hotbedd ps                   # unified inventory (the dashboard shows the same, live)
cd some/project && hbd    # shell in — the stack image builds on first use
```

Point a browser at `http://127.0.0.1:7770`: the sidebar should list your
sandbox (and the `＋ New sandbox` wizard is right next to it).

### 6 · Optional: the accounting proxy

One command wires your providers into the token-accounting proxy, then
per-sandbox keys and $-wallets as needed — full walkthrough in
[P2](#p2--the-llm-proxy-wallets-and-one-global-opencode-config). On a fresh
headless box, remember the step-3 note: `config init` is an import, so
either bring `~/.config/opencode/` over from a machine that has it or
write `~/.config/hotbedd/config.json` by hand.

---

## One-time setup

The scripted [install above](#install-step-by-step) already does
`install-self` and can do the image fetch. If you instead operate the kit
manually, the three things it automates are:

```bash
# put hbd on your PATH (symlinks ~/.local/bin/hbd → this kit's bin/hbd)
hbd install-self

# check the container environment: engine, bind mounts, images, opencode bits
hbd doctor

# fetch + auto-patch the Debian cloud master image for hotbedvm (one-time;
# ~326 MB download, ~950 MB on disk)
hotbedvm fetch-image
```

After this you never need root again for daily work (qemu runs unprivileged
as you; every VM is per-user data).

---

## hbd — container sandboxes

### The idea

`hbd` makes **one persistent, throwaway Docker container per project
directory**. The container name is derived from the project's absolute path
(`hotbed-<stack>-<hash8>`), so re-running `hbd` anywhere in that project
reattaches to the *same* sandbox. Inside it you are a user with **your host
uid**, the project is mounted at `/workspace`, passwordless sudo works
(container-local!), and the internet is reachable — the workflow an agent
expects.

Why persistent: containers keep running detached (`hbd stop` to pause), so
the agent's state survives your terminal and reboots (restart policy
`unless-stopped`), while tooling it installs still dies with `hbd rm`.

### Quick start

```bash
cd ~/some/project
hbd                       # shell in (creates & builds the sandbox on first use)
hbd opencode              # opencode TUI inside the sandbox
hbd --fresh               # one-shot ephemeral sandbox instead; dies on exit
hbd status                # what exists for this project right now
hbd rm --yes              # nuke this project's sandbox and everything in it
```

From inside you (or an agent) can `apt install`, `pip install`, `npm i -g`,
compile, run servers — none of it lands on the host.

### Stacks

| stack    | image        | tooling you get                                 |
|----------|--------------|-------------------------------------------------|
| `node`   | `hotbed:node`   | Node 22 LTS + npm + corepack                    |
| `python` | `hotbed:python` | python3-dev, pipx, uv                           |
| `go`     | `hotbed:go`     | latest stable Go + module cache volume          |
| `rust`   | `hotbed:rust`   | rustup stable, minimal profile                  |
| `base`   | `hotbed:base`   | fallback: git, build tools, python3             |

All stacks also get: git, curl/wget, jq, ripgrep, openssh-client, build
toolchain (gcc/make/pkg-config), python3+pip, sudo (passwordless *inside*
only), and a baked opencode fallback binary (an official release).

Auto detection (`hbd stack` to print; override with `--stack NAME`):

- `go.mod`, `go.work` → go
- `Cargo.toml` → rust
- `package.json` → node
- `pyproject.toml`, `requirements.txt`, `setup.py`, `setup.cfg`, `Pipfile`,
  `uv.lock` → python
- otherwise → base

Images build on first use and are cached afterwards. Rebuild explicitly:
`hbd build [stacks...]`, fully fresh: `hbd --no-cache build`.

### What's mounted in

| host path                        | sandbox path                | mode | purpose                        |
|----------------------------------|-----------------------------|------|--------------------------------|
| the project dir                  | `/workspace`                | rw   | the project (blast radius)     |
| `~/.local/share/opencode`        | `/home/hbd/.local/share/opencode` | rw | opencode state & auth (sessions, logins) |
| `~/.config/opencode`             | `/home/hbd/.config/opencode`| rw   | opencode config                |
| `~/.opencode/bin/opencode`       | `/usr/local/bin/opencode`   | ro   | *your* opencode binary wins over the baked one |
| `~/.gitconfig`                   | `/home/hbd/.gitconfig`      | ro   | commits carry your identity    |
| `~/.ssh`                         | `/home/hbd/.ssh`            | ro   | git push/pull over ssh         |
| named volume `hotbed-npm`           | `/home/hbd/.npm`            | rw   | npm cache (node)               |
| named volume `hotbed-pip`           | `/home/hbd/.cache/pip`      | rw   | pip/uv cache (python)          |
| named volume `hotbed-go`            | `/home/hbd/go`              | rw   | Go cache (go)                  |
| named volume `hotbed-cargo`         | `/usr/local/cargo`          | rw   | cargo cache (rust)             |

Opt out: `--no-auth`, `--no-occonf`, `--no-git`, `--no-ssh`. Add more:
`--mount HOST:CT[:ro]` and `--env KEY=VAL` (repeatable, must come before the
command, e.g. `hbd --mount /data:/data shell`).

Helpers for the strict case: `hbd --fresh --no-auth --no-ssh --net none`.

### Command reference

```text
hbd (no args) | shell     interactive shell in this project's sandbox
hbd opencode [-- args]    opencode TUI inside the sandbox
hbd exec [--] cmd...      one-shot command in the sandbox (tty-aware)
hbd root [--] cmd...      as root inside the sandbox (apt-get update ...)
hbd status | stop | rm    manage THIS project's sandbox (--yes skips prompt)
hbd ls | ps               all hbd sandboxes (stack, state, project)
hbd prune [--yes]         remove all hbd containers (asks, or --yes)
hbd caches [ls|nuke]      list / wipe hotbed-* cache volumes (--yes)
hbd build [stacks...]     (re)build images; hbd --no-cache build to be sure
hbd stack                 print the auto-detected stack for this cwd
hbd doctor                environment diagnosis
hbd --fresh [cmd...]      ephemeral mode: --rm container, dies on exit
hbd install-self          symlink into ~/.local/bin
hbd version | help        meta
```

Global options (place **before** the command): `--stack NAME`, `--fresh`,
`--net MODE`, `--no-auth`, `--no-occonf`, `--no-git`, `--no-ssh`,
`--mount HOST:CT[:ro]`, `--env KEY=VAL`, `--yes`, `--no-cache`.

---

## hotbedvm — the VM escape hatch

Some things a container can't do well: systemd services, nested Docker,
kernel modules, anything needing "a real machine". `hotbedvm` creates headless
**KVM-accelerated Debian 13 VMs** through libvirt/virt-install, provisioned
tail-to-back by cloud-init. The same CLI also runs a second backend with
**no libvirt at all** — plain Docker containers with qemu inside — see
[the docker backend](#the-docker-backend-hotbedvmd).

### What you get

- SSH in as user `dev` with a dedicated auto-generated key
  (`~/.ssh/hotbedvm_key`), **passwordless sudo**, docker group membership.
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
hotbedvm create agent1 --cpu 2 --mem 4096 --disk 20 --share ~/projects/agent1
#   ~2–3 min: boot → cloud-init provisions → automatic reboot into the full
#   kernel → /workspace mount verified, then returns control

hotbedvm ssh agent1                          # shell inside
hotbedvm opencode agent1                     # opencode TUI (cwd /workspace)
hotbedvm snapshot agent1 save before-agent   # freeze the moment
hotbedvm ssh agent1                          # agent does its (messy) work…
hotbedvm snapshot agent1 revert before-agent # …and the machine goes back in time
hotbedvm start agent1
hotbedvm destroy agent1 --yes                # reclaim everything
```

`create` defaults: `--cpu 4 --mem 6144 --disk 20`. Name rules:
`^[a-zA-Z][a-zA-Z0-9._-]*$`; the libvirt domain is `hotbedvm-<name>`, all VM data
lives in `vm/vms/<name>/`.

### The provisioning pipeline (what `create` runs)

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

Since v0.2.10 the pipeline also **stages opencode from the host** over its
own ssh channel when the guest's egress can't reach GitHub (ICMP+DNS fine,
TCP 443 blackholed — seen behind policy routers/VPNs), and **self-syncs the
hbd proxy** so fresh guests get their model list immediately. Since
v0.2.11 the dashboard's create returns **the moment the machine is
spawned** (`HOTBEDVM_SPAWN_ONLY=1` + a `postboot` verb) and the post-boot
half runs in the daemon's background — any VM reaching boot-end without a
sync target is wired automatically exactly once (audited `sandbox.sync`).

Tip: no `--share`? Then the VM works in a VM-local `/workspace` (a
container-free machine for experiments); live snapshots then also work.

### Getting files in and out

```bash
# host → VM (rsync over the VM's SSH; the key and the lease IP do the rest)
rsync -av -e "ssh -i ~/.ssh/hotbedvm_key" ~/my/dir/ \
      "dev@$(hotbedvm ip agent1):/workspace/mydir/"

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
  `hotbedvm start <name>` (a message in the output reminds you).
- `destroy` refuses nothing: it stops the VM, deletes *all* snapshots first
  (virsh otherwise refuses to undefine a domain with snapshots), undefines
  with NVRAM cleanup and removes `vm/vms/<name>/` entirely.

### Command reference

```text
hotbedvm fetch-image                 download + auto-patch the master image (once)
hotbedvm create NAME [opts]          create + fully provision a VM
      [--cpu N] [--mem MB] [--disk GB] [--share DIR]
hotbedvm ssh NAME [-] command        ssh as dev@ (interactive shell w/o command)
hotbedvm shell NAME                  alias of ssh
hotbedvm opencode NAME [--] args     opencode TUI inside the VM (cwd /workspace)
hotbedvm ip NAME                     current lease IP (from virsh domifaddr)
hotbedvm start | stop NAME           power control (stop = ACPI, then forced)
hotbedvm destroy NAME [--yes         delete VM: stop + snapshots + disk + dir
        required when non-interactive]
hotbedvm ls                          list VMs: state, IP, snapshot count
hotbedvm snapshot NAME save|list|revert|delete [...]
hotbedvm console NAME                attach the serial console (needs a real TTY)
hotbedvm version | help              meta
```

Env flag: `HOTBEDVM_SERIAL_FILE=1` before `create` logs the serial console to
`vm/vms/<name>/serial.log` (a mode-0666 file readable by you) instead of
only a pty.

---

## The docker backend (hotbedvmd)

The same `hotbedvm` CLI can drive VMs **without libvirt at all**: each VM is one
plain Docker container running qemu. Select it per invocation:

```bash
HOTBEDVM_BACKEND=docker hotbedvm version        # hotbedvm 0.1.0 (backend: docker)
HOTBEDVM_BACKEND=docker hotbedvm create demo --cpu 2 --mem 2048 --disk 12 \
    --share ~/projects/demo
HOTBEDVM_BACKEND=docker hotbedvm ssh demo
```

(or `export HOTBEDVM_BACKEND=docker` once per shell. **libvirt stays the
default** so the original path is never switched by accident.) The same
command reports which backend a given invocation will use:
`HOTBEDVM_BACKEND=docker hotbedvm version` → `(backend: docker)`.

### Shape

- **One container = one VM** (`hotbedvmd-<name>`, `--restart unless-stopped`).
  Lifecycle is plain `docker run/stop/rm`; `stop` traps SIGTERM in PID-1 and
  hands the guest an ACPI shutdown (then a hard kill after 50 s — a stopped
  guest always exits cleanly).
- **State lives in a named volume** (`hotbedvmd-store`), not in bind mounts: the
  patched master (`hotbedvm fetch-image`, the same one-time fetch) copied in as
  `/store/master.qcow2`, and per-VM
  `/store/vms/<name>/{disk.qcow2, seed.iso, serial.log}` — the qcow2 gets
  native ext4 I/O inside the engine instead of the Desktop bind-mount tax.
- The runtime image `hotbedvmd:latest` is **built automatically** on first
  `create` (Debian + qemu + xorriso; sources in `vm/hotbedvmd/`). The host side
  writes the seed files as usual into `vm/vms/<name>/seedwork/` and leaves
  the rest to the container: the seed dir is bind-mounted read-only at
  `/seed-src` and packed into `seed.iso` *inside* (no host xorriso).
- **SSH is a published port, not a DHCP lease**: the container publishes
  `127.0.0.1::<random>` and **that port changes on every stop/start** (and on
  the restart each snapshot save does). `hotbedvm ssh|ip|ls` re-resolve it via
  `docker port`, so always go through the CLI; `hotbedvm ip demo` prints
  `127.0.0.1:<port>` for hand-rolled ssh/rsync.
- `--share DIR` is bind-mounted at `/share` in the container and exposed to
  the guest as the same 9p virtfs as the libvirt path (`hostshare` →
  `/workspace`).
- Serial console: `HOTBEDVM_BACKEND=docker hotbedvm console demo` streams it via
  `docker exec` (`hotbedvmd-entry serial`); it also lands in
  `/store/vms/demo/serial.log` (rotated to `.1` per boot). Debug helpers:
  `docker logs hotbedvmd-demo` (PID-1 side) and a qemu monitor under the hood —
  `docker exec hotbedvmd-demo hotbedvmd-entry ctl 'info status'`.
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
and **leaves it stopped** — follow with `hotbedvm start <name>` (the CLI says
so); `list` reads with `-U` while the VM runs. RAM state is never captured —
the same trade-off a 9p-shared VM has on libvirt.

---

## hotbedd — Hotbed (daemon, dashboard, docker-style CLI)

`hotbedd` is the resident control plane for the whole kit: one Go binary that
serves a **unified inventory** of every sandbox kind (bin/hbd containers,
hotbedvmd docker-backend VMs, libvirt domains), a **web dashboard** and a
**docker-lexicon CLI** — GUI and CLI are both clients of the same REST/SSE
API. No SDKs: engines are driven through `docker`/`virsh` and the kit's own
CLIs, so the binary stays stdlib-only and single-file.

```text
        Windows Terminal / browser / Electron shell
              │  (quick-connect, copy-connection)
┌─────────────▼────────────────────────────────────────┐
│ hotbedd — localhost-only daemon (default port 7770)     │
│  • inventory scan: docker ps/stats, virsh domstats   │
│  • REST + SSE API for dashboard & CLI                │
│  • attach desk: one persistent tmux session each     │
│  • embedded dashboard (no build step, vanilla JS)    │
│    opens onto an Overview start page — host CPU /    │
│    RAM / disk / uptime + live inventory counts       │
│  • LLM proxy :7775 — token & dollar accounting       │
│    (the ONLY listener reachable from sandboxes)      │
└──────────────────────────────────────────────────────┘
```

### Quick start

```bash
cd hotbedd && go build -o bin/hotbedd ./cmd/hotbedd
./bin/hotbedd serve            # dashboard at http://127.0.0.1:7770
./bin/hotbedd ps               # unified table (containers + both VM backends)
./bin/hotbedd stats            # with live CPU/MEM (docker stats / virsh domstats)
./bin/hotbedd attach smoke3    # persistent tmux session, shell inside (detach: Ctrl-b d)
./bin/hotbedd attach agent2 --oc   # same session but with the opencode TUI
./bin/hotbedd connect smoke3   # print connection variants (ssh/shell first)
./bin/hotbedd logs smoke3      # docker logs / serial / file-tail, streamed
./bin/hotbedd start|stop NAME  # engine start/stop (docker / kit hotbedvm semantics)
./bin/hotbedd rm NAME [--yes]  # fully delete a sandbox (VM: disk+snapshots w/ --yes)
./bin/hotbedd doctor
```

CLI verbs talk to the daemon when it runs (`HOTBEDD_PORT`, default 7770) and
fall back to direct engine calls otherwise.

### The desktop shell

`hotbed/desktop/` is a thin Electron wrapper (~200 lines of `main.js`): a
native window pointing at the dashboard, which auto-boots `hotbedd` inside WSL
via `wsl.exe` if unreachable — Docker-Desktop-style. The webapp itself stays
portable: any browser gets the identical UI at the same URL. Run instructions
in `hotbed/desktop/README.md`.

### P2 — the LLM proxy, wallets and the LLM providers config

P2 turns hotbedd into the **single money-aware gate** for every agent call a
sandbox makes. The upstream provider key never leaves hotbedd; sandboxes hold
only minted `hbk-…` **virtual keys** — one per sandbox × agent × provider
(`hbk-codex-openwebui-…`, `hbk-opencode-openwebui-…`) — which the proxy
translates to the real provider key. Every call is booked to the key's
sandbox *and* agent: the Burn views and `hotbedd usage NAME` split usage per
agent, `hotbedd key ls` lists the keys with their agent. Wallets stay per
sandbox (USD/day, tokens/day) and are enforced *before* any request is
forwarded. Keys minted before per-agent keys (or with `hotbedd key mint`)
show as "shared key"; a re-sync gives every agent its own. The providers
themselves live on the dashboard's **LLM providers** page (formerly
"OpenCode config").

```text
sandbox (opencode TUI)                                   
  │ Authorization: Bearer hbk-<sandbox>-<rand>          
  ▼                                                      
hotbedd proxy :7775  ── 401 unknown/revoked key        
  │     ┝─ 429 wallet dry (today ≥ budget)          
  │     ┝─ re-auth shoulder: real provider key never in the sandbox
  ▼                                                      
upstream (openai or anthropic style) ── SSE streams pass
through verbatim; usage parsed from final chunk /
message_start/_delta, else estimated (bytes/4, est=true)
  ▼
~/.local/state/hotbedd/usage.jsonl  →  burn dashboard tab
```

Setup, once:

```bash
./bin/hotbedd config init     # import providers from host ~/.config/opencode/opencode.json
./bin/hotbedd key mint NAME   # per-sandbox proxy key (provider default)
./bin/hotbedd budget NAME 5   # $/day wallet (0 = unlimited, default)
./bin/hotbedd sync NAME       # push proxy-backed opencode config INTO the sandbox
#   or in the dashboard: Burn tab → per-sandbox sync button
#   (VMs wire this up automatically at boot-end since v0.2.11)
```

After a sync, the sandbox's opencode only ever sees one provider
(`hotbed-proxy`) whose `apiKey` is the sandboxes' minted key — edit the global
config at `~/.config/hotbedd/config.json` (`hotbedd config edit` searches for open
editors, `hotbedd config set proxy.port 7775` etc.) and **running daemons pick
it up without a restart** (stores are stateless file views; the proxy
re-reads config on every request).

#### Reachability: the in-engine proxy relay (Docker Desktop)

On Docker Desktop engines the containers run in a separate utility VM whose
sandboxes *cannot* reach the WSL distro's proxy listener on any route
(distro IP, bridge gateway or `host.docker.internal`); hotbedd fixes this
instead of failing loudly:

- the **first container sync** (or daemon boot) spawns the relay, a
  `--network host` container running this same binary in a dedicated
  **`hotbedd proxy`** (standalone, dashboard-less) mode, with the daemon's
  config/keys/state dirs bind-mounted (hotbed:base image or any local glibc
  base; `--restart unless-stopped`; host-uid — it is **not** labeled
  `dev.hotbed`, so it never shows in the inventory);
- the relay name is **per state set** (`sbx-relay-<confighash>`,
  overridable with `HOTBEDD_RELAY_NAME`) — two hotbedd processes (a real daemon
  and a hermetic test suite) can run side by side without adopting or
  destroying each other's relay; a pre-name-spacing `hotbed-proxy-relay` is
  migrated on first spawn (`relay.migrate` in the audit trail);
- the sandbox then dials `http://<bridge-gateway>:<proxy-port>/v1`
  (typically `172.17.0.1:7775`) — reachable by construction;
- the relay re-reads config/keys per request: later `key mint`, wallet
  edits and `budget` changes apply live; usage receipts append to the same
  `usage.jsonl` the dashboard reads (shared `/state` mount);
- the relay is **owned by its daemon** (mount sources = its state dirs): a
  foreign relay under this daemon's own name is replaced automatically
  (`relay.replace` in the audit trail); a lost relay (engine restart, etc.)
  is restored on daemon boot;
- `doctor` shows the relay line on Docker Desktop engines ("proxy relay: …
  (running at gw …)"), every declined/failed relay attempt is audited
  (`relay.skip` with the reason), and `HOTBEDD_RELAY_DISABLE=1` turns the
  feature off (non-Docker-Desktop engines never spawn it: native dockerd,
  bridge-mode WSL setups and VM sandboxes have host-reachable routes
  already).

The sweeper only ever stops idle sandboxes its own daemon spawned: kit
containers carry a `dev.hotbed.owner` label (stable hash of the spawning
daemon's config dir) and foreign-owned sandboxes are skipped — a hermetic
test daemon can never idle-stop the user's real sandboxes.

What lives where (all local, all `chmod 600`, env-overridable for tests):

| Path (default) | Contents |
|---|---|
| `~/.config/hotbedd/config.json` | providers (base_url + **upstream key**), default model, proxy port/bind, `prices` overrides |
| `~/.local/share/hotbedd/keys.json` | virtual keys: one `hbk-<agent>-<provider>-…` token per sandbox × agent × provider |
| `~/.local/share/hotbedd/synced/<name>.json` | what each sandbox was last synced with |
| `~/.local/state/hotbedd/usage.jsonl` | one line per proxied call: tokens in/out, cost, est flag, dur, status |
| `~/.local/state/hotbedd/tunnel-<name>.pid/.log` | the ssh -R tunnels keeping guest loopback on the proxy |

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
opencode config; hotbedd verifies it actually answers before):

| Sandbox kind | Path | base_url |
|---|---|---|
| VMs (hotbedvmd, libvirt, docker-in-docker) | persistent `ssh -R` tunnel, kept alive by hotbedd (pid/log in state dir) | `http://127.0.0.1:7775/v1` |
| Containers (probed in order) | WSL eth0 IP → `host.docker.internal` | first that answers |
| nothing answers | sync reports the failure + how to fix (e.g. kit `HBD_NET=host`) | — |

The tunnel belongs to the daemon: it respawns automatically if ssh drops or
hotbedd restarts. Note on Docker-Desktop WSL setups: containers cannot reach
WSL-side ports directly (they run in a separate VM), so VMs always go through
their ssh tunnel and plain containers either reach the probed route or tell
you what to change at creation time.

New CLI verbs (all file-backed — work with or without the daemon):

```bash
./bin/hotbedd config init|print|get|set|path|edit
./bin/hotbedd usage [NAME]      # global per-sandbox table, or a full drill-down
./bin/hotbedd key mint|ls|revoke NAME
./bin/hotbedd budget NAME [USD] # show or set; proxy trips 429 when dry
./bin/hotbedd sync NAME|all [--provider P]
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

**Counted vs uncounted.** The proxy and ledger always count what reaches
them; a sandbox shows zero rows only when its opencode bypasses the proxy.
Every request must carry both (a) the synced proxy config (`hotbedd sync
NAME` writes a `hotbed-proxy` provider with the sandbox's own key) and (b)
the attach-time `XDG_CONFIG_HOME` override that makes opencode read it.
Attach, the one-shot connection variants, Windows Terminal and the kit's
own `hbd shell|opencode|exec` all inject the override for containers; VM
guests get the config written directly and a persistent ssh tunnel. If a
session predates a later sync, its next attach respawns the (bare) shell
pane automatically so the env lands - a running opencode TUI is never
respawned; close it and re-attach once to switch to counted mode.

### P3 — recipes, config-file budgets, mounts, egress, sweeper, audit, create wizard

P3 turns hotbedd from observer into **operator**: everything the kit used to
only do through `bin/hbd` becomes declarative and daemon-managed, with an
audit trail for the control plane.

**Everything settable, lives in `config.json`** (`hotbedd config edit`):
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
  "budgets":       { "myproj": 5, "hotbed-side-tests": 0 },    // USD wallets
  "budget_tokens": { "myproj": 250000, "hotbed-side-tests": 0 }, // token wallets
  "sweeper":       { "idle_stop_minutes": 45 }   // auto-stop dead sandboxes
}
```

`hotbedd budgets` shows every wallet against actual burn; `hotbedd config set
sweeper.idle_stop_minutes N` re-reads live — no restart. The **sweeper**
(daemon goroutine, samples every 30s) stops kit-labeled sandboxes whose CPU
stayed under 1% for the configured window (VM-backed `hotbedvmd-*` containers
are always spared); reversible with `hotbedd start NAME`; every stop lands in
the audit trail.

**Recipes — declarative sandboxes** (`hbd.yaml` next to your project):

```yaml
name: myproj          # container becomes hotbed-myproj
stack: go             # needs the kit image once: bin/hbd build go
mounts:
  - ~/projects/core:/workspace/core   # ~ expands; ":ro" suffix keeps it read-only
  - /var/data/cache:/cache:ro
env:
  FOO: bar
ports: ["8080:8080"]  # published for real at container create
budget: 5             # USD wallet
budget_tokens: 250000
egress: proxy-only    # config-time fence, see below
sync: true            # opencode → hbd proxy wiring after spawn
attach: --oc          # extra flags for the suggested attach command
```

```bash
./bin/hotbedd recipe print hbd.yaml     # honest plan of the exact spawn
./bin/hotbedd recipe validate hbd.yaml  # checks: name/stack/dup targets/egress/budgets
./bin/hotbedd recipe up hbd.yaml [--yes]  # create+start → wallets → egress → sync
```

`recipe up` recreates the kit's container contract faithfully (dev.hotbed
labels, opencode auth mounts, git/ssh identity read-only, per-stack package
cache volume, restart policy) — so `bin/hbd attach hotbed-myproj` works
unchanged. Duplicate names are refused; non-interactive runs need `--yes`.

**Mounts after the fact** (`hotbedd mount`): `mount ls NAME` / `mount add NAME
/h:/x[:ro] --yes` / `mount rm NAME /x --yes`. Docker bakes volumes at
creation, so add/rm does a guarded recreate (same image, same flags,
original mounts carried, state in named volumes/paths keeps) and skipping
`--yes` in a non-TTY means "do nothing". VM-backed `hotbedvmd-*` containers
refuse live recreation with an explanation.

**Egress presets** — a convenience fence (not a security boundary):
`hotbedd egress NAME proxy-only` fences public addresses out of the sandbox
netns (iptables OUTPUT chain HBD_EGRESS; loopback, established flows, RFC1918
and DNS stay open) so LLM traffic must ride the hbd proxy on the host.
`hotbedd egress NAME none` is stricter: only loopback+established stay —
no DNS, no private ranges, everything else drops. `allow` resets to the
Docker default. Needs no restart: rules live in the netns, so re-apply
after any recreate. Containers that already run iptables get it via plain
exec; everything else uses a tiny auto-built helper image (`hotbedd-egress-helper:1`, built once on first use).

Their difference in one sentence: **none** cuts a sandbox off everything
except its own loopback (even DNS), while **proxy-only** additionally
leaves the private/docker-internal network and plain DNS reachable so the
host-side burn-ledger proxy works while the public internet still drops.

**Domain allowlist** (`allowlist` preset, host-based egress firewall):
`hotbedd egress NAME allowlist api.openai.com example.org` stores the
sandbox's domain list in `config.json` (`egress_allowlist`) and installs
a stricter fence: all of the sandbox's DNS is DNAT'ed into the fence
listener inside the docker engine netns (`gw:15353`, part of the daemon /
proxy relay — no extra container), which only answers names on the
sandbox's list. Every IPv4 the fence answers is installed as an ACCEPT in
the sandbox's netns (domains are pre-resolved at install time, then kept
fresh by the daemon's egress keeper); everything not on the list gets a
synthesized REFUSED and stays dropped. Unknown sandbox sources and DNS
bypasses fail closed. DoH/DoT workarounds (unlisted hosts on port 443)
drop with everything else. Stop/start wipes the netns rules; the daemon
re-applies every preset it recorded (`egress_mode` in config) within ~30 s.
Dashboard create wizard offers it, plus `hbd` recipes via
`egress: allowlist` + `egress_allowlist` (in the recipe or in config).
An allowlist may start empty — only the hotbed proxy is reachable then.

**DNS tracking** (`allow` and `allowlist` presets): the fence records every
lookup of a tracked sandbox into `state/dns-seen/<name>.json` — domain,
lookups, refused count, first/last seen. With `allowlist` that shows what
the fence blocked; with `allow` the sandbox's DNS is routed through the
fence in pass-through mode, so you see where the agent goes while nothing
is blocked. The sandbox's **DNS tab** in the dashboard lists the domains
with an allow toggle each (live: the fence re-reads the list per lookup,
removing an entry rebuilds the chain), plus *Lock down to allowlist* to
turn an observed `allow` sandbox into a fenced one with exactly the
toggled domains. Enable it in the create wizard (checkbox, on by
default), with `dns_track: true` in a recipe, or:

```bash
hotbedd egress NAME allow --track        # open egress, lookups recorded
hotbedd egress NAME seen                  # what it looked up (status, counts)
hotbedd egress NAME add api.example.com   # allow afterwards (rm to revoke)
hotbedd egress NAME allowlist             # lock down to the collected list
hotbedd egress NAME track off
```

Tracking routes DNS to the fence's upstream (`HOTBEDD_DNS_UPSTREAM`,
default `1.1.1.1:53`) instead of Docker's resolver — set it if your network
requires its own DNS.

**VMs get the same fences, tracking and DNS tab** (create wizard, DNS tab
toggles, `hotbedd egress VMNAME …`). Where the rules live differs:

| Sandbox | Rules in | Fence reached at | Notes |
|---|---|---|---|
| container | container netns | docker bridge gw (relay on Docker Desktop) | outside the sandbox's control |
| VM · docker (`hotbedvmd`) | the VM's `hotbedvmd-<name>` container netns | docker bridge gw | qemu user-mode net: every guest packet leaves through that netns — outside the guest |
| VM · libvirt (`hotbedvm`) | **inside the guest** (ssh + sudo) | guest's default gateway → the daemon's own fence listener | host iptables would need root; root in the guest can lift the fence |

The proxy keeps working under every preset (VMs reach it through their
ssh tunnel on guest loopback). Restarts/reboots wipe the rules; the daemon
re-applies them (containers and docker VMs: StartedAt changes; libvirt:
the guest's boot id). Docker inside a VM routes its containers via FORWARD,
not OUTPUT — nested containers are not fenced.

**Audit trail** — `state/audit.jsonl` records the control plane: sandbox
start/stop/spawn, key mint/revoke, wallet changes, mount recreates, egress
changes, sweeper stops. `hotbedd audit [N]` renders the last N as a table.
Best-effort by design: a failed audit write never blocks the action.

**Create wizard** — sidebar `＋ New sandbox` on the dashboard (identical
POST `/api/create` API): a sectioned form (basics / network / environment /
mounts / opencode / budgets) with the same validation as the CLI (unique
name, absolute targets, known stacks/presets) and the same kit-faithful
container shape. ENV is a dynamic key→value row editor (Portainer-style),
ports are host→container rows, and mount sources can be picked via a real
folder-browser pop-up (`GET /api/fs` lists directories under `$HOME` —
loopback-only dashboard, traversal outside home is rejected).

### Agent modules — other coding agents next to OpenCode

Coding agents are drop-in folders: `agents/<id>/` in the kit (a folder with
the same id under `~/.config/hotbedd/agents/` overrides it or adds your
own). Shipped: **Aider**, **Claude Code**, **Codex CLI**, **Hermes
Agent**, **OpenClaw**, **OpenCode** (built in, on by default). Toggle them
on the dashboard's *Agent modules* page (or `hotbedd agents enable ID`);
enabled ones appear as checkboxes in the create wizard — for containers
and both VM backends. Every sandbox has an **Agents** tab: each module
with its state there (installed / available / module off), *Install* for
enabled ones, *Open in tmux* for installed ones (own tmux session per
agent; the header also gets one "*Name* in tmux" button each) and a
re-sync of the proxy wiring. CLI: `hotbedd agents add SANDBOX ID`.

Where they run: containers via `docker exec` (installed right after the
spawn, before the egress fence); VMs (`hotbedvm` libvirt and `hotbedvmd`
docker backend) over the kit's ssh channel as the guest user `dev`,
installed once post-boot has finished and wired to the proxy through the
VM's ssh tunnel (`127.0.0.1:7775`).

A module is two files:

```jsonc
// agents/<id>/agent.json
{
  "name": "Codex CLI",
  "command": "codex",                       // what the launch runs
  "proxy": {                                // optional: wire to the hotbed proxy
    "style": "openai",                      // provider wire style to pick (openai|anthropic)
    "env":   { "HOTBED_PROXY_KEY": "{{key}}" },
    "files": { "~/.config/x/config.toml": "base_url = \"{{base}}\"\nmodel = \"{{model}}\"\n" },
    "setup": "x config set base_url {{base}}"  // optional: runs as the sandbox user after
                                              // env+files, values shell-quoted (Hermes, OpenClaw)
  },
  "resume": "codex resume --last",          // continue the last conversation (Agents tab "Resume last")
  "persist": {                              // keep conversations in the workspace ({{state}})
    "env":   { "CODEX_HOME": "{{state}}" },          // agents with a relocation variable
    "links": { "~/.x/sessions": "sessions/",         // the rest: home path -> symlink into
               "~/.x/state.db": "state.db" }         // {{state}} (trailing / = folder)
  },
  "egress_domains": []                      // added to allowlist sandboxes at create
}
```

`install.sh` runs as root inside the sandbox, with `agents/_lib.sh` in
front (`ensure_node [MIN]`, `npm_global`, `ensure_uv`, `as_user` for
per-user installers like Hermes); in containers it runs **before** the
egress fence goes up. The marker `/etc/hotbed/agents/<id>` records the
install — it dies with the container. On sync, hotbedd picks a provider of
the module's style, mints that sandbox's `hbk-` key for it and renders the
templates (`{{base}}` = `…/v1`, `{{base_root}}`, `{{key}}`, `{{model}}`,
`{{models_json}}` = every provider model as a JSON array (default first),
`{{models_objects}}` = the same as `[{"id","name"}]`, `{{provider}}`,
`{{home}}`; a template whose value is unknown is left out)
into `~/.config/hotbed/agents/<id>.env` + the listed files — so every
agent's calls land in the same burn ledger and wallets. Without a provider
of the right style (e.g. Claude Code needs an `anthropic`-style one) the
agent is still installed and can be used with its own login.

**Conversations live in the workspace.** Every agent keeps its sessions in
`/workspace/.hotbed/agents/<id>/` (a `.gitignore` in `.hotbed/` keeps them
out of your commits), so they survive tearing the sandbox down: mount the
same project folder into a new sandbox, install the agent, and *Resume
last* (Agents tab) continues where you left off. How each agent is pointed
there: OpenCode `OPENCODE_DB` (injected into every OpenCode start — its
background service keeps the first DB path it saw), Claude Code
`CLAUDE_CONFIG_DIR`, Codex `CODEX_HOME`, Aider history files, Hermes and
OpenClaw via symlinks for their session stores. Existing home-folder
sessions are moved into the workspace on the first sync; a workspace
without a host mount simply keeps them inside the sandbox.

Virtual keys follow the workspace too: each project folder gets an id
(`/workspace/.hotbed/id`), and a new sandbox on a folder whose previous
sandbox was deleted gets that agent's previous key back (re-activated,
re-bound — burn and wallets count for the new sandbox). Agents that bind
a session to its credential (Hermes) can only resume that way; a folder
mounted into two live sandboxes at once still gets one key per sandbox.
Hermes references the key as `${HOTBED_PROXY_KEY}`, so its `config.yaml`
in the workspace never contains a token.

**Local / self-hosted models.** All six shipped agents run against an
OpenAI-style gateway in front of open models (tested: Bifrost → vLLM,
Qwen3.8-27B). Claude Code needs an `anthropic`-style provider: point one at
the gateway's Anthropic Messages route and switch on the proxy's compat
rewrite for it —

```jsonc
"openwebui-anthropic": {
  "base_url": "https://<gateway>/anthropic/v1", "api_key": "…", "style": "anthropic",
  "models": ["vllm/Qwen3.8-27B-FP8", "…"],
  "compat": ["anthropic-gateway"]   // system messages mid-conversation → top-level
                                     // system; effort "high" → "xhigh" (open-model
                                     // chat templates / gateway effort sets reject both)
}
```

Every Claude model alias (Haiku/Sonnet/Opus) maps to the synced model.
Upstream errors that report no usage are recorded as calls with zero
tokens (no bytes/4 estimate), so a client retrying a 400 can't drain a
wallet.

Notes: current Codex only speaks the Responses API (`/v1/responses`) —
the upstream must serve it. OpenClaw runs as `openclaw chat` (local
embedded TUI, no gateway service) and needs Node 24 (provisioned). Hermes
installs per user with its own pinned Python (~2 min) and asks a one-time
telemetry question on first start (sharing is off by default either way).
A VM created with agents installs them in the background after boot — a
daemon restart in that window drops the queue (use `＋ Agent`).

### House rules & security posture (P2 included)


- Dashboard binds loopback by default (`127.0.0.1`, `--bind`/`HOTBEDD_BIND`
  is explicit opt-in for network reachability), never touches
  `~/.ssh/hotbedvm_key`, never falls back to a TCG-only engine.
- The proxy **must** be reachable from sandboxes, so it binds `0.0.0.0:7775`
  — the rest is gated: every request needs a minted, non-revoked `hbk-…`
  key (401 otherwise), wallets can return 429, and config/keys files are
  `0600`. Upstream provider keys live only in hotbedd's config file and are
  rewritten into the outbound request server-side.
- A synced sandbox config is written into a *shared-write* path the kit
  already mounts for VMs, or a container-only XDG shim path — never over a
  location the kit mounts writable for other purposes.

#### Not on the roadmap by design

Two "obvious" desktop-type features are deliberately not part of HBD
Desktop, recorded here so the positioning is explicit:

- **Shared-host user/permission concept** — deliberately not by default.
  If ever built, it is an opt-in *shared host* mode that needs: API token
  auth (loopback-trust alone no longer suffices), a per-user sandbox name
  namespace with list filtering, per-user proxy wallets and burn ledgers,
  and host folder mounts disabled outright (a shared folder is the first
  cross-user leak). Anything less is a false sense of multi-user safety.
- **Hosted server-mode** (Hotbed as SaaS for teams) is rejected as
  native multi-tenancy — that is Gitpod/Codespaces territory, not a
  desktop tool's. The sensible middle path if remote use is wanted:
  *remote-daemon mode* — keep the single-tenant daemon, point a local UI/CLI
  at your own server over the API, and have project-dir push at create
  time replace the folder binding.

### Sequel phases

- **P1 (shipped)**: inventory + stats/inspect, logs/serial, persistent tmux
  attach desk + quick-connect/copy-connection, doctor, dashboard, desktop shell.
- **P2 (shipped)**: token-accounting LLM proxy — per-sandbox keys, per-sandbox
  burn/cost ledger, budget ceilings, SSE-aware token counting — plus the
  global OpenCode settings file pushed into sandboxes, with verified
  reachability paths per sandbox kind (ssh tunnel for VMs, probe for containers).
- **P3 (shipped)**: recipes (`hbd.yaml` print/validate/up), wallets + token
  pricing + sweeper knob in `config.json`, mounts after creation (guarded
  recreate), egress presets (iptables in the sandbox netns with helper
  fallback), end-to-end control-plane audit trail + `hotbedd audit`, the
  dashboard create wizard (POST `/api/create`) and tokens in the burn view.
  The create wizard now covers **all three vessels**: `container` (hbd) and
  both VM kinds (`vm` + `vm_backend: libvirt|docker`) — kind radios, VM
  sizing (vCPU/MEM/disk/optional 9p share dir), backend auto-picked by
  engine reachability, and duplicate/charset validation matching the kit's
  own rules (`hotbedvm.sh create` runs underneath, so provisioning semantics
  are identical to the CLI). Start/Stop/Delete in the dashboard and CLI
  (`hotbedd start|stop|rm NAME`) already span all three kinds.

- **VM boot self-registration**: the kit signals the daemon when a guest
  boot starts (`POST /api/vm/boot {"name":…,"phase":"begin"}`) and again
  once it is truly up (ssh + cloud-init provisioning confirmed;
  `"phase":"end"`). Between the two signals the dashboard shows an amber
  **booting** state instead of the engine's premature `running`, attach is
  politely refused with a hint, and endpoint security is the daemon's own:
  loopback-by-default listener (remote exposure is an explicit `HOTBEDD_BIND`
  opt-in) + CSRF origin guard (the kit's curl has no Origin
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
- The image bakes user `hbd` (uid 1000); at **runtime** the entrypoint
  (`containers/entrypoint.sh`) creates/adjusts a user matching your *actual*
  host uid (`HBD_UID`/`HBD_GID`), grants passwordless sudo, fixes ownership
  of `$HOME`/cargo registry if stale (only when needed), then `setpriv`s to
  your uid and execs the command. Persistent mode (no command) execs
  `sleep infinity` so you attach/detach at will.
- No side effects on the host beyond the bind mounts + named cache volumes.

### VM side

- **Master image**: official `debian-13-genericcloud-amd64.qcow2`
  (cloud.debian.org, trixie). `fetch-image` → downloads → **patches** → puts
  a `.hotbedvm-patched` marker next to it, so patching is idempotent and
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
  the host talks to the VM via SSH (`hotbedvm ip` reads the dhcp lease).
- **9p share**: `--filesystem type=mount,…,target=hostshare` +
  accessmode `mapped`. Guest side mounts it with a systemd `.mount` unit
  (`options=trans=virtio,version=9p2000.L`; requested `msize=104857600`
  negotiates down to ~512 KiB — normal virtio behavior).
  Host-side ownership lands as `you:libvirt-qemu` because qemu runs as you.

---

## Field debugging recipes

These recipes were hard-won — they live here so you don't have to rediscover
them:

**Capture the serial console** (what `hotbedvm console` needs a TTY for):

```bash
PTY=$(virsh --connect qemu:///system ttyconsole hotbedvm-<name>)
timeout 8 cat "$PTY"          # read
printf '\n' > "$PTY"          # press Enter, if you must talk to a getty
```

**VM reset-loops at boot** (serial shows endless `"Booting …"`):
the master image lost its patch. Check & re-patch:

```bash
ls vm/images/*.hotbedvm-patched || hotbedvm fetch-image   # re-fetch (auto-patches)
# boot shape (outside libvirt, in vm/.work — NOT /tmp, it's a RAM tmpfs):
mkdir -p vm/.work && cd vm/.work
qemu-img convert -O raw ../images/debian-13-genericcloud-amd64.qcow2 v.raw
S=$(fdisk -l v.raw | awk '/Linux root/ {print $2; exit}')   # rootfs sector
dd if=v.raw of=v.ext4 bs=512 skip=$S status=none
G=$(debugfs -R "cat /boot/grub/grub.cfg" v.ext4 2>/dev/null)
echo "$G" | grep -cE '^[[:space:]]*load_video[[:space:]]*$'    # want: 0 (unpatched: 4)
echo "$G" | grep -c  'disabled by hotbedvm'                       # want: 4 (the strips)
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

**"ssh did not come up"**: check `hotbedvm ls` for the lease IP, and the serial
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
[the docker backend](#the-docker-backend-hotbedvmd).

---

## Tests

```bash
tests/smoke.sh      # container sandboxes — stacks, mounts, fresh runs, rm/prune
tests/vmsmoke.sh    # VMs (libvirt backend) — boots a real throwaway VM
tests/vmdsmoke.sh   # VMs (docker backend) — boots a VM inside a container
tests/hotbedddsmoke.sh # Hotbed suite — the comprehensive gate (see below)
```

Every suite prints its own tally (`== result: N passed, 0 failed ==`); the
gate is **0 failed**. The daemon suite runs hermetic scratch daemons on
scratch ports but needs ≥ 1 running sandbox visible to the inventory for its
P1 section — the invocation dance is documented in `docs/RELEASING.md` §4.

`smoke.sh`: doctor sane, all four stack autodetections (`package.json` → node,
`pyproject.toml` → python, `go.mod` → go, `Cargo.toml` → rust, empty → base),
one smoke-sandbox per stack with its toolchain running (node+npm, python+uv,
go, rust), fresh runs leave nothing behind, container-written files are
host-user owned, exec/status/reattach/exit-code propagation, `hbd opencode`
works, `rm`/`ls` bookkeeping, `prune`, `caches ls`.

`vmsmoke.sh`: creates a real throwaway VM (`hotbedvmsmoke-<epoch>`) with a
mktemp share and exercises the whole loop — master + libvirt present,
create+ssh, user/sudo, 9p host→guest and guest→host, opencode in VM, docker
service, snapshot save → rogue-writes → revert → restart → file resurrected,
then destroy + vm dir cleaned + domain undefined (this last check caught a
real bug: undefine silently skipped while snapshots existed).

The reset-loop / boot investigation also produced a one-line live test
embedded in vmsmoke: if a fresh VM can't ssh, it prints the serial log path.

`vmdsmoke.sh`: the same loop against the docker backend (the suite presets
`HOTBEDVM_BACKEND=docker`) — creates `hotbedvmdsmoke-<epoch>`, a VM that is itself
a Docker container, then checks ssh/sudo, 9p host→guest and guest→host,
opencode, full kernel, **a real `docker run --rm hello-world` inside the VM**
(the check that dies first if the guest-bridge collision ever returns),
snapshot save → rogue-write → revert → restart → file resurrected, then
destroy + vm dir + container-undefined checks.

`hotbedddsmoke.sh`: spins the daemon on a scratch port (7790, auto-stopped
again) and verifies the Hotbed surface — health probes, unified `ps`,
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
bin/hbd                    container sandbox CLI (the daily driver)
vm/hotbedvm.sh             VM CLI (install.sh puts a `hotbedvm` wrapper on PATH)
quick-install.sh           one-command install/update (curl | bash)
install.sh                 kit installer: PATH wrappers, prereq gate, base image
containers/Dockerfile      5 stack images from one shared base layer
containers/entrypoint.sh   runtime uid/sudo/ownership bootstrap
tests/smoke.sh             container suite (all stacks; no VMs needed)
tests/vmsmoke.sh           VM suite (libvirt; boots a real throwaway VM)
tests/vmdsmoke.sh          VM suite (docker backend; VM = container)
tests/hotbedddsmoke.sh        Hotbed suite — comprehensive gate (P1+P2+P3)
hotbed/                      Hotbed: Go daemon + dashboard + docker-style CLI
SECURITY.md                full security survey: fixed findings + accepted risks
  cmd/hotbed/                entrypoint (main.go)
  internal/engine/         docker/virsh/kit-CLI wrappers, file tail
  internal/sandbox/        unified entity + cross-runtime collector
  internal/attach/         tmux sessions, ConnInfo, wt.exe launcher, ssh -R
                           proxy tunnels, sync write transports, probes
  internal/api/            REST + SSE server, embedded dashboard (web.go)
  internal/cli/            serve + ps/stats/logs/attach/connect/start/stop +
                           doctor + config/usage/key/budget/sync (p2.go)
  internal/config/         global settings store + masked merge + host import
  internal/dnsfence/       allowlist DNS fence + per-sandbox lookup tracking
  internal/egressctl/      preset apply + config record, DNS-tab actions, re-applier
  internal/keys/           per-sandbox proxy keys + budget wallets
  internal/ledger/         usage journal, token/cost summaries, pricing
  internal/proxy/          LLM proxy: auth, budgets, usage-aware SSE pass
  internal/cfgsync/        per-sandbox opencode config generation + stale markers
  internal/env/            shared constants, kit-root resolution, HOTBEDD_* dirs
  bin/hotbedd                 built binary
  desktop/                 thin Electron shell (see hotbed/desktop/README.md)
vm/images/
  debian-13-genericcloud-amd64.qcow2        patched master (shared by all VMs)
  debian-13-genericcloud-amd64.qcow2.hotbedvm-patched   idempotency marker
vm/hotbedvmd/                 docker backend runtime: Dockerfile + PID-1 `entry`
vm/vms/<name>/             per-VM seed files (+ disk & serial on libvirt; the
                           docker backend keeps disk/serial in the
                           `hotbedvmd-store` volume, seeds here)
vm/.work/                  scratch convention for multi-GB disk work
                           (never /tmp — RAM-backed tmpfs)
```

---

## Disk footprint & housekeeping

- **Master image**: ~950 MB on disk (~3 GiB virtual), shared read-only by all VMs.
- **Per VM**: overlay starts ≈ 200 KB and grows only with actual writes
  (sparse up to `--disk` size). Twenty-GB default means typical 2–3 GB usage.
- Reclaim: `hotbedvm destroy <name> --yes` (everything of that VM, incl.
  snapshots), `hbd rm --yes` / `hbd prune --yes` (containers),
  `hbd caches nuke --yes` (cache volumes), `docker rmi hotbed:*`
  (images will rebuild on demand). To fully restore from zero:
  delete `vm/images/` and `vm/vms/`, then `fetch-image` again.
- **Docker backend extra**: VM state lives in the named docker volume
  `hotbedvmd-store` (the ~950 MB master shared by all VMs + one
  `/store/vms/<name>/` subdir per VM). `HOTBEDVM_BACKEND=docker hotbedvm destroy <name> --yes` removes the
  container and that VM's store dir; the volume and the `hotbedvmd:latest`
  runtime image remain until you `docker volume rm hotbedvmd-store` /
  `docker rmi hotbedvmd:latest` (the image is rebuilt automatically on the next
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
  `hbd --fresh --no-auth --no-ssh --net none` or a no-share `hotbedvm create`.
- **P2 reduces one of these surfaces**: after `hotbedd sync NAME`, the sandbox's
  opencode holds only its own `hbk-…` proxy key (scoped to that sandbox,
  revocable, budget-capped). The upstream provider key stays in
  `~/.config/hotbedd/config.json` on the host and is rewritten server-side —
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
  system" — none of it survives `hbd rm` or `hotbedvm destroy`.
  Not designed against: a malicious agent actively attacking the Docker or
  libvirt socket (root-equivalent), kernel-level container escapes, or
  piecing together your mounted credentials.

### Ship / distribution

Source ships; the heavy Debian cloud master (~1 GB unpacked) is fetched at
runtime with `hotbedvm fetch-image` — that is why `vm/images/` is gitignored, and
per-VM seed ISOs are generated, not committed. Concretely:

- **Install on a new host**: prerequisites + scripted walkthrough in
  [Install: step by step](#install-step-by-step) — `./install.sh` gates the
  deps, installs `hbd` + `hotbedd-serve`, and offers the one-time image fetch.
- **Release artifacts**: `make release` builds version-stamped
  `dist/hotbedd-v<ver>-linux-{amd64,arm64}` + `SHA256SUMS` (Go toolchain only).
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

## LLM disclosure

This kit is developed in open partnership with LLM coding agents: most of
the code, tests, docs and release steps in this repository were authored
or reworked by an assistant (OpenCode driving an LLM) under close human
direction. Nothing lands without the maintainer reading it and the gate
passing (`scripts/static-check.sh`, `tests/hotbedd-smoke.sh`, and
live-compatibility checks on the real box - including the rename-release
verification that caught a hermetic-sweeper bug before it could ship).
The dashboard help page carries the same note. If you find where the
machine ran ahead of its operator, an issue or PR is the full disclosure.

## License

[MIT](LICENSE) — use it, break it, ship it. The upstream opencode binary
and the Debian/other base images referenced by this kit keep their own
licenses; nothing here republishes them.
