# SBX Desktop v0.2.2

The remote-deployment & install-hardening release of the **OpenCode-SBX** kit
+ **sbxd** daemon. Built from the first live remote deployment of the whole
tree (headless Ubuntu box, release asset + `install.sh`).

## What's new

- **`SBXD_BIND` / `sbxd serve --bind`** — the dashboard/API listens on
  `127.0.0.1` by default as before; remote boxes can now opt into network
  reachability explicitly (`SBXD_BIND=0.0.0.0 sbxd-serve`). Non-loopback
  binds print a loud unauthenticated-warning: the CSRF origin guard protects
  browsers only, your firewall position is the gate for everything else
  (same trust model as the token-gated proxy listener).
- **`install.sh` adopts release assets** — drop a downloaded
  `sbxd-v…-linux-<arch>` binary into `dist/` (or the kit root) and the
  installer wires it up itself (`adopted release binary: … → sbxd/bin/sbxd`);
  no Go toolchain needed. Honest failure when neither binary nor Go exists.
- **Honest libvirt gating** — the installer previously said "libvirt stack
  present" whenever `virsh` et al existed, even though the system daemon
  (`libvirt-daemon-system`) was missing; VM ops failed much later at
  `fetch-image`. It now probes `virsh -c qemu:///system uri` and prints the
  exact remedy (apt packages, `libvirt` group + re-login, classic
  `libvirtd` vs. modular `virtqemud.socket`+`virtnetworkd.socket`).
- **Install guide with real fetch paths** — step-by-step install section in
  the README: prerequisite tables, release-asset routes (`gh release
  download`, token'd curl, direct rsync), PATH behavior spelled out, and the
  `config init`-is-an-import note for fresh headless boxes.

## Upgrade from v0.2.1

```bash
# download the new asset (or rsync from a box that has it), verify, drop into the tree:
curl -fLO https://github.com/Chris5011/OpenCode-SBX/releases/download/v0.2.2/sbxd-v0.2.2-linux-amd64
sha256sum -c SHA256SUMS --ignore-missing
install -m755 sbxd-v0.2.2-linux-amd64 sbxd/bin/sbxd      # in the kit tree
# restart the daemon (loopback unchanged unless you opt in):
sbxd-serve
```

## Highlights carried from v0.2.1

Create wizard across all three vessels, boot self-registration + `booting`
state, shut-off-VM inventory, working live logs (serial + guest journal),
first-load spinner, pending stop/start UX, burn view, honest deletes,
loopback-only dashboard with CSRF guard, token accounting with budgets.
