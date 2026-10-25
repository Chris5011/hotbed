#!/usr/bin/env bash
# vmsmoke.sh — end-to-end verification of sbxvm (phase 2).
# Creates a temporary VM, checks ssh/9p/opencode/snapshot/revert, destroys it.
# Takes several minutes (VM boot + first cloud-init run). Run:  tests/vmsmoke.sh
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SBXVM="$ROOT/vm/sbxvm.sh"
VMNAME="sbxvmsmoke-$(date +%s | tail -c 5)"
SHARE="$(mktemp -d /tmp/opencode/sbxvmsmoke.XXXXXX)"
PASS=0; FAIL=0

pass() { printf '  \e[32m✔\e[0m %s\n' "$1"; PASS=$((PASS + 1)); }
fail() { printf '  \e[31m✘\e[0m %s\n' "$1"; FAIL=$((FAIL + 1)); }
hdr()  { printf '\n\e[1m== %s ==\e[0m\n' "$*"; }

cleanup() {
    "$SBXVM" destroy "$VMNAME" --yes >/dev/null 2>&1 || true
    rm -rf "$SHARE"
}
trap cleanup EXIT

hdr "runtime"
if [[ -f "$ROOT/vm/images/debian-13-genericcloud-amd64.qcow2" ]]; then pass "master image present"; else fail "master image (run sbxvm fetch-image)"; fi
"$SBXVM" ls >/dev/null 2>&1 && pass "libvirt reachable" || fail "libvirt reachable"

hdr "create + boot ($VMNAME)"
if "$SBXVM" create "$VMNAME" --cpu 2 --mem 3072 --disk 20 --share "$SHARE" >/dev/null 2>&1; then
    pass "create + ssh up"
else
    fail "create + ssh up (serial log: $ROOT/vm/vms/$VMNAME/serial.log)"
    echo; printf '\e[31;1mSMOKE: aborted\e[0m\n'; exit 1
fi

hdr "inside the VM"
out="$("$SBXVM" ssh "$VMNAME" -- whoami 2>/dev/null)"
[[ "$out" == dev ]] && pass "ssh works (user dev)" || fail "ssh works (user dev) ($(echo "$out" | tail -1))"
out="$("$SBXVM" ssh "$VMNAME" -- sudo -n true && echo sudo-ok 2>/dev/null)"
[[ "$out" == *sudo-ok* ]] && pass "passwordless sudo inside VM" || fail "passwordless sudo inside VM"
out="$("$SBXVM" ssh "$VMNAME" -- test -f /workspace/probe.txt && echo share-ok 2>/dev/null)"
echo -n "$(date) smoke-probe" > "$SHARE/probe.txt"
out="$("$SBXVM" ssh "$VMNAME" -- cat /workspace/probe.txt 2>/dev/null)"
[[ "$out" == *smoke-probe* ]] && pass "9p share visible at /workspace" || fail "9p share at /workspace (host->guest)"
"$SBXVM" ssh "$VMNAME" -- 'touch /workspace/from-vm.txt' >/dev/null 2>&1
[[ -f "$SHARE/from-vm.txt" ]] && pass "guest writes visible on host" || fail "guest writes visible on host"
out="$("$SBXVM" ssh "$VMNAME" -- /usr/local/bin/opencode --version 2>/dev/null | tail -1)"
[[ "$out" =~ ^[0-9]+(\.[0-9]+){2}$ ]] && pass "opencode installed in VM (v$out)" || fail "opencode in VM ($out)"
out="$("$SBXVM" ssh "$VMNAME" -- systemctl is-active docker 2>/dev/null)"
[[ "$out" == active ]] && pass "docker service running" || fail "docker service running"

hdr "snapshots"
"$SBXVM" ssh "$VMNAME" -- 'echo snapshotproof | sudo tee /root/proof.txt >/dev/null' >/dev/null 2>&1
"$SBXVM" snapshot "$VMNAME" save t1 >/dev/null && pass "snapshot saved" || fail "snapshot saved"
"$SBXVM" ssh "$VMNAME" -- 'sudo rm -f /root/proof.txt' >/dev/null 2>&1
"$SBXVM" snapshot "$VMNAME" revert t1 >/dev/null 2>&1 && pass "revert executed" || fail "revert executed"
"$SBXVM" start "$VMNAME" >/dev/null 2>&1 && pass "restart after revert" || fail "restart after revert"
out="$("$SBXVM" ssh "$VMNAME" -- 'sudo test -f /root/proof.txt && echo proof-ok' 2>/dev/null)"
[[ "$out" == *proof-ok* ]] && pass "file resurrected from snapshot" || fail "file resurrected from snapshot"

hdr "teardown"
"$SBXVM" destroy "$VMNAME" --yes >/dev/null 2>&1 && pass "destroy" || fail "destroy"
[[ ! -e "$ROOT/vm/vms/$VMNAME" ]] && pass "vm dir cleaned" || fail "vm dir cleaned"
if "$SBXVM" ls | awk '{print $1}' | grep -qxF "$VMNAME"; then fail "domain undefined"; else pass "domain undefined"; fi

echo
if [[ $FAIL -eq 0 ]]; then
    printf '\e[32;1mVMSMOKE: all %d checks passed ✓\e[0m\n' "$PASS"
else
    printf '\e[31;1mVMSMOKE: %d failed / %d passed\e[0m\n' "$FAIL" "$PASS"
    exit 1
fi
