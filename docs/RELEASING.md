# Releasing SBX Desktop — agent playbook

Audience: **any future agent / maintainer** asked to build, verify or release this
tree. Follow it exactly; every command in here was executed at least once on a
real release (ref. build: v0.2.1 scaffold, 2026-10-25).

Ground rule of this project: **source ships, everything heavy is fetched or
built on the user's machine** (`sbxvm fetch-image`, kit Dockerfiles, per-VM seed
ISOs). The repo must stay ~1 MB of text; `dist/` binaries and `vm/images/` are
never committed (see `.gitignore`).

---

## 0. Ground truth you must respect

| Thing | Value |
|---|---|
| Kit root detection | `$SBX_ROOT` env → **executable dir if it looks like the kit** → `$HOME/OpenCode-SBX`. The installed `sbxd-serve` wrapper pins `SBX_ROOT` to the checkout |
| Daemon CLI | `sbxd/internal/cli` (`Serve`, `ps`, `start/stop/rm`, `attach`, `config get/set/init`, `usage`, …) |
| Daemon dirs (XDG) | config `~/.config/sbxd`, data `~/.local/share/sbxd`, state `~/.local/state/sbxd` (audit.jsonl, usage.jsonl, boot_pending.json, tunnel pid/logs) — all overridable: `SBXD_CONFIG_DIR`, `SBXD_DATA_DIR`, `SBXD_STATE_DIR` |
| Dashboard | embedded in the binary (`internal/api/web.go`): HTML + a raw-string `const appJS` + CSS. **No node toolchain required** — validate extracted JS with `node --check` (snippet in §2) |
| Ports | daemon 7770 (loopback by default; `SBXD_BIND`/`--bind` opt-in to network — README says why), proxy 7775 (0.0.0.0, token-gated), hermetic suite uses 7790/7794/7795/7796/7797/7798/7799 — never "fix" these |
| Suite | `tests/sbxddsmoke.sh` — hermetic but needs **≥1 running sandbox visible to docker** for its P1 inventory gate; the certification dance spawns a disposable one (§5) |
| Version | single source: `sbxd/internal/env/env.go` `var Version` — release stamping overrides it via ldflags; do **not** keep a const here |
| Bash kit | `vm/sbxvm.sh` must stay `bash -n`-clean after every edit; same for `tests/sbxddsmoke.sh` |

**Hard no-list** (learned the hard way): never delete/touch `sbx-test` or any
sandbox without explicit user instruction; never touch foreign libvirt domains
(`orchestrator-*`, `ubuntu-agent-base`); never `pkill` the daemon — kill the
exact PID from `ss` (§5); never commit `vm/images/` or `dist/` hashes.

---

## 1. Version bump (do this first, it defines everything downstream)

1. `Makefile`: `VERSION ?= X.Y.Z` (one line).
2. `sbxd/internal/env/env.go`: `var Version = "X.Y.Z"` (source default = archival;
   the released binary is stamped to `vX.Y.Z` by ldflags regardless — but keep
   both in sync for `go run` users).
3. `README.md`: update the version/changelog mention if there is one.
4. Changelog entry for the release notes (collect from the current git log /
   last session summary; keep them user-facing).

## 2. Static checks (cheap, run before anything live)

