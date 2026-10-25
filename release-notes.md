# SBX Desktop v0.2.8

The hygiene + usability-fix release of **OpenCode-SBX** + **sbxd**: a fresh
box's python/node create works again, a serial firehose can no longer crush
the browser tab, and gate rigidity grew.

## What's new

- **Fix: create-time stack builds (v0.2.7 regression)** — the provenance
  label was passed *before* the `build` verb (`docker --label X build …`),
  which made docker print its usage banner and fail every create-time
  stack build on a fresh box (`sbx:python` hit exactly this). Now
  `docker build --label … --target …`; the suite's fake-docker shim
  asserts the invocation is docker-valid. Existing-box fix for the
  warning path: `bin/sbx build <stack> --no-cache` re-stamps as before.
- **Serial log hygiene** — the sbxvmd serial feed arrives as an
  ANSI-crime firehose with multi-kB lines: it rendered *malformed* and a
  single wide line was enough to crush the browser tab (DOM/reflow OOM).
  Every stream source (docker serial tail, libvirt capture, guest journal
  ssh, container logs) now passes through line hygiene before SSE
  framing: CSI/OSC/C1 escapes stripped, CRs folded, control bytes
  removed, blank spam collapsed, lines capped at 4000 chars
  (`…(+n kB line truncated)`); a newline-free flood flushes capped at
  512 KiB. The browser caps rendered node width too (8000 chars).
  Result on the repro case: a 300 kB junk line shows as ~1 capped line
  instead of the raw megabyte.
- **Suite: 123 checks** — new: the fake-docker shim asserts the ensure
  call is docker-valid (`build --label … --target`); a fake `sbxvmd-`
  container feeding 300 kB of urandom + ANSI junk must stream sanitized
  (no ANSI/CR bytes, wide line capped, marker intact — and the check now
  waits for the collector's 5 s inventory refresh instead of racing it).
- **Suite: stale-port guard** — the gate now hard-fails when the smoke
  port is already bound: a stale suite daemon silently answering health
  checks poisoned a whole gate run (found by a real incident here).
- **Guest opencode** — unchanged from v0.2.7 (per-create seed + retrying
  unit, `systemd-analyze` verified). If a *newly created* sbxvm still
  lacks opencode, run the three-line diagnostic from the release
  discussion; the two usual answers are a guest seeded before the update
  (recreate the VM from the updated tree) or guest egress/DNS.

## Upgrade from v0.2.7

```bash
cd ~/OpenCode-SBX
scripts/update.sh --yes --tls
```

## Carried from v0.2.2–v0.2.7

`SBXD_BIND` remote exposure, snakeoil TLS, recipe provenance stamps +
stale-image warns, honest container log stream (source banner + audit
trail prefill), TLS-resilient VM boot registration, self-healing guest
opencode, update.sh collision/bind hardening.
