# SBX Desktop v0.2.4

The first-remote-week fixes-of-record for **OpenCode-SBX** + **sbxd**: the
two live defects reported from the remote headless box — a dashboard that
crashed exactly when you hit *create* on an empty inventory, and an
installer that let "docker installed" stand in for "docker usable".

## What's new

- **Create no longer crashes the dashboard on an empty inventory** — the
  browser's snapshot listener assigns the SSE payload straight into
  `state.list` and calls `forEach` on it. Older builds shipped two wrong
  shapes on a fresh box: the initial `/api/events` event was a wrapped
  `{"sandboxes":…}` object and every 5 s broadcast tick marshalled the
  empty Go slice to literal `null` — so clicking *create* died with
  `TypeError: Cannot read properties of null (reading 'forEach')`
  (`renderList ← showGlobal` — the stack you saw). The contract is now
  unambiguous and enforced at every layer: `Scan()` returns a non-nil
  slice (`[]`, not null), `BroadcastSnapshot` nil-guards, the SSE
  initial write is a bare array, and the browser normalizes
  array / legacy-wrapped / null before rendering, as defense in depth.
  Wire proof on an empty inventory: `data: []` and
  `{"sandboxes":[],…}`.
- **install.sh: docker is now probed for USABILITY, not presence** —
  following-the-docs boxes previously passed the dependency gate while
  the dashboard honestly reported "down". The installer runs a bounded
  `docker info` and prints the exact remedy per failure shape:
  permission denied on the socket (→ `sudo usermod -aG docker "$USER"`
  + re-login), engine down (→ `sudo systemctl enable --now docker`),
  or an unclassified failure (→ `systemctl status docker`). Because
  root passes the socket probe trivially, running the installer under
  `sudo` additionally checks the *real* daemon user's group membership.
- **Suite: two wire-contract checks** — the first `/api/events` snapshot
  must be a bare array and an empty-engine `/api/sandboxes` payload must
  be `"sandboxes":[]`. Gate grew to **112 passed / 0 failed**.

## Upgrade from v0.2.3

```bash
curl -fLO https://github.com/Chris5011/OpenCode-SBX/releases/download/v0.2.4/sbxd-v0.2.4-linux-amd64
sha256sum -c SHA256SUMS --ignore-missing
install -m755 sbxd-v0.2.4-linux-amd64 ~/OpenCode-SBX/sbxd/bin/sbxd
# re-run the installer once to get the new docker usability probe:
cd ~/OpenCode-SBX && ./install.sh --yes
# restart the daemon by exact PID (keep your bind choice):
kill $(ss -ltnp 2>/dev/null | grep :7770 | grep -o 'pid=[0-9]*' | cut -d= -f2 | head -1)
SBXD_BIND=0.0.0.0 sbxd-serve
```

## Carried from v0.2.2 / v0.2.3

`SBXD_BIND`/`--bind` remote exposure with a loud warning, bounded wedged-engine
scan gating (4 s probe + wall-honest caps), installer release-asset adoption,
honest libvirt gating, step-by-step install guide.