```bash
cd /home/chris5011/OpenCode-SBX
make check                  # gofmt-check + go vet ./... + local build
bash -n vm/sbxvm.sh         # kit stays bash-valid
bash -n tests/sbxddsmoke.sh

# dashboard JS: extract the raw-string appJS from web.go, syntax-check it.
# PITFALL: the end marker is "`" + literal newline — do not use "\\n" in a
# quoted heredoc (it becomes a literal backslash-n and fails).
python3 - <<'PY'
src = open('sbxd/internal/api/web.go').read()
marker = "const appJS = `"
i = src.index(marker)
j = src.index("`" + chr(10), i + len(marker))
open('/tmp/opencode/app.js', 'w').write(src[i + len(marker):j])
PY
node --check /tmp/opencode/app.js   # want: no output, exit 0
```

Node is available on the dev host (v20+). If `node --check` fails, fix the JS
**in the Go raw string** (mind: backticks inside must be escaped for Go raw
strings — there are none currently; keep it that way).

## 3. Local binary smoke (optional but recommended)

```bash
cd /home/chris5011/OpenCode-SBX/sbxd && ./bin/sbxd version   # expect: sbxd 0.Y.Z (…)
./bin/sbxd ps
```

## 4. Full-suite certification (the real gate)

The suite is self-contained (own scratch daemons on 7790/7794) but needs one
real running sandbox for its P1 inventory check, so spawn a disposable target
on the live daemon (7770), run the suite, delete the target:

```bash
# if no daemon is running yet: see §5 procedure to restart it first
curl -s --max-time 90 -X POST http://127.0.0.1:7770/api/create \
     -H 'Content-Type: application/json' -d '{"name":"certsuite","stack":"base"}' >/dev/null
sleep 2
cd /home/chris5011/OpenCode-SBX
SBXD_SMOKE_PORT=7790 bash tests/sbxddsmoke.sh 2>&1 | tail -20
# gate: "== result: N passed, 0 failed ==" (the count grows with new checks — must be 0 failed)
/home/chris5011/OpenCode-SBX/sbxd/bin/sbxd rm sbx-certsuite --yes
sleep 5; /home/chris5011/OpenCode-SBX/sbxd/bin/sbxd ps   # certsuite gone; nothing else touched
```

If a check fails: fix the code, NOT the suite expectations, and rerun from the
top. The suite will spawn/stop its own toy containers — that is fine — but it
must never alter user-owned inventory (it can't; it only manages what it names).

## 5. Daemon restart on the dev host (exact — never pkill)

```bash
old=$(ss -ltnp 2>/dev/null | grep 7770 | grep -o 'pid=[0-9]*' | head -1 | cut -d= -f2)
[ -n "$old" ] && kill "$old"
for i in $(seq 1 8); do sleep 1; ss -ltn | grep -q 7770 || break; done
cd /home/chris5011/OpenCode-SBX/sbxd
nohup ./bin/sbxd serve > /tmp/opencode/real-daemon.log 2>&1 &
sleep 3
ss -ltnp 2>/dev/null | grep 7770 | grep -o 'pid=[0-9]*' | cut -d= -f2 > /tmp/opencode/real-daemon.pid
curl -s http://127.0.0.1:7770/api/health    # {"docker":true,"libvirt":true,...}
```

PID lands in `/tmp/opencode/real-daemon.pid`. If the port never frees, check
`ss` again before any repetition; the log file holds the daemon's stderr.

## 6. Release build (`make release`)

```bash
cd /home/chris5011/OpenCode-SBX
make clean
make release VERSION=X.Y.Z        # make release alone uses the Makefile default
```

Expected `dist/` contents (Go 1.26, ~-s -w flags):

```
sbxd-vX.Y.Z-linux-amd64      ~10-12 MB
sbxd-vX.Y.Z-linux-arm64      ~11-13 MB
SHA256SUMS                   two lines
```

Prove the stamp before shipping:

```bash
./dist/sbxd-vX.Y.Z-linux-$(go env GOARCH) version   # must print v…=X.Y.Z
```

(`env.Version` is an ldflags-injected `var`; do not turn it back into a const.)

## 7. Git + GitHub flow (user authenticates via `gh` beforehand)

Day-to-day tree hygiene lives in **`CONTRIBUTING.md`** — conventional commits,
`type/<topic>` branches, squash-merge PRs into a releasable `main`. Release
releases additionally honor it: tags are cut on `main` only, and the release
flow below starts from a branch that is main itself once all PRs are squashed.

```bash
gh auth status                    # must be Logged in before anything git-remote

