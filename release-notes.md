# SBX Desktop v0.2.11

The async-create release of **OpenCode-SBX** + **sbxd**: pressing Create
answers the moment the VM is spawned — not after it finished booting — and
every VM now gets its models wired automatically at first boot-end.

## What's new

- **Create returns at spawn, boot continues in background** — the
  dashboard's create button used to idle on the creation page for the
  whole VM boot (~2 min): the route ran the kit's full create pipeline
  (ssh wait + cloud-init + staging + sync) synchronously. The kit now
  honors `SBXVM_SPAWN_ONLY=1` inside `create` (returns right after
  rebuild/seed/spawn, docker and libvirt arms) and grew a `postboot`
  verb for the second half; the daemon spawns, answers the create call
  immediately with `"booting":true`, and drives post-boot in a
  background goroutine. Live-verified: API answered in ~6 s, boot
  confirmed ~97 s later.
- **VMs auto-sync at boot-end** — a VM reaching boot-end without a sync
  target (dashboard-created VMs never synced before; a CLI-create whose
  self-sync failed) now gets exactly ONE controller-side sync: mint key
  + write the guest's `opencode.json` model list + establish the ssh -R
  tunnel — the same work the dashboard sync button does, automatic.
  Audited (`sandbox.sync — models wired automatically at boot-end …`);
  failure text is recorded honestly (e.g. no providers configured).
  Idempotent by design: the kit's own create-time sync POST lands
  *before* its boot-end, so the target already exists then and the
  attempt is skipped — no double mint.
- Verified end to end on a real docker-backend guest: async create →
  booting state → running; opencode staged, `GET /v1/models` from
  INSIDE the guest answered through the tunnel; with target + tunnel
  removed, a synthetic boot-end rebuilt both automatically.

## Upgrade from v0.2.10

```bash
cd ~/OpenCode-SBX
scripts/update.sh --yes --tls
```

VMs created before this release sync exactly once on their next
boot-end — or right now, manually: `./sbxd/bin/sbxd sync <vm-name>`
(needs `sbxd config init` first if providers were never imported).

## Carried from v0.2.2–v0.2.10

`SBXD_BIND` remote exposure, snakeoil TLS, recipe provenance stamps +
stale-image warns, honest container log stream, TLS-resilient VM boot
registration, self-healing guest opencode, opencode staging over ssh
(no guest egress), CLI-create self-sync, create-time build-arg fix,
serial log hygiene, update.sh collision/bind hardening.
