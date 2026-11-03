# Session hand-off — 2026-11-03

State of `main`: **1 commit ahead of `origin/main`, not pushed** (as asked).
`v0.8.0` is released and pushed; the VS Code work (`7c12e58`) is local only.

## Done today

| Commit | What | Status |
|---|---|---|
| `f262362` | fix(vm): agent persistence on VMs without `--share` (`/workspace` was root-owned → `Permission denied` on every agent sync) | released in v0.8.0 |
| `2e69a7c` | feat(egress): **`strict` preset** (#4) | released in v0.8.0 |
| `2975daf` | chore(release): v0.8.0 — gate 143/0, assets verified by download | pushed, GitHub release live |
| `7c12e58` | feat(ide): **open sandboxes in VS Code** (#2) | **local only, gate 143/0** |

Issue comments: #4 got a comment describing the release (left **open** for you
to confirm). #3 and #5 are still open waiting for your confirmation (v0.7.3).
Nothing posted on #2 yet (not released).

### #4 strict egress (v0.8.0)

- Containers → internal docker network `hotbed-strict-<id>`; only way out is
  the gateway container `sbx-egress-<id>` (allowlist HTTP(S) proxy `:3128` +
  LLM proxy forwarder; `hotbedd egress-gateway`).
- libvirt VMs → NIC re-pointed live (same MAC) to an isolated libvirt network;
  daemon serves the proxy on its bridge IP. The guest's old ssh `-R` tunnel
  is ended before the switch (it otherwise kept `127.0.0.1:7775` busy).
- Agents/OpenCode re-synced automatically once the switch has settled
  (`strict.resync` audit). DNS tab: *Isolate (strict)* / *Leave strict*;
  wizard option; create-time refusals (published ports, docker-backend VMs).
- Live-verified on a container and a libvirt VM, including root bypass
  attempts, container + daemon restarts and leaving strict.
- Documented limits: strict sandboxes can reach each other; attribution by
  source address (root in one strict sandbox could borrow another's address).
- **Not tested:** non-Docker-Desktop engines (gateway → daemon via the WSL
  IP), non-default `proxy.port` together with libvirt strict (the tunnel
  hand-over uses the default/`HOTBEDD_PROXY_PORT` port).

### #2 VS Code (local commit `7c12e58`)

Your answers: own key + one Include line; show (never install) a missing
extension.

- `hotbedd ide NAME [--print]`, `POST /api/sandboxes/<kind>/<name>/ide`,
  **VS Code** button in the sandbox header.
- Containers: Dev Containers attach URI. Your VS Code lacks
  `ms-vscode-remote.remote-containers` → hotbed shows
  `code --install-extension ms-vscode-remote.remote-containers`.
- VMs: Remote-SSH alias `hotbed-<vm>` in `%USERPROFILE%\.ssh\hotbed.conf`,
  key `%USERPROFILE%\.ssh\hotbed_ide`, pinned `hotbed_known_hosts`,
  ProxyCommand `wsl.exe -d kali-linux -e …/hotbedd ide-pipe <vm>`.
- **Changed on your Windows side:** `~/.ssh/config` got the line
  `Include hotbed.conf` at the top (original saved as
  `~/.ssh/config.hotbed-bak`, your host entries unchanged — checked
  `Ansible-Master` still resolves). `hotbed.conf` / `hotbed_known_hosts` are
  currently empty of hosts (the test VM was deleted, its entries removed).
- **Verified:** Windows `ssh.exe hotbed-idevm` → logged in as `dev` through
  the pipe with the pinned host key; extension detection; delete cleanup.
- **Not verified:** an actual VS Code window opening (I did not launch the
  editor on your desktop), the Dev Containers attach (extension missing),
  native-Linux VS Code, the first-connect "platform" prompt of Remote-SSH.

## Not started

- **#1 Image builder** (all three parts in one release, as you chose):
  declarative custom stacks → `hotbed-custom:<name>`; "save sandbox as
  image" with secret scrubbing (no `hbk-` keys / agent env files — verify on
  the image); VM base images (cloud-init clean, machine-id, ssh host keys,
  standalone qcow2) for both backends; Images page. Plan unchanged from the
  build plan.

## Next steps

1. Try **VS Code** on a VM (`hotbedd ide <vm>`) and on a container after
   installing the Dev Containers extension; then push `7c12e58` and cut
   v0.8.1/v0.9.0 (`scratchpad/release-080.sh` pattern).
2. Confirm #3/#4/#5 on your side and close them.
3. #1 image builder.
