# SBX Desktop v0.2.10

The model-wiring release of **OpenCode-SBX** + **sbxd**: a CLI-created VM
now arrives with its opencode models already wired through the sbx proxy.

## What's new

- **CLI-created VMs self-sync** — dashboard-created sandboxes run the
  sync route (mints the proxy key + writes the guest's `opencode.json`
  model list + establishes the ssh -R tunnel), but `sbxvm create`
  skipped it entirely: opencode came up with an **empty model list**.
  After staging, create now POSTs `/api/sandboxes/vm/<name>/sync` to
  the local controller (scheme-aware, best-effort) and prints the
  outcome honestly: *wired*, *no controller*, or *no providers* — with
  the exact cure (`sbxd config init` + `sbxd sync NAME`).
- Verified end to end on a real docker-backend guest: binary staged +
  sync + tunnel, then `GET /v1/models` **from inside the guest** through
  the tunnel returned the provider's real model list (minted `sbxk-…`
  key accepted).

## Upgrade from v0.2.9

```bash
cd ~/OpenCode-SBX
scripts/update.sh --yes --tls
```

For an **existing** VM created before this release (opencode present,
empty model list):

```bash
./sbxd/bin/sbxd config init        # imports providers from the host's opencode config
./sbxd/bin/sbxd sync <vm-name>     # mints key + writes guest config + tunnel
# then: sbxvm ssh <vm-name> -- opencode   → models listed
```

## Carried from v0.2.2–v0.2.9

`SBXD_BIND` remote exposure, snakeoil TLS, recipe provenance stamps +
stale-image warns, honest container log stream, TLS-resilient VM boot
registration, self-healing guest opencode, opencode staging over ssh
(no guest egress), create-time build-arg fix, serial log hygiene,
update.sh collision/bind hardening.
