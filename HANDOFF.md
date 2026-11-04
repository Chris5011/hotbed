# Session hand-off — 2026-11-03 / 2026-11-04

State of `main`: **3 commits ahead of `origin/main`, not pushed**.
`v0.8.0` is released and pushed; VS Code (`7c12e58`), this hand-off and the
image builder (`bd6cebf`) are local only. Gate suite on `bd6cebf`: 146/0.

## 2026-11-04 — #1 image builder (`bd6cebf`, local)

All three parts, as chosen:

- **Declarative images** — `hotbedd image build NAME --from STACK --apt …
  --run … --env …` → `hotbed-custom:NAME`, spec stored for `image rebuild`.
  Used as stack `custom:NAME` (wizard, recipes, API).
- **Save a container** — commit → throwaway network-less copy → scrub →
  verify → flatten (`export | import`). Live test: planted hotbed key,
  `sk-ant-…` file, JSON refresh token and root history were gone from the
  image, harmless files kept, the source sandbox untouched, a new sandbox
  ran from the snapshot. Spec env kept, sandbox `--env` dropped.
- **Save a VM** (shut off) — flattened standalone qcow2, `virt-sysprep`,
  cloud-init state dropped, scrub via guestfish, machine-id emptied.
  Live test: new VMs from the image on **both backends** booted with their
  own machine-id / host keys, cloud-init `done`, planted secrets and the
  local workspace gone, other files kept; the source VM kept its identity.
- Found + fixed on the way: VMs from an image all shared one machine-id
  (guest commands after sysprep regenerated it); the seed's kernel-purge
  `runcmd` reported cloud-init `error` when nothing was left to purge.
- **Not verified:** the Images page / Save-as-image dialog visually — the
  headless Edge screenshot helper stopped working after the reboot (Edge's
  DevTools port refuses connections); the JS passes the static checks and
  the API job flow was tested directly.
- **Limits** (README): the scrub knows paths + token *shapes*; a secret in
  an unknown format stays. Flattened images lose layer sharing (a saved
  image is as big as the filesystem).
- Your containers `hotbed-asdf` and `sbx-moon-sweepers-analysis` were
  **stopped** when I checked (exit 127 at 15:43 UTC, right after the
  reboot, no log output, before any test ran) — I left them alone.

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

## Next steps

1. Try **VS Code** on a VM (`hotbedd ide <vm>`) and on a container after
   installing the Dev Containers extension, and look at the **Images** page.
2. Then push and cut **v0.9.0** (VS Code + images), comment on #1 and #2.
3. Confirm #3/#4/#5 on your side and close them.
