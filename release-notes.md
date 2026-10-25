# SBX Desktop v0.2.9

The guest-provisioning-hardening release of **OpenCode-SBX** + **sbxd**:
fresh sbxvms get opencode from the kit's own ssh channel — guest internet
egress is no longer part of the install contract.

## What's new

- **opencode staging without guest egress** — the seed unit needs guest
  GitHub egress; one box showed the failure mode that makes this unusable
  (ICMP + DNS pass, TCP 443 blackholed — the look of a policy router or
  VPN), and its fresh guests stayed without opencode no matter how hard
  the seed retried. After cloud-init completes, `sbxvm create` now
  checks the guest over the kit's own ssh channel and, when the binary
  is missing, downloads the tarball on the host (hosts already have this
  egress — they built the kit images from the same URL) and pushes it
  into the guest: extract + chmod + version check. The unit keeps
  running in each guest as belt-and-suspenders for egress-capable boxes.
- Verified end to end on a real docker-backend guest: binary removed —
  staged back over ssh within seconds, `opencode --version` → 1.19.4.

## Upgrade from v0.2.8

```bash
cd ~/OpenCode-SBX
scripts/update.sh --yes --tls
# existing egress-blocked guest (create-time staging lands on NEW vms):
#   curl -fSL …/releases/download/v1.19.4/opencode-linux-x64.tar.gz -o /tmp/oc.tgz
#   cat /tmp/oc.tgz | sbxvm ssh <vm> -- 'cat > /tmp/opencode.tgz'
#   sbxvm ssh <vm> -- sudo sh -c 'tar -xz -C /usr/local/bin -f /tmp/opencode.tgz opencode && chmod 0755 /usr/local/bin/opencode'
```

## Carried from v0.2.2–v0.2.8

`SBXD_BIND` remote exposure, snakeoil TLS, recipe provenance stamps +
stale-image warns, honest container log stream, TLS-resilient VM boot
registration, self-healing guest opencode, create-time build-arg fix,
serial log hygiene, update.sh collision/bind hardening.
