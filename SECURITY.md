# HBD Security Posture

Status: **public alpha** (positioned for review, not for hostile hosting). This
document is the security survey of the HBD desktop kit — what protects what,
what was found and fixed, and what is deliberately out of scope.

## 1. Scope

What HBD is:

- `hotbedd` — a localhost daemon (dashboard :7770 + LLM proxy :7775) that spawns
  and steers docker containers and libvirt/docker-backend VMs ("sandboxes").
- The hbd proxy holds the **only copies** of upstream provider API keys (in
  `~/.config/hotbedd/config.json`, `0600`). Sandboxes never see a provider key —
  they get a minted per-sandbox bearer token (`hbk-…`) that only works
  against the proxy.

What HBD is **not**: a security boundary around untrusted workloads. Sandbox
containers are uid-mapped but the default egress preset is `allow`; a
container with `docker` socket access or a hostile Sun/* in the mount set can
escape onto the host. Treat HBD sandboxes as "my own agents, fenced from the
network", not "prison".

## 2. Protection model (verified)

| Layer            | Property                                                                 | Status |
| ---------------- | ------------------------------------------------------------------------ | ------ |
| Key custody      | Provider keys only on host, `0600` file / `0700` dir; never in sandboxes  | ✅ verified |
| Sandbox creds    | Per-sandbox minted tokens, 128-bit random (`crypto/rand`), revocable     | ✅ verified |
| Token comparison | `subtle.ConstantTimeCompare` in `ByToken` — no timing leak on 0.0.0.0     | ✅ verified |
| Proxy auth abuse | Per-IP backoff after 10 failed auths/60 s (capped 2 s sleep)              | ✅ verified |
| Dashboard CSRF   | `withOriginGuard`: cross-origin mutating requests → `403`                 | ✅ addressed |
| Path escape      | `/api/fs` resolves symlinks (`EvalSymlinks`) then re-checks home prefix   | ✅ addressed |
| Method hygiene   | All mutating routes POST-only (start/stop/delete/budget/terminal/import)  | ✅ addressed |
| Command exec     | Always `exec.Command(arg-list)`, never `sh -c`; shell strings are only    | ✅ verified |
|                  | *composed for display* or outer-quoted (`%q`) for WSL `bash -lc`.         |        |
| Name charset     | Dashboard: `[a-z0-9-]{1,40}`; kit VMs: `[a-zA-Z][a-zA-Z0-9._-]*`;          | ✅ verified |
|                  | docker's own charset blocks metachars for foreign-named containers.       |        |
| Route traversal  | `/api/fs` home-prefixed; sync/target files via `safeName()` slug          | ✅ verified |
| Body limits      | `io.LimitReader` on every decode (1 MB create / 256 KB config / 4 KB rest)| ✅ verified |
| File perms       | config (`0600`), keys (`0600`), audit + sync store (`0600`/`0700`)        | ✅ verified |
| Key echo         | Proxy errors never echo tokens; sync route blanks `tg.Key`;               | ✅ verified |
|                  | `configRoute` GET returns `MaskedCopy`; POST-merge skips `<masked>` keys.  |        |
| Privilege        | No `sudo`, no host iptables mutations; egress rules exec *inside* the     | ✅ verified |
|                  | sandbox netns (iptables there or a scoped helper container).              |        |
| Ownership stamp  | Daemon-spawned objects carry `dev.hotbed.owner` = f(config dir); sweepers    | ✅ verified |
|                  | refuse to touch foreign-owner containers.                                 |        |

## 3. Fixed in this sweep (alpha hardening)

| # | Finding                                                             | Fix |
| - | ------------------------------------------------------------------- | --- |
| 1 | Sandbox bearer tokens minted with only 64 bits of randomness.       | All 16 minted bytes now enter the hex token (128 bits). Old short tokens keep working (string match); re-mint to upgrade. |
| 2 | Token lookup was string-equality — timing side channel on a 0.0.0.0 listener. | `crypto/subtle.ConstantTimeCompare` in `keys.Store.ByToken`. |
| 3 | Proxy accepted unlimited auth guesses per source IP.                | `authBackoff`: ≥10 failures/60 s per IP pays escalating sleep (≤2 s); cleared on success. |
| 4 | Dashboard lacked CSRF protection: any web page could POST to localhost. | `withOriginGuard` wraps the whole mux: mutating requests with an Origin that mismatches the Host are rejected with `403`. GET/HEAD pass; native clients (no Origin header) unaffected. |
| 5 | `/api/fs` prefix-checked the *textual* path only — a symlink inside home could resolve outside it (mount-picker escape). | `filepath.EvalSymlinks` on requested path + resolved home, prefix re-checked on resolved values. |
| 6 | start/stop/delete/budget/terminal/import routes ran for any HTTP method. | Explicit `405` on non-POST (delete already had it). |
| 7 | Empty budget maps could serialize as `null` in `config.json` (cosmetic-driven UI confusion). | `sane()` normalizes nil maps to `{}`. |
| 8 | No way to delete a sandbox (create-only lifecycle).                 | `hotbedd rm` + `POST /api/actions/delete` + dashboard Delete button; VM deletes require explicit confirm (`--yes` / `{"confirm":true}`); keys, tmux session, wallets and sync target are cleaned with the sandbox; every delete is audit-logged (`sandbox.rm`). *(also a functional fix, listed here for completeness)* |

## 4. Noted / accepted (alpha posture)

| Finding | Rationale | Mitigation |
| ------- | --------- | ---------- |
| Proxy binds `0.0.0.0` by design | WSL2 NAT and multi-machine LANs need it; single-user trust model | 128-bit tokens + revocation + auth backoff; set `proxy.bind: "127.0.0.1"` in config to hard-bind loopback-only |
| No TLS on either listener | Localhost/desktop scope; certs would be self-signed anyway | Both listeners are localhost-only by default; rely on loopback/network trust boundary |
| Egress fences `proxy-only` / `none` / `allowlist` are convenience, not a boundary | They are rules inside the sandbox; on VMs root in the guest can flush them | `strict` (v0.8.0) enforces the allowlist outside the sandbox (isolated network + egress proxy); see §4a |
| Default egress `allow` reaches private networks | Sandbox traffic leaves through the host, so it reaches whatever the host can: LAN, VPN tunnels, the host's own services (§4a) | Pick `strict` or `allowlist` for agent workloads; `proxy-only` keeps private ranges open by design and does **not** help here |
| Root inside every sandbox | Agents install packages and hotbed manages VMs through the sandbox user's sudo; the container/VM is the boundary, not the user account | VM `dev` / container user have passwordless sudo, VM `dev` is in the `docker` group — treat everything inside as root-controlled (§4a) |
| Budget enforcement may overshoot on concurrent bursts | Check-then-forward race inside the proxy; tiny window per request | Small overshoot bounded by in-flight requests per wallet; acceptable for a personal burn limiter |
| Upstream key custody rests with config.json I/O | One file holds all provider secrets | `0600`/`0700` perms, masked API GET/merge round-trip; disk encryption is the real control |
| Dashboard GET endpoints are side-effect free but unauthenticated | Loopback listener; origin guard covers mutations | Keep `dashboard.bind` on loopback (default) if the LAN is untrusted |
| Foreign-named docker containers inherit hotbedd CLI steering | `hotbedd` names are requested, not enforced against `docker run` by hand | Arg-list exec + charset validation make this inert; deliberate choice |
| `~/.ssh/hotbedvm_key` used for VM ssh | Kit-generated dev key | Read-only by code in the daemon path; `accept-new` host-key policy scoped to 127.0.0.1 |

## 4a. What a sandbox can reach, and who is root in it

Verified 2026-11-04 on the dev box (Windows 11 + WSL2 NAT, Docker Desktop,
libvirt VM on the default NAT network, egress preset `allow`, a WireGuard
tunnel active on Windows). An agent inside the VM ran its own escape sweep;
the host side was then probed from the VM:

| From a sandbox (egress `allow`) | Reachable | Why |
| ------------------------------- | --------- | --- |
| Public internet | yes | default NAT |
| hotbed LLM proxy (`:7775` on the bridge gateway) | yes | intended; every request needs a minted key |
| hotbed dashboard (`:7770`) | **no** | loopback-only |
| Services the host binds on all interfaces (e.g. a container publishing `0.0.0.0:5000`, the DNS fence `:15353`) | yes | the sandbox's gateway is the host |
| Windows itself (WSL gateway: SMB `445`, RPC `135`) | yes | WSL NAT routes through Windows |
| The home LAN (router, NAS, …) | yes | routed via Windows' default route |
| VPN networks the Windows host has up (WireGuard) | yes | routed via Windows like any other destination |

In short: **a sandbox with egress `allow` can reach everything the host can.**
For agent workloads use `strict` (containers without published ports,
libvirt VMs: an isolated network whose only way out is the allowlist proxy,
enforced outside the sandbox) or at least `allowlist` (in-sandbox fence;
root in a VM guest could lift it). `proxy-only` keeps RFC1918 ranges open by
design — it stops public egress, not lateral movement. Container fences hold
against root in the container (no `NET_ADMIN`); VM guest fences do not.

**Root inside.** The VM user `dev` and the container sandbox user have
passwordless `sudo`; `dev` is also in the guest's `docker` group (the docker
daemon is the guest's own — "docker escape" yields guest root, not the
host). This is by design: agents install packages, and hotbed manages VMs
through `dev`'s sudo. Consequently everything inside a sandbox — files,
the agent's tools, any in-sandbox fence — must be considered under the
control of whatever runs there. The containment is the VM (kernel/KVM
isolation; keep the host kernel and QEMU patched) or the container (shared
host kernel: weaker than a VM), plus host-side controls: `strict` egress,
loopback-only dashboard, key-gated proxy. `/dev/kvm` and vsock devices in
VMs come from nested virtualization being enabled.

## 5. Alpha release checklist

- [x] Full test suite green (`tests/hotbedddsmoke.sh`, hermetic port)
- [x] `rm` lifecycle verified live (container + VM guard refusal)
- [x] Token entropy + constant-time compare shipped, old tokens unaffected
- [x] Origin guard + method checks shipped; CLI/curl unaffected (no Origin)
- [x] No provider keys in logs, API echoes, or sandbox env
- [x] File/perm/audit behaviors verified on a real install
- [x] README command reference and security note updated
- [x] SECURITY.md (this file)

## 6. Reporting

This is a desktop kit for single-user use. If you find a vulnerability,
please open a GitHub issue describing it (or contact the maintainer);
include reproduction steps and the audit trail lines from
`~/.local/state/hotbedd/audit.jsonl` if relevant. Please do not run destructive
proofs-of-concept against a live system you don't own.
