#!/usr/bin/env bash
# vmdsmoke.sh — end-to-end verification of the sbx_vm docker backend
# (SBXVM_BACKEND=docker: qemu inside a plain container, "sbxvmd").
# Creates a temporary VM, checks ssh/9p/opencode/nested-docker/snapshot/
# revert, destroys it. Takes several minutes (VM boot + first cloud-init
# run). Requires nested KVM (/dev/kvm usable in containers).
# Run:  tests/vmdsmoke.sh
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SBXVM="$ROOT/vm/sbxvm.sh"
export SBXVM_BACKEND=docker
VMNAME="sbxvmdsmoke-$(date +%s | tail -c 5)"
SHARE="$(mktemp -d /tmp/opencode/sbxvmdsmoke.XXXXXX)"
PASS=0; FAIL=0

pass() { printf '  \e[32m✔\e[0m %s\n' "$1"; PASS=$((PASS + 1)); }
fail() { printf '  \e[31m✘\e[0m %s\n' "$1"; FAIL=$((FAIL + 1)); }
hdr()  { printf '\n\e[1m== %s ==\e[0m\n' "$*"; }

cleanup() {
    "$SBXVM" destroy "$VMNAME" --yes >/dev/null 2>&1 || true
    rm -rf "$SHARE"
}
trap cleanup EXIT

run() { "$SBXVM" "$@" 2>&1; }

hdr "runtime"
if [[ -f "$ROOT/vm/images/debian-13-genericcloud-amd64.qcow2" ]]; then pass "master image present"; else fail "master image (run sbxvm fetch-image)"; fi
docker version --format '{{.Server.Version}}' >/dev/null 2>&1 \
    && pass "docker engine reachable" || fail "docker engine reachable"

hdr "create + boot ($VMNAME)"
if run create "$VMNAME" --cpu 2 --mem 3072 --disk 20 --share "$SHARE" >/dev/null; then
    pass "create + ssh up (docker backend)"
else
    fail "create + ssh up (docker backend)  — see: docker logs sbxvmd-$VMNAME && docker exec sbxvmd-$VMNAME sbxvmd-entry serial"
    echo; printf '\e[31;1mSMOKE: aborted\e[0m\n'; exit 1
fi

hdr "inside the VM"
out="$(run ssh "$VMNAME" -- whoami | tail -1)"
[[ "$out" == dev ]] && pass "ssh works (user dev)" || fail "ssh works (user dev) ($out)"
out="$(run ssh "$VMNAME" -- 'sudo -n true && echo sudo-ok' | tail -1)"
[[ "$out" == *sudo-ok* ]] && pass "passwordless sudo inside VM" || fail "passwordless sudo inside VM"
printf 'vmdsmoke-file host->guest\n' > "$SHARE/probe.txt"
out="$(run ssh "$VMNAME" -- cat /workspace/probe.txt | tail -1)"
[[ "$out" == *vmdsmoke-file* ]] && pass "9p share: host -> guest (/workspace)" || fail "9p share host->guest"
run ssh "$VMNAME" -- 'touch /workspace/from-vm.txt' >/dev/null
[[ -f "$SHARE/from-vm.txt" ]] && pass "9p share: guest -> host" || fail "9p share guest->host"
out="$(run ssh "$VMNAME" -- /usr/local/bin/opencode --version | tail -1)"
[[ "$out" =~ ^[0-9]+(\.[0-9]+){2}$ ]] && pass "opencode installed in VM (v$out)" || fail "opencode in VM ($out)"
out="$(run ssh "$VMNAME" -- uname -r | tail -1)"
[[ "$out" == *cloud* ]] && fail "full kernel booted (got: $out)" \
    || pass "full kernel booted ($out)"
# Nested docker: the hello-world pull goes out through slirp and can be
# transiently slow on a fresh VM, so pull first (with retries) and run
# second. Keep stderr for diagnostics. Filter out the CLI's own
# "sbxvm: ssh dev@host:port" info banner before matching.
out="$(run ssh "$VMNAME" -- \
    'for i in 1 2 3; do sudo docker pull hello-world >/dev/null 2>&1 && break; sleep 6; done
     sudo docker run --rm hello-world' \
    | grep -av 'sbxvm:')"
[[ "$out" == *Hello* ]] && pass "nested docker container runs in VM" \
    || fail "nested docker in VM ($(printf '%s\n' "$out" | tail -n 1))"

hdr "snapshots"
run ssh "$VMNAME" -- 'echo snapshotproof | sudo tee /root/proof.txt >/dev/null' >/dev/null
run snapshot "$VMNAME" save t1 >/dev/null && pass "snapshot saved (offline stop/start cycle)" || fail "snapshot saved"
run ssh "$VMNAME" -- 'sudo rm -f /root/proof.txt' >/dev/null
run snapshot "$VMNAME" revert t1 >/dev/null && pass "revert executed" || fail "revert executed"
run start "$VMNAME" >/dev/null && pass "restart after revert (fresh host port)" || fail "restart after revert"
out="$(run ssh "$VMNAME" -- 'sudo test -f /root/proof.txt && echo proof-ok' | tail -1)"
[[ "$out" == *proof-ok* ]] && pass "file resurrected from snapshot" || fail "file resurrected from snapshot"

hdr "teardown"
run destroy "$VMNAME" --yes >/dev/null && pass "destroy" || fail "destroy"
[[ ! -e "$ROOT/vm/vms/$VMNAME" ]] && pass "vm dir cleaned" || fail "vm dir cleaned"
docker ps -a --format '{{.Names}}' | grep -qxF "sbxvmd-$VMNAME" \
    && fail "container undefined" || pass "container undefined"

echo
if [[ $FAIL -eq 0 ]]; then
    printf '\e[32;1mVMSMOKE(docker): all %d checks passed ✓\e[0m\n' "$PASS"
else
    printf '\e[31;1mVMSMOKE(docker): %d failed / %d passed\e[0m\n' "$FAIL" "$PASS"
    exit 1
fi
