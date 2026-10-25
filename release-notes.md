# SBX Desktop v0.2.7

The provenance + honesty release of **OpenCode-SBX** + **sbxd**: stale images
get named at create time, the Logs tab stops streaming eternal blanks, and
TLS daemons no longer miss VM boot signals.

## What's new

- **Recipe provenance (answers "could it be caching?")** — kit images are
  built from `containers/Dockerfile` by two paths (the daemon's create-time
  ensure and `bin/sbx build`); a pre-existing `sbx:<stack>` image from an
  older kit tree kept serving new containers whose provisioning no longer
  matched. Both paths now stamp `dev.sbx.recipe` = first 8 hex of the
  sha256 of that Dockerfile. When a create lands on an image that predates
  stamping or drifted from the current recipe, the response (and the
  recipe CLI) warns by name with the one-cure command:
  `bin/sbx build <stack> --no-cache`. The audit trail records `image.stale`.
- **Honest container Logs tab** — a healthy kit container is silent forever
  by design (the entrypoint idles; the opencode TUI lives in *your* exec
  PTY, invisible to `docker logs`), so the stream read as broken/blank. The
  container stream now opens with a banner naming the real source
  (`sbx attach <name>` / `docker exec -it <name> opencode`), prefills the
  sandbox's audit trail (spawn/attach/every recorded verb), then keeps
  following docker stdout/stderr as before. Attaching and terminal opens
  are now audited too.
- **VMs stop faking "running" over TLS** — the kit's boot registration
  hardcoded `http://`; an `SBXD_TLS=1` daemon answers plaintext with Go's
  400 and silently ATE both begin/end signals, so fresh boots displayed an
  instant fake running. `sbxvm.sh` now probes the scheme once per run
  (https with `-k`, http fallback) — the daemon shows an honest `booting`
  pill until the guest is confirmed up, then re-establishes proxy tunnels.
- **Guests self-heal opencode** — the seeded `sbx-opencode-install.service`
  previously made a single first-boot attempt; one transient network miss
  permanently left VMs without opencode (the reported "sbxvmd has no
  opencode"). New seed: 5 in-run attempts (5 s apart) *and* systemd re-runs
  the failed unit every minute (`StartLimitIntervalSec=0`,
  `Restart=on-failure`) until the binary exists. Existing guests: run
  `sbxvm ssh <vm> -- sudo systemctl start sbx-opencode-install.service`
  once (or just rebake the guest image from this tree).
- **update.sh hardening** — re-running the script absorbs its own untracked
  bootstrap copy at the merge (no more collision abort; receipt + re-exec),
  reads the daemon's *real* bind address from `ss` (a LAN listener was
  misread as loopback), and exposes `--tls`.
- **Suite +2** — container stream must name its source + prefill the trail;
  `notify_controller` (extracted verbatim) must land begin+end on the
  daemon. Gate: **120 passed / 0 failed**.

## Upgrade from v0.2.6

```bash
cd ~/OpenCode-SBX
scripts/update.sh --yes --tls    # tree + SHA-verified asset + restart, TLS preserved
# or manually:
#   curl -fLO …/releases/download/v0.2.7/sbxd-v0.2.7-linux-amd64
#   sha256sum -c SHA256SUMS --ignore-missing
#   install -m755 sbxd-v0.2.7-linux-amd64 sbxd/bin/sbxd
#   kill <pid>; SBXD_BIND=0.0.0.0 SBXD_TLS=1 sbxd-serve
# then open https://<box>:7770 — accept the snakeoil warning once,
# fingerprint should match the printed sha256.
#
# if a create ever advises a stale image:
#   bin/sbx build <stack> --no-cache
#
# opencode inside an existing VM guest:
#   sbxvm ssh <vm> -- sudo systemctl start sbx-opencode-install.service
#   sbxvm ssh <vm> -- opencode --version
```

## Carried from v0.2.2–v0.2.6

`SBXD_BIND` remote exposure, snakeoil TLS (secure clipboard context),
bounded wedged-engine scan gating, bare-array snapshot contract,
create-time image ensure + tiny build contexts, post-create landing,
installer docker-usability + `sbx:base` pre-build, VM guest opencode
self-heal, gh-CLI self-update with autostash, honest rm across GUI+CLI.
