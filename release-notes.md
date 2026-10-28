# Hotbed v0.7.0 — agents, DNS insight and per-agent burn

The biggest release since the proxy landed: coding agents beyond OpenCode
as drop-in modules, DNS tracking with allow-afterwards for every sandbox
kind (containers and both VM backends), per-agent virtual keys, a
one-command installer and a reworked create wizard. Everything below was
verified live on the release host (containers, hotbedvm/libvirt and
hotbedvmd VMs), including a real-inference sweep of all six agents
against a self-hosted vLLM gateway.

## What's new

- **Agent modules** — Aider, Claude Code, Codex CLI, Hermes Agent,
  OpenClaw and OpenCode (built in). A module is a folder
  (`agents/<id>/agent.json` + `install.sh`); add your own under
  `~/.config/hotbedd/agents/` without touching the daemon. Toggle them on
  the new *Agent modules* page; enabled ones are checkboxes in the create
  wizard. Every sandbox gets an **Agents** tab (Install / Enable & install
  / Open in tmux / re-sync) and one "*Name* in tmux" button per installed
  agent. Works in containers and both VM backends (installed over ssh
  after boot). CLI: `hotbedd agents ls|enable|disable|add SANDBOX ID`.
- **Proxy wiring for every agent** — manifests declare the provider style
  (`openai`/`anthropic`), env, files and a `setup` command; sync renders
  them with the sandbox's key and the provider's models (`{{model}}`,
  `{{models_json}}`, `{{models_objects}}`, …). Hermes and OpenClaw get the
  full model list.
- **Per-agent virtual keys** — one `hbk-<agent>-<provider>-…` key per
  sandbox × agent × provider; the proxy translates it and books every call
  to the sandbox *and* agent. Burn tab, burn drilldown, `hotbedd usage
  NAME` and `hotbedd key ls` show the per-agent split. Existing keys keep
  working as "shared key" until a re-sync.
- **Conversations survive the sandbox** — every agent keeps its sessions
  in the workspace (`/workspace/.hotbed/agents/<id>`, git-ignored): tear
  the sandbox down, mount the same folder into a new one, *Resume last*
  (Agents tab) continues the conversation. Verified for all six agents.
  Virtual keys follow the workspace, so credential-bound sessions
  (Hermes) resume too.
- **Local models, Claude Code included** — new per-provider
  `compat: ["anthropic-gateway"]` rewrite lets Claude Code run against the
  Anthropic route of an OpenAI-style gateway in front of open models
  (in-conversation system messages moved to the top-level system,
  unsupported effort mapped); every Claude model alias maps to the synced
  model.
- **DNS tracking + allow afterwards** — `allow` and `allowlist` sandboxes
  can record every lookup (domain, count, refused, first/last seen). The
  new **DNS** tab lists them with an allow toggle per domain (live, no
  restart), *Lock down to allowlist* and *Open egress*. Allowlists may
  start empty. CLI: `hotbedd egress NAME … --track | track on|off | seen |
  add|rm DOMAIN`. Recipes: `dns_track`, `egress_allowlist`.
