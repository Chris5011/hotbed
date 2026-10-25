# SBX Desktop v0.2.3

The cold-host hardening release of the **OpenCode-SBX** kit + **sbxd**
daemon. Built from the *real* first remote deployment: the dashboard went
unresponsive during sandbox scans and reported a working docker as "down".

## What's new

- **Scan-path is no longer hostage to a down/wedged docker engine** — a
  cold or remote box used to drag every refresh tick for the full
  ps/stats/ports fanout (the stats call alone capped at 45 s), and any API
  client arriving before the first successful scan blocked on the whole
  pile: the dashboard looked frozen for ~a minute. `Scan()` now answers a
  bounded 4 s probe first and, while the engine is down or wedged, skips
  the docker fanout entirely — an empty docker section comes back
  promptly (with libvirt VMs still rendered) until the engine works again.
- **Wall-honest engine timeouts** — `docker info` (health probe) drops
  15 s → 4 s, `docker stats` 45 s → 8 s, and `RunTO` now uses
  `exec.WaitDelay`, so mere grandchildren holding inherited output pipes
  (shims, wrappers) can no longer keep a nominal cap running past its
  deadline (probed with a fake wedged docker: wall 7.0 s → 4.5 s).
- **Honest "down" badge, promptly** — `/api/health` reports
  `"docker":false` the moment the engine refuses (permission-denied
  socket, stopped daemon, wedged engine), instead of hanging on
  CLI-retry timeouts. Note: docker reported "down" on a box where it was
  *installed* almost always means the CLI can't reach the socket — see
  the fix in the release notes of your install guide / README step 5
  (`sudo usermod -aG docker $USER` + re-login, or
  `sudo systemctl enable --now docker`).
- **Suite: wedged-engine regression checks** — the hermetic smoke suite
  now splices a fake `docker` into a scratch daemon (wedged: `info` past
  the cap, everything else hanging 40 s; and perm-denied: instant
  refusal) and asserts health + sandboxes answer inside seconds, badge
  honest. Gate grew to **110 passed / 0 failed**.

## Upgrade from v0.2.2

```bash
# download the new asset, verify, drop into the tree:
curl -fLO https://github.com/Chris5011/OpenCode-SBX/releases/download/v0.2.3/sbxd-v0.2.3-linux-amd64
sha256sum -c SHA256SUMS --ignore-missing
install -m755 sbxd-v0.2.3-linux-amd64 ~/OpenCode-SBX/sbxd/bin/sbxd
# restart the daemon (keep your bind choice):
kill <old-pid>        # exact PID from `ss -ltnp | grep 7770`
cd ~/OpenCode-SBX && SBXD_BIND=0.0.0.0 sbxd-serve
```

## Carried from v0.2.2

`SBXD_BIND`/`--bind` remote exposure opt-in with a loud warning, installer
release-asset adoption, honest libvirt daemon gating, step-by-step install
guide with real fetch routes.
