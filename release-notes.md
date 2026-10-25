# SBX Desktop v0.2.6

The secure-context release of **OpenCode-SBX** + **sbxd**: the dashboard's
copy buttons need `https://` (browsers only grant `navigator.clipboard`
there), so the daemon now speaks snakeoil TLS on demand.

## What's new

- **Snakeoil TLS listener** — `SBXD_TLS=1 sbxd-serve` (or `--tls`) flips
  the *same* dashboard port to TLS. The daemon generates the self-signed
  cert in-process (ECDSA P-256, stdlib only) and pins it at
  `~/.local/state/sbxd/tls/sbxd-cert.pem` — the printed sha256
  fingerprint stays stable across restarts, so the browser warns *once*;
  deleting the file pair regenerates. URL print flips to
  `https://…`; plaintext clients to the TLS port get the honest Go 400
  ("Client sent an HTTP request to an HTTPS server"). Combine with
  `SBXD_BIND=0.0.0.0` on remote boxes — clipboard + non-plaintext on the
  LAN in one flag.
- **Clipboard fallback** — without a secure context the copy buttons now
  degrade to `execCommand('copy')` (works over plain http), and when even
  that fails, the toast names the actual cure instead of a dead button.
- **update.sh preserves TLS** — `scripts/update.sh --yes` reads the old
  daemon's `SBXD_TLS` from its environment and restarts the same shape
  (bind + TLS preserved automatically).
- **Suite +3** — snakeoil checks: https serves health, plaintext is
  refused with the honest 400, and the served fingerprint is identical
  across restarts. Gate: **117 passed / 0 failed**.

## Upgrade from v0.2.5

```bash
cd ~/OpenCode-SBX
scripts/update.sh --yes          # tree + SHA-verified asset + wrapper refresh + restart
# or manually:
#   curl -fLO …/releases/download/v0.2.6/sbxd-v0.2.6-linux-amd64
#   sha256sum -c SHA256SUMS --ignore-missing
#   install -m755 sbxd-v0.2.6-linux-amd64 sbxd/bin/sbxd
#   kill <pid>; SBXD_BIND=0.0.0.0 SBXD_TLS=1 sbxd-serve
# then open https://<box>:7770 — accept the snakeoil warning once,
# fingerprint should match the printed sha256.
```

## Carried from v0.2.2–v0.2.5

`SBXD_BIND` remote exposure, bounded wedged-engine scan gating, bare-array
snapshot contract, create-time image ensure + tiny build contexts,
post-create landing, installer docker-usability + `sbx:base` pre-build,
VM guest opencode self-heal, gh-CLI self-update with autostash.
