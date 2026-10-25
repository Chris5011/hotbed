# SBX Desktop v0.2.5

The create-workflow release of **OpenCode-SBX** + **sbxd**, cut from live
remote-box feedback: the first create on a fresh box dangled on a missing
kit image, and guests could end up without opencode.

## What's new

- **Create-time image ensure** — `sbx:<stack>` is a kit-built image with
  no registry upstream; the dashboard's first create on a fresh box used
  to die with `pull access denied for sbx`. The daemon now inspects and,
  when absent, builds the stack image through the shared
  `containers/Dockerfile` (same flags as `bin/sbx build <stack>`, 20 min
  cap, one-time — layers are cached afterwards), with an
  `image.build` audit row and an honest remedy hint if the kit build
  files aren't reachable.
- **Tiny build contexts** — the kit Dockerfiles build with the repo root
  as context but COPY only `containers/entrypoint.sh` /
  `vm/sbxvmd/entry`; a root `.dockerignore` now keeps `vm/images/`
  (~1 GB!) and everything else out: context transfers measured
  651 B / 2.5 kB with all layers CACHED.
- **Install builds `sbx:base`** — the default dashboard stack image is
  pre-built inside the docker-reachable install branches
  (non-fatal on failure; on-demand ensure covers the rest).
- **Landing page after create** — success no longer parks the user on
  the form: the dashboard jumps straight to the new machine's overview,
  polling while the collector surfaces it (≤ ~5 s), honest fallback to
  the list. The create button meanwhile explains the one-time image
  build that may hold the first request.
- **VM guests self-heal opencode** — the guest's opencode install was a
  one-shot cloud-init `runcmd`; a transient first-boot network miss
  left VMs without the binary forever (caught live on `sbxvm-test`). It
  is now a guarded systemd unit (`sbx-opencode-install.service`,
  network-online-aware, one-shot per BOOT — retries every boot until
  the binary is present).
- **Suite +2** — fake-docker create checks: a missing stack image must
  auto-build through the daemon and the build must precede the
  container create (order asserted on a recorded shim call log). Gate:
  **114 passed / 0 failed**.

## Upgrade from v0.2.4

```bash
curl -fLO https://github.com/Chris5011/OpenCode-SBX/releases/download/v0.2.5/sbxd-v0.2.5-linux-amd64
sha256sum -c SHA256SUMS --ignore-missing
install -m755 sbxd-v0.2.5-linux-amd64 ~/OpenCode-SBX/sbxd/bin/sbxd
# one-off guest fix if a VM already lacks the binary (future VMs self-heal):
~/OpenCode-SBX/bin/sbxvm ssh <name> \
  'curl -fsSL https://github.com/sst/opencode/releases/download/v1.19.4/opencode-linux-x64.tar.gz | tar -xz -C /usr/local/bin opencode && chmod 0755 /usr/local/bin/opencode'
kill $(ss -ltnp 2>/dev/null | grep :7770 | grep -o 'pid=[0-9]*' | cut -d= -f2 | head -1)
SBXD_BIND=0.0.0.0 sbxd-serve
```

## Carried from v0.2.2–v0.2.4

`SBXD_BIND` remote exposure, bounded wedged-engine scan gating, bare-array
snapshot contract (empty-inventory crash), installer docker-usability +
libvirt probes, release-asset adoption, honest install guide.
