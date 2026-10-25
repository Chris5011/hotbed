# SBX Desktop v0.2.1

First tagged release of the **OpenCode-SBX** kit + **sbxd** daemon: three sandbox
vessels (kit containers `sbx`, libvirt VMs `sbxvm-*`, docker-backend VMs
`sbxvmd-*`) behind one CLI and one embedded, loopback-only dashboard.

## What ships

- `sbxd-v0.2.1-linux-amd64` / `sbxd-v0.2.1-linux-arm64` — daemon + CLI + dashboard
  in one static binary (verify: `SHA256SUMS`; stamp: `sbxd version`)
- source tree (~1 MB): kit (`bin/sbx`, `vm/sbxvm.sh`, Dockerfiles), Go daemon,
  hermetic smoke suite, agent-grade release playbook (`docs/RELEASING.md`),
  `install.sh` one-liner

The heavy Debian cloud master (~1 GB unpacked) is **not** shipped — fetched once
at runtime via `sbxvm fetch-image` / prompted by `install.sh`.

## Install

```bash
git clone https://github.com/Chris5011/OpenCode-SBX && cd OpenCode-SBX
./install.sh               # dep checks + entry points + optional image fetch
sbxd-serve                 # dashboard/API at http://127.0.0.1:7770
```

Requires: bash, docker, tmux, ssh, curl, jq. VM · libvirt add-ons
(`libvirt-daemon-system libvirt-clients virtinst xorriso`) are optional —
the docker VM backend works without them.

## Highlights since the 0.2.0 reference build

- **Create wizard covers all three vessels** — container and both VM kinds with
  sizing (vCPU/MEM/disk/share), backend auto-pick by engine reachability, kit-rule
  validation; VM lifecycle (start/stop/delete) spans GUI + CLI.
- **Boot self-registration** — VMs signal the controller at boot-begin and when
  truly up; the dashboard shows an honest amber **booting** state instead of a
  premature `running`, and proxy tunnels are re-established right after boot-end.
- **Stopped VMs stay in the sidebar** — `virsh list --all` parser fix; shut-off
  domains render with their own pill.
- **Live logs that actually work** — container `docker logs` streaming, VM
  serial capture (serial-file device wired into daemon-created VMs) plus a guest
  `journalctl` fallback over the kit's ssh channel for hosts where libvirt runs
  qemu privileged; correct JSON payload rendering in the logbox.
- **Much more responsive UI** — first-load spinner (with slow-scan and reconnection
  hints), optimistic stop/start with amber `stopping…/starting…` pending pills,
  locked action buttons, and bigger, color-coded toasts.
- **Burn view** — per-sandbox token/cost ledger with stale-record hiding, pins,
  and bulk purge; budgets enforced at the proxy.
- **Honest file/deletion semantics** — double-delete is a 404, deletion re-verifies
  engine-level existence; loopback-only daemon with CSRF origin guard; token
  accounting with budget ceilings; portable tmux attach flow.
