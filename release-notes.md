# SBX Desktop v0.4.0

The burn-reliability release of **OpenCode-SBX** + **sbxd**: the token
ledger that actually renders, a self-healing proxy relay, in-site
dialogs, a real help page and a dashboard that keeps your place.

## What's new

- **Relay no longer a shared shell** — per-daemon-state relay container
  names (`sbx-relay-<confighash>`, `SBXD_RELAY_NAME` override) end
  cross-daemon adopt-and-destroy (hermetic suites used to demolish the
  host relay). The daemon re-establishes its relay at boot. Legacy
  migration drops the old shared-name relay on upgrade.
- **Burn views render, always** — two dashboard JS "ghosts" (`fmtTok`,
  `burnShowStale` called but never defined) blanked the burn views by
  throwing inside render callbacks whose `.catch` painted "burn data
  unavailable" despite 200 responses. Fixed, and the class is now
  guarded forever: `scripts/static-check.sh` extracts the embedded
  appJS, syntax-checks it and runs a free-call scan for undefined
  helpers (string-masked, comments/regex-literals safe). Wired into
  `make check` and the gate suite.
- **Burn ledger UX** — fixed table ordering (live → wallet-only/synced
  → stale, alphabetical within groups; the 5 s refresh no longer
  reshuffles rows); value-only ticks patch cells in place (no flicker,
  no focus theft); sandbox switches while the burn tab is open now swap
  the data; explicit reload button; overview cards show tok-today
  chips. Stale rows genuinely hide behind an iOS-style toggle.
- **Stale rows can be deleted for real** — `/api/usage/purge` now also
  drops a dead sandbox's leftover wallets + stored sync targets (the
  rm-lifecycle mirror). Before, config-backed rows resurrected on
  every `/api/usage` (opush2: a VM synced via ssh tunnel kept its
  target file and "delete records" could never remove the row).
- **In-site dialogs** — `window.confirm` replaced by an in-site modal
  (danger styling, attribute lines, Esc/backdrop cancel) for stale
  purge (2-step), stale-row delete, sandbox delete and the VM
  fully-wipe second confirm.
- **Help & CLI page** — the sidebar "Help" link opens a real reference
  page (mental model, quick start, sbx/sbxvm/sbxd command tables, burn
  counting notes, troubleshooting) instead of a one-line toast.
- **Dashboard remembers your place** — front-most view (global view or
  sandbox detail incl. active tab) persists in localStorage and is
  restored on load; unknown sandbox degrades to the empty card.
- **Plain-http correctness** — dashboard assets serve
  `Cache-Control: no-store` (stale app.js across deploys is gone).
- Suite grows to **133 checks** (static appJS section added).

## Upgrade from v0.3.0

```bash
cd ~/OpenCode-SBX
scripts/update.sh --yes
# hard-reload the dashboard once (Ctrl+Shift+R) if a pinned tab shows
# old assets — though no-store headers make this a formality now.
```

## Carried from v0.2.2–v0.3.0

`SBXD_BIND` remote exposure, snakeoil TLS, recipe provenance stamps +
stale-image warns, honest container log stream, TLS-resilient VM boot
registration, self-healing guest opencode, opencode staging over ssh
(no guest egress), CLI-create self-sync, async VM create (returns at
spawn) + boot-end auto-sync, create-time build-arg fix, serial log
hygiene, update.sh collision/bind hardening — plus the v0.3.0
dashboard refresh: live Overview start page (`GET /api/stats`), create
wizard busy-state across all three vessel kinds, honest burn counting
(est = bytes/4 where providers report no usage).
