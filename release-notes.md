# SBX Desktop v0.3.0

The dashboard-refresh release of **OpenCode-SBX** + **sbxd**: an Overview
start page with live host & sandbox metrics, and a create button that
cannot be triggered twice.

## What's new

- **Overview start page** — the dashboard now opens onto a live
  overview (also in the sidebar): CPU percent + load + core count,
  memory incl. swap, disk per relevant mount (root, home, docker data
  root, kit tree — de-duplicated per filesystem), kernel, uptime,
  engine badges (docker / libvirt / tmux) and grouped inventory counts
  (containers / VMs · libvirt / VMs · docker — running and known).
  Refreshes every 5 s. Served by a new `GET /api/stats`: plain `/proc`
  reads + `statfs`, no exec; CPU% comes from a lazily-started 2 s
  sampler (the first call double-samples 250 ms apart so it answers
  honestly immediately); counts come from the collector's cached
  snapshot — the route never re-scans.
- **Create button busy-state** — pressing Create in the wizard now
  disables the button with a spinner ring until the create call
  resolves or fails, on all of its three kind paths (container, VM ·
  libvirt, VM · docker): a hurried double-click can no longer fire a
  second `POST /api/create`. The stale wizard copy ("blocks until the
  guest is up") was updated to the v0.2.11 reality: creation returns
  the moment the machine is spawned, the guest finishes booting in the
  background, models wire up automatically at boot-end.
- Suite grows to **127 checks** (+4 for the stats contract: sample
  fields, disk statfs, count groups, version field).

## Upgrade from v0.2.11

```bash
cd ~/OpenCode-SBX
scripts/update.sh --yes --tls
# hard-refresh the browser tab once (Ctrl+Shift+R) — the dashboard is
# served with no-cache headers, but a pinned tab may keep its old HTML.
```

## Carried from v0.2.2–v0.2.11

`SBXD_BIND` remote exposure, snakeoil TLS, recipe provenance stamps +
stale-image warns, honest container log stream, TLS-resilient VM boot
registration, self-healing guest opencode, opencode staging over ssh
(no guest egress), CLI-create self-sync, async VM create (returns at
spawn) + boot-end auto-sync, create-time build-arg fix, serial log
hygiene, update.sh collision/bind hardening.