- **Fences for VMs** — DNS tracking, allowlists and all presets now work
  for hotbedvmd (rules on the VM's container, outside the guest) and
  libvirt VMs (rules inside the guest over ssh). The guest DNS cache is
  flushed on every fence change so refused lookups show up.
- **Fences survive restarts** — stop/start, the idle sweeper and reboots
  used to wipe the netns rules silently; the daemon now re-applies every
  recorded preset within ~30 s.
- **Create wizard rework** — kind cards, one section order for every kind
  (Basics → Workspace → Network → Coding agents → OpenCode → Budgets);
  VMs gain size presets, a styled workspace row and the full Network
  section.
- **LLM providers** — the "OpenCode config" page is now *LLM providers*
  (style + compat documented in place).
- **One-command install** — `curl -fsSL
  https://raw.githubusercontent.com/Chris5011/hotbed/main/quick-install.sh
  | bash -s -- --deps --service` (clone, sha-verified binary, install.sh,
  systemd user unit). Re-run to update.

## Fixes

- `install.sh` / `scripts/update.sh` aborted on the current tree (stale
  `hotbedd/` path); `hotbedvm` is now installed as a wrapper on PATH.
- Wizard-created containers without a mount had a root-owned
  `/workspace` — agents could not write files.
- The relay now respawns on every new daemon build (version + binary
  hash), not only on version bumps.
- The relay keeps the DNS fence running even without LLM providers.
- Upstream errors without reported usage book zero tokens instead of a
  bytes/4 estimate (a client retrying 400s drained wallets).
- OpenAI Responses API usage is parsed exactly (Codex).
- Recipes accept `egress: allowlist`.

## Upgrade from v0.6.1

```bash
cd ~/hotbed
scripts/update.sh --yes
```

Then, per sandbox, *re-sync proxy wiring* on its Agents tab (or
`hotbedd sync NAME`) to give every agent its own virtual key.

---

# Hotbed v0.6.1 — the fence polish release

Three user-reported fixes around the v0.6.0 fences and one sure-footed
dashboard touch-up. No behavioral surprises: everything below was
verified live (throwaway sandboxes, then deleted).

## What's new

- **Allowlist keeps the LLM proxy reachable** - the allowlist chain used
  to accept only the DNS fence listener on the bridge gateway, so a
  synced opencode inside an allowlist sandbox could never reach the
  hosted proxy (the connection silently hung). The proxy is
  kit-internal and bearer-key gated, not "internet": the chain now also
  accepts `gw:7775` (`HOTBEDD_PROXY_PORT` override for custom proxy
  configs), in both the preset script and the keeper re-apply script.
  Sandboxes fenced before this fix need one re-apply
  (`hotbedd egress NAME allowlist ...`) to pick the rule up.
- **Create wizard: allowed-domains input explains itself** - the
  allowlist input (shown when the `allowlist` preset is selected) now
  carries a hover tooltip and a helper-line pointer noting that synced
  opencode needs `opencode.ai` on the list for login/model-list/update
  calls, otherwise chat fails early (`EAI_AGAIN`). Preset order reflects
  the fence ladder: `allow, allowlist, proxy-only, none`.
- **Sidebar names readable again** - the name-owns-a-full-row layout:
  the sandbox name wraps only at natural separators (`-`, `_`) with the
  state pill beside it; stack/image subtitle and cpu/mem meters sit on
  a second row. The v0.6.0 `overflow-wrap: anywhere` attempt let the
  flex track collapse names to two characters per line.
- **Gate suite grew to 141 checks** - allowlist keeps the LLM proxy
  reachable (plus the wizard-tooltip and sidebar-layout regressions in
  earlier commits all green).

## Upgrade from v0.6.0

```bash
cd ~/hotbed
scripts/update.sh --yes
```

---

# Hotbed v0.6.0 — the egress allowlist release

The outbound-fence story completes: sandboxes can now be pinned to an
**explicit domain whitelist** (host-based egress firewall), and two rough
edges from v0.5.0 are fixed.

## What's new

- **Domain allowlist egress preset** — `hotbedd egress NAME allowlist
  example.com api.provider.net` installs a real content fence in the
  sandbox's netns: everything drops (loopback + established stay), all of
  the sandbox's DNS is routed through the fence listener inside the docker
  engine, and only the listed domains resolve and connect. Every IPv4 the
  fence answers is installed as an ACCEPT moments later (domains are
  pre-resolved at install so the fence is complete at minute zero); the
  daemon keeps it fresh as CDNs rotate addresses. Non-listed domains get a
  synthesized `REFUSED` — dashboards and agents fail fast instead of
  hanging. Domains live in `config.json` under `egress_allowlist` and also
  work from `hbd.yaml` recipes (`egress: allowlist`) and the dashboard
  create wizard. The fence is fail-closed: an unknown source or a missing
  attribution answer answers nothing.
- **The four presets at a glance**
  | preset | open to the sandbox |
  |---|---|
  | `allow` | everything (docker default) |
  | `proxy-only` | loopback, established, private ranges, DNS — public **drops**; LLM traffic rides the hosted proxy |
  | `none` | loopback + established only; even DNS drops |
  | `allowlist` | like `none`, plus the listed domains (DNS goes through the fence) |
- **Burn tab rows stop jumping** — model rows ordered by token usage
  (desc), cost as tiebreak, model name as the final deterministic key; the
  zero-cost shuffling from v0.5.0's cost-only sort is gone.
- **Proxy streams oversized bodies** — the 8 MiB response-buffer
  truncation is gone: bodies stream through untruncated (a 9 MB model
  payload arrives intact; regression-checked in the suite). Provider
  context windows (GLM's 1,048,576-token one confirmed live) surface
  correctly in the burn row.
- **Terminal spawn works from the resident daemon** — under a systemd
  user service (minimal PATH) `SpawnTerminal` used to fall through to
  wt.exe/cmd.exe failure; it now probes the WindowsApps glob and System32
  paths explicitly. Also: `HOTBED_ROOT` self-location dereferences
  symlinks, and hermetic test daemons can never adopt legacy sandboxes.
- **Relay upgrades transparently** — the engine-side relay is version
  stamped; a daemon upgrade respawns it once so engine-VM features (the
  DNS fence) land without manual container surgery.
- **Docs** — LLM development disclosure on README, both CLI helps and the
  dashboard; naming-notes corrections (hotbed.dev is registered — no
  domain is claimed).

## Try the fence

```bash
hotbedd egress NAME allowlist api.openai.com   # pin a sandbox to one domain
hotbedd egress NAME                            # show rules + configured list
hotbedd egress NAME allow                      # reset to the docker default
```

Needs docker; same convenience-fence disclaimer as before (it is
re-apply-after-recreate, not a security boundary). The gate suite grew to
**140 checks** (allowlist chain, nat side, fail-closed DROP, reset cleanup).

## Upgrade from v0.5.0

```bash
cd ~/hotbed
scripts/update.sh --yes
```

The daemon restart respawns its relay (version-stamped) — sandboxes keep
running through it.

---

# Hotbed v0.5.0 — the rename release

The project (formerly **OpenCode-SBX**, daemon **sbxd**, CLI **sbx**) is now
**Hotbed**. Same contract, new name, zero live disruption — all running
sandboxes from pre-rename daemons keep working, untouched, on this build.

## Why rename

`sbx` is taken: Docker Inc. ships proprietary "Docker Sandboxes" with the CLI
`sbx` (repo `docker/sbx-releases`) for the exact same use case — AI coding
agents in isolated environments. Once public, every search for "sbx" would
land there, not here. "Hotbed" is fresh everywhere that matters: npm, PyPI,
crates.io, Homebrew and Docker Hub all had the name free. (Correction,
2026-10-27: a WHOIS check found `hotbed.dev` already registered — the
DNS-record probe at naming time was inconclusive. The repo links to no
domain.)

## The new names

| before | after |
|---|---|
| OpenCode-SBX (repo/project) | **Hotbed** |
| `sbx` CLI | **`hbd`** |
| `sbxd` daemon | **`hotbedd`** |
| `sbx:<stack>` images | `hotbed:<stack>` |
| `dev.sbx.*` labels | `dev.hotbed.*` |
| `bin/sbx`, `sbxd-serve`, `sbxvm`, `sbxvmd` | `bin/hbd`, `hotbedd-serve`, `hotbedvm`, `hotbedvmd` |
| `sbxk-…` keys | `hbk-…` (`sbxk-…` tokens remain valid) |
| `~/.config/sbxd`, `~/.local/{share,state}/sbxd`, `~/.local/state/sbx` | `~/.config/hotbedd`, `~/.local/{share,state}/hotbedd` (copied once, legacy dirs untouched — a downgrade still boots) |
| container user `sbx` (`/home/sbx`) | `hbd` (`/home/hbd`) in v0.5 images |

## Live compatibility (the point of v0.5.0)

- **Legacy sandboxes stay visible and manageable** — collectors list BOTH
  label generations (`dev.hotbed` + `dev.sbx`), deduped by container name;
  the kit queries both too.
- **Owner stamps survive** — the owner label is now seeded from a stable
  `owner-id` file (first run seeds from the legacy config-dir hash), so the
  rename never re-stamps existing sandboxes as foreign and idle sweepers
  keep respecting them.
- **The relay keeps its name** — relay names derive from the owner stamp,
  which is preserved byte-for-byte across the rename.
- **Tmux sessions re-parent** — pre-rename `sbx-*` sessions are renamed to
  `hotbed-*` on daemon boot; attached clients stay attached.
- **In-container home probing** — runtime code probes `/home/sbx` vs
  `/home/hbd` per container, so synced proxy configs and attach-time XDG
  overrides work on both image generations.
- **Old keys keep working** — keys match by stored token string; `sbxk-…`
  tokens minted by v0.4 remain valid, new mints use `hbk-…`.

Deliberately unchanged: the relay container's `sbx-relay-<owner>` prefix
(its in-flight identity derives from the preserved owner stamp), and the
in-container config path `opencode-sbx` (legacy containers depend on it).

# SBX Desktop v0.4.1

A setup-ergonomics patch: the two things a fresh box touches first (the
sandbox image and the installer) get the gaps closed.

## What's new

- **One-line install with dependencies** — `./install.sh --with-deps`
  apt-installs every actually-missing host prerequisite (docker.io,
  tmux, ripgrep, jq, ssh, build tools, python3/pip), enables the docker
  engine, adds your user to its socket group and pulls the Go toolchain
  from the official tarball when no daemon binary exists. The libvirt
  VM stack stays opt-in; non-Debian systems get an honest tool list
  instead of pretending. A real `--help` replaces the flag error that
  used to advertise one. Fresh box, whole kit:

  ```bash
  git clone https://github.com/Chris5011/OpenCode-SBX.git \
    && cd OpenCode-SBX && ./install.sh --with-deps --yes
  ```

- **tmux ships in every sandbox image** — the common image layer now
  includes tmux (a few MB). The attach desk never needed container-side
  tmux (that session lives on the host), but agents working inside a
  sandbox legitimately want a terminal multiplexer. All five stack
  images (`base`, `node`, `python`, `go`, `rust`) rebuild-verified:
  go 1.27.3, cargo/rustc 1.99.1, node v22.23.4, uv 0.13.5 now carry the
  full common toolset including tmux.

- **Security sweep fixes carried from the v0.4.0 dashboard work**
  (same-day): sandbox-name flag-injection guard (`--privileged` etc.
  rejected at create), upstream proxy header timeout (wedged upstream
  no longer parks request goroutines), ReadHeader/Idle timeouts on all
  listeners, single `env.SafeName` name sanitizer, observable dashboard
  error paths. Full history secrets scan: clean.

## Upgrade from v0.4.0

```bash
cd ~/OpenCode-SBX
scripts/update.sh --yes
```

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