# one-time repo bootstrap (private, from this tree):
cd /home/chris5011/OpenCode-SBX
git init -b main
git add -A && git status --short  # verify: NO vm/images, NO dist, NO sbxd/bin
git commit -m "SBX Desktop: daemon + kit + dashboard + suite (ref v0.2.1 scaffolding)"

gh repo create chris5011/OpenCode-SBX --private --source . --remote origin
git push -u origin main

# actual release:
git tag -a vX.Y.Z -m "sbxd v0.2.1 — VM GUI create, boot self-registration,
booting state, shutoff-VM inventory, live logs (serial+journald), WM UX polish"
git push origin vX.Y.Z
# NOTE: keep release-notes.md in the REPO ROOT (one file per release, committed)
# — v0.2.1 ships one; update it instead of writing scratch notes in /tmp.
gh release create vX.Y.Z dist/sbxd-vX.Y.Z-linux-amd64 dist/sbxd-vX.Y.Z-linux-arm64 dist/SHA256SUMS \
    --title "SBX Desktop vX.Y.Z" --notes-file release-notes.md
```

Release notes checklist for `release-notes.md` (repo root, committed per release):
build/verify steps for the user (git clone → `./install.sh` → `sbxd-serve`),
requirements (docker; libvirt stack optional), what's new (burn view, VM create
wizard incl. sbxvm/sbxvmd, boot self-registration + booting pill, shutoff VMs
in inventory, live logs first串 capture then guest journal over ssh, spinner,
stop/start pending UX, honest rm across GUI+CLI).

## 8. Post-release verification (on a scratch HOME, optional but easy)

```bash
HOME=$(mktemp -d /tmp/opencode/sbx-install-XXXX) bash install.sh --yes \
  # shows a clean dependency report; branch-off before --fetch-image unless
  # you actually want the ~1 GB download in that scratch HOME
```

Then from the scratch HOME's perspective: `~/bin_ish paths` … skip the
`--fetch-image` unless you have patience; the installer must exit green on
dependencies and print the correct "run `SBX_ROOT=… sbxvm fetch-image`" hint.

## 9. Known pitfalls (verbatim from actual incidents — re-read before releasing)

- `RunTO`/`Run` are **CombinedOutput** (blocking) — never use them for streaming
  paths (logs/SSE → separate piped impls in `engine`).
- SSE log payloads are **JSON strings**; the browser must `JSON.parse` before
  appending — do not regress this into raw `ev.data` rendering.
- The libvirt daemon may run qemu **as root** on a host → `vm/vms/<name>/serial.log`
  comes out `root:0600`. The logs path therefore prefers readable serial, then
  falls back to the guest journal over the kit's ssh channel. Keep both tiers.
- `libvirtSerialCapture` check must match `<serial` + `type='file'` on the same
  line (disks are `type='file'` too);
- Suite P1 needs ≥1 running sandbox; never call the suite from an empty machine
  without the §4 dance.
- Kit names keep their case (virsh domains are case-sensitive); the dashboard
  API lowercases container names only.
- Do not "fix" the hermetic suite ports; the daemon/proxy/sweeper port matrix
  is deliberately separate from the live one.
- `dist/` goes into releases, `.gitignore` keeps the tree clean; if
  `git status` ever shows `vm/images/…` staged, **stop and reset**.

## 10. Where things live (quick map)

- `Makefile`, `scripts/release.sh`, `install.sh` — build/install/deps (this scaffold)
- `tests/sbxddsmoke.sh` — full verification suite (the §4 gate)
- `sbxd/internal/{api,engine,sandbox,proxy,keys,ledger,cfgsync,config,attach,audit,env,cli}` — daemon modules
- `sbxd/internal/api/web.go` — embedded dashboard (HTML/CSS/JS raw strings)
- `vm/sbxvm.sh` — kit (libvirt + docker VM backend, snapshots, serial seeding)
- `bin/sbx` — container CLI; `containers/Dockerfile` + `entrypoint.sh` — stack images
- `vm/sbxvmd/Dockerfile` — docker-VM backend image (build/docker-pull target)
- `README.md`, `SECURITY.md` — user-facing docs
