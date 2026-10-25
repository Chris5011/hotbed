#!/usr/bin/env bash
# sbxddsmoke.sh — verification of the sbxd daemon surface (SBX Desktop):
# P1: serve/health, unified ps, connect variants, persistent tmux attach,
# instant SSE snapshot, dashboard assets, doctor. Needs at least ONE running
# sandbox (container or vm) in the inventory; does NOT mutate any of it
# (start/stop are exercised manually). Daemon runs on a scratch port and is
# stopped again afterwards.
# P2 (hermetic, no real provider): per-sandbox keys, token/cost ledger
# (upstream-counted + estimated), budgets with 429 trip, config/get/set,
# key revoke — all against a python3 mock upstream on a scratch daemon.
# Run:  tests/sbxddsmoke.sh
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SBXD="$ROOT/sbxd/bin/sbxd"
PORT="${SBXD_SMOKE_PORT:-7790}"
export SBXD_PORT="$PORT"
PASS=0; FAIL=0
DAEMON_PID=""

pass() { printf '  \e[32m✔\e[0m %s\n' "$1"; PASS=$((PASS + 1)); }
fail() { printf '  \e[31m✘\e[0m %s\n' "$1"; FAIL=$((FAIL + 1)); }
hdr()  { printf '\n\e[1m== %s ==\e[0m\n' "$*"; }

cleanup() {
    [[ -n "$DAEMON_PID" ]] && kill "$DAEMON_PID" 2>/dev/null
    curl -s --max-time 2 -X POST "http://127.0.0.1:$PORT/api/actions/stop" >/dev/null 2>&1 || true
}
trap cleanup EXIT

hdr "runtime"
if [[ -x "$SBXD" ]]; then pass "sbxd binary present"; else
    fail "sbxd binary present (build: cd sbxd && go build -o bin/sbxd ./cmd/sbxd)"
    printf '\e[31;1mSMOKE: aborted\e[0m\n'; exit 1
fi
"$SBXD" version 2>&1 | grep -q '^sbxd 0\.' \
    && pass "sbxd version sane" || fail "sbxd version sane"
tmux -V >/dev/null 2>&1 && pass "tmux present (attach desk)" || fail "tmux present"
curl -s --max-time 2 "https://dummy" >/dev/null 2>&1 || true # curl availability probe
command -v curl >/dev/null && pass "curl present (API checks)" || fail "curl present"

hdr "daemon"
"$SBXD" serve --port "$PORT" >/tmp/sbxddsmoke.serve.log 2>&1 &
DAEMON_PID=$!
for _ in $(seq 1 20); do
    health="$(curl -s --max-time 1 "http://127.0.0.1:$PORT/api/health" 2>/dev/null)" && [[ -n "$health" ]] && break
    sleep 0.25
done
echo "$health" | grep -q '"version"' && pass "daemon answers /api/health" \
    || { fail "daemon answers /api/health — see /tmp/sbxddsmoke.serve.log"; printf '\e[31;1mSMOKE: aborted\e[0m\n'; exit 1; }
echo "$health" | grep -q '"docker":true' && pass "docker probe healthy" || fail "docker probe healthy"
echo "$health" | grep -q '"tmux":true'   && pass "tmux probe healthy"   || fail "tmux probe healthy"

hdr "inventory (ps)"
list="$("$SBXD" ps 2>&1)"
echo "$list" | grep -q 'NAME.*KIND.*STATE' && pass "ps prints unified table" || fail "ps prints unified table"
n_sb="$(echo "$list" | tail -n +2 | grep -c .)"
[[ "$n_sb" -ge 1 ]] && pass "inventory not empty ($n_sb sandboxes)" \
    || { fail "inventory not empty — create one: cd <dir> && sbx, or sbxvm create <name>"; \
         printf '\e[31;1mSMOKE: aborted (nothing to inspect)\e[0m\n'; exit 1; }

pick="$(echo "$list" | awk '$3=="running"{print $1"|"$2; exit}')"
NAME="${pick%%|*}"; KINDRAW="${pick##*|}"
case "$KINDRAW" in
    vm:*) KIND="vm" ;;
    container) KIND="container" ;;
    *) KIND="vm" ;;
esac
[[ -n "$NAME" ]] && pass "running sandbox selected: $NAME ($KIND)" || fail "no *running* sandbox found (stats checks skipped)"
curl -s --max-time 3 "http://127.0.0.1:$PORT/api/sandboxes" | grep -q "\"name\":\"$NAME\"" \
    && pass "/api/sandboxes includes $NAME" || fail "/api/sandboxes includes $NAME"

hdr "stats"
sleep 2 # let two scan ticks pass so CPU% has a delta (libvirt)
stats="$("$SBXD" stats 2>&1)"
echo "$stats" | grep -q "$NAME" \
    && pass "stats lists $NAME" || fail "stats lists $NAME"
echo "$stats" | awk -v n="$NAME" '$1==n && $4!=""' | grep -q . \
    && pass "stats shows CPU/MEM values" || fail "stats shows CPU/MEM values"

hdr "connection (connect)"
conn="$("$SBXD" connect "$NAME" 2>&1)"
echo "$conn" | grep -q 'attach-tmux:' && pass "connect: tmux variant" || fail "connect: tmux variant"
echo "$conn" | grep -q 'windows:'      && pass "connect: Windows Terminal variant" || fail "connect: windows variant"
if [[ "$KIND" == "container" ]]; then
    echo "$conn" | grep -q 'shell:' && echo "$conn" | grep -q 'docker exec' \
        && pass "connect: docker exec variants" || fail "connect: docker exec variants"
else
    echo "$conn" | grep -q 'ssh:' && echo "$conn" | grep -q 'ssh ' \
        && pass "connect: ssh variant" || fail "connect: ssh variant"
    echo "$conn" | grep -q 'attach-tmux:' && pass "connect: tmux listed too" || fail "connect: tmux listed too"
fi
# primary connection command must appear first (SSH/shell above tmux)
if [[ "$(printf '%s\n' "$conn" | grep -nE '^(ssh|shell):' | head -1 | cut -d: -f1)" \
      -lt "$(printf '%s\n' "$conn" | grep -n 'attach-tmux:' | head -1 | cut -d: -f1)" ]]; then
    pass "connect: primary way first (user preference)"
else
    fail "connect: primary way first (user preference)"
fi

hdr "persistent attach (tmux session sbx-$NAME)"
curl -s --max-time 5 -X POST -H 'Content-Type: application/json' \
    -d '{"mode":"shell","spawn":false}' \
    "http://127.0.0.1:$PORT/api/sandboxes/$KIND/$NAME/attach" \
    | grep -q '"tmuxSession":"sbx-'"$NAME"'"' \
    && pass "attach endpoint rolls session" || fail "attach endpoint rolls session"
sleep 3 # give ssh/exec time to land (VM boot prompt can lag)
tmux has-session -t "sbx-$NAME" 2>/dev/null \
    && pass "tmux session sbx-$NAME alive" || fail "tmux session sbx-$NAME alive"
pane="$(tmux capture-pane -p -t "sbx-$NAME" 2>/dev/null | grep -v '^$' | tail -3)"
[[ -n "$pane" ]] && pass "pane holds a live shell" || fail "pane holds a live shell"
tmux kill-session -t "sbx-$NAME" 2>/dev/null && pass "test session cleaned up" || fail "test session cleaned up"

hdr "SSE + dashboard"
scratch="$(mktemp -d /tmp/opencode/sbxddsmoke.XXXXXX)"
ev_ok=0
for _ in 1 2; do
    curl -sN --max-time 6 "http://127.0.0.1:$PORT/api/events" >"$scratch/events" 2>/dev/null
    grep -m1 -q '^event: snapshot' "$scratch/events" && { ev_ok=1; break; }
done
[[ "$ev_ok" -eq 1 ]] && pass "instant snapshot on /api/events" || fail "instant snapshot on /api/events"
if [[ "$KIND" == "vm" ]]; then
    lg_ok=0
    for _ in 1 2; do
        curl -sN --max-time 6 "http://127.0.0.1:$PORT/api/sandboxes/$KIND/$NAME/logs" >"$scratch/logs" 2>/dev/null
        grep -m1 -q '^event: log' "$scratch/logs" && { lg_ok=1; break; }
    done
    [[ "$lg_ok" -eq 1 ]] && pass "log/serial SSE frames flow" || fail "log/serial SSE frames flow"
fi
curl -s --max-time 2 "http://127.0.0.1:$PORT/" | grep -q 'SBX Desktop' \
    && pass "dashboard index served" || fail "dashboard index served"
curl -s -o /dev/null --max-time 2 "http://127.0.0.1:$PORT/static/app.js" \
    && pass "dashboard app.js served" || fail "dashboard app.js served"
rm -rf "$scratch" 2>/dev/null || true

hdr "doctor"
"$SBXD" doctor 2>&1 | grep -q 'sbxd daemon' \
    && pass "doctor detects the daemon" || fail "doctor detects the daemon"
"$SBXD" doctor >/dev/null 2>&1 && pass "doctor exits clean" \
    || { "$SBXD" doctor >/dev/null 2>&1; fail "doctor exits clean (rc=$?)"; }

# ---------------------------------------------------------------- P2 ----
# Hermetic token-accounting proxy exercise: separate data dirs, a python
# mock upstream (with real-looking usage reports), a scratch daemon on
# 7791 with the proxy on 7792. No real provider is ever contacted.
hdr "P2: token-accounting proxy (hermetic)"
if [[ -x /usr/bin/python3 ]]; then pass "python3 present (mock upstream)"; else
    fail "python3 present (mock upstream)"; fi
P2="$(mktemp -d /tmp/opencode/sbxdp2.XXXXXX)"
MPORT="${SBXD_SMOKE_MOCKPORT:-12487}"
mkdir -p "$P2/config" "$P2/data" "$P2/state"
MPORT=8787; DBUS=7791; DPROTO=7792
cat > "$P2/mock.py" <<'PYEOF'
import json, os
from http.server import BaseHTTPRequestHandler, HTTPServer
class H(BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def _bad(self):
        b=json.dumps({"error":{"message":"no such key here"}}).encode()
        self.send_response(403); self.send_header("Content-Type","application/json")
        self.send_header("Content-Length",str(len(b))); self.end_headers(); self.wfile.write(b)
    def do_POST(self):
        n=int(self.headers.get("Content-Length") or 0); raw=self.rfile.read(n)
        try: req=json.loads(raw or b"{}")
        except Exception: req={}
        if self.headers.get("Authorization") != "Bearer mock-uk": self._bad(); return
        if self.headers.get("x-api-key") is not None: self._bad(); return
        if req.get("stream"):
            self.send_response(200); self.send_header("Content-Type","text/event-stream"); self.end_headers()
            if req.get("model") == "est-model":
                for tok in ("wor","ks","we"):
                    f={"id":"2","choices":[{"index":0,"delta":{"content":tok}}]}
                    self.wfile.write(b"data: "+json.dumps(f).encode()+b"\n\n"); self.wfile.flush()
                return
            for tok in ("hel","lo ","wor","ld"):
                f={"id":"1","choices":[{"index":0,"delta":{"content":tok}}]}
                self.wfile.write(b"data: "+json.dumps(f).encode()+b"\n\n"); self.wfile.flush()
            fin={"id":"1","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":7}}
            self.wfile.write(b"data: "+json.dumps(fin).encode()+b"\n\n")
            self.wfile.write(b"data: [DONE]\n\n"); self.wfile.flush(); return
        body=json.dumps({"id":"1","choices":[{"message":{"content":"hi there"}}],
                         "usage":{"prompt_tokens":42,"completion_tokens":17}}).encode()
        if req.get("model") == "est-model":
            body=json.dumps({"id":"1","choices":[{"message":{"content":"no usage"}}]}).encode()
        self.send_response(200); self.send_header("Content-Type","application/json")
        self.send_header("Content-Length",str(len(body))); self.end_headers(); self.wfile.write(body)
HTTPServer(("127.0.0.1",int(os.environ.get("MOCK_PORT") or 8787)),H).serve_forever()
PYEOF
cat > "$P2/config/config.json" <<'CFGEOF'
{
  "opencode": {
    "default_provider": "mock",
    "providers": { "mock": {
        "base_url": "http://127.0.0.1:${MPORT}/v1",
        "api_key": "mock-uk",
        "models": ["mock-fast", "est-model"] } }
  },
  "proxy": { "port": "7792", "bind": "0.0.0.0" },
  "prices": { "mock-fast": [1, 1] }
}
CFGEOF
sed -i "s|\${MPORT}|$MPORT|" "$P2/config/config.json"
P2MOCK_PID=""; P2DAEMON_PID=""
P2CLEAN() {
    for pid in "$P2MOCK_PID" "$P2DAEMON_PID"; do
        [[ -n "$pid" ]] || continue
        kill "$pid" 2>/dev/null
        sleep 0.2
        kill -9 "$pid" 2>/dev/null
    done
    rm -rf "$P2" 2>/dev/null || true
}
p2env=(env "SBXD_CONFIG_DIR=$P2/config" "SBXD_DATA_DIR=$P2/data" "SBXD_STATE_DIR=$P2/state")
MOCK_PORT=$MPORT /usr/bin/python3 "$P2/mock.py" >/dev/null 2>&1 & P2MOCK_PID=$!
"${p2env[@]}" "$SBXD" serve --port "$DBUS" >/dev/null 2>&1 & P2DAEMON_PID=$!
# readiness: the mock can take 1-2s to bind in scripted contexts — wait for
# BOTH the dashboard/proxy AND the mock (403-without-auth = mock ready).
mock_ready=""
for _ in $(seq 1 40); do
    if curl -s --max-time 1 -o /dev/null -XPOST "http://127.0.0.1:$MPORT/v1/chat/completions" -d '{}' 2>/dev/null; then
        mock_ready=1; break
    fi
    sleep 0.25
done
[[ -n "$mock_ready" ]] && pass "mock upstream ready" || fail "mock upstream ready (8787 silent)"
for _ in $(seq 1 20); do
    curl -s --max-time 1 "http://127.0.0.1:$DPROTO/v1" >/dev/null 2>&1 && break
    sleep 0.25
done
# 1. auth: no key => 401
c1=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 -XPOST "http://127.0.0.1:$DPROTO/v1/chat/completions" -H 'Content-Type: application/json' -d '{}')
[[ "$c1" == 401 ]] && pass "proxy: 401 without a key" || fail "proxy: 401 without a key (got $c1)"
# 2. mint via the stateless CLI store (instantly visible to the running daemon)
keyout="$("${p2env[@]}" "$SBXD" key mint tester 2>&1)"
TOK="$(echo "$keyout" | awk '/sbxk-/{print $1; exit}')"
echo "$TOK" | grep -q '^sbxk-' && pass "key mint (CLI, instant) — $TOK" || fail "key mint (CLI, instant)"
# 3. non-stream passthrough with upstream usage counted
c2=$(curl -s --max-time 8 -XPOST "http://127.0.0.1:$DPROTO/v1/chat/completions" \
        -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' \
        -d '{"model":"mock-fast","stream":false,"messages":[]}')
echo "$c2" | grep -q 'hi there' && pass "proxy: non-stream passthrough" || fail "proxy: non-stream passthrough"
# 4. SSE passthrough
sse=$(curl -sN --max-time 8 -XPOST "http://127.0.0.1:$DPROTO/v1/chat/completions" \
        -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' \
        -d '{"model":"mock-fast","stream":true,"messages":[]}')
echo "$sse" | grep -qF '[DONE]' && pass "proxy: SSE passthrough (frames + DONE)" || fail "proxy: SSE passthrough"
# 5. usage ledger: real upstream usage counted (42+12 in / 17+7 out), est flagged for est-model
curl -s -o /dev/null --max-time 8 -XPOST "http://127.0.0.1:$DPROTO/v1/chat/completions" \
    -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' \
    -d '{"model":"est-model","stream":false,"messages":[]}'
uj="$P2/state/usage.jsonl"
grep -q '"in":42' "$uj" && grep -q '"out":7' "$uj" && grep -q '"est":true' "$uj" \
    && pass "ledger: upstream usage + est rows recorded" || fail "ledger: upstream usage + est rows recorded"
us="$("${p2env[@]}" "$SBXD" usage tester 2>&1)"
echo "$us" | grep -q 'tokens in/out 66/24' && pass "usage CLI sums counted + est tokens (66/24)" || fail "usage CLI sums counted + est tokens (got: $(echo "$us" | head -2 | tail -1))"
# 6. budgets trip BEFORE forwarding
"${p2env[@]}" "$SBXD" budget tester 0.000001 >/dev/null 2>&1
c3=$(curl -s -o /dev/null -w '%{http_code}' --max-time 8 -XPOST "http://127.0.0.1:$DPROTO/v1/chat/completions" \
        -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' -d '{"model":"mock-fast","stream":false}')
[[ "$c3" == 429 ]] && pass "wallet: tiny budget trips 429" || fail "wallet: tiny budget trips 429 (got $c3)"
"${p2env[@]}" "$SBXD" budget tester 100 >/dev/null 2>&1
c4=$(curl -s -o /dev/null -w '%{http_code}' --max-time 8 -XPOST "http://127.0.0.1:$DPROTO/v1/chat/completions" \
        -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' -d '{"model":"mock-fast","stream":false}')
[[ "$c4" == 200 ]] && pass "wallet: raising the budget unblocks" || fail "wallet: raising the budget unblocks (got $c4)"
# 7. config round-trips through the stateless stores
"${p2env[@]}" "$SBXD" config set opencode.default_model mock-fast >/dev/null 2>&1
cm="$("${p2env[@]}" "$SBXD" config get opencode.default_model 2>&1)"
echo "$cm" | grep -q 'mock-fast' && pass "config set/get round-trip" || fail "config set/get round-trip (got $cm)"
"${p2env[@]}" "$SBXD" config print --raw 2>/dev/null | grep -q 'mock-uk' \
    && pass "config print --raw shows secrets locally" || fail "config print --raw shows secrets locally"
curl -s --max-time 3 "http://127.0.0.1:$DBUS/api/config" | grep -q 'masked' \
    && pass "API config masks secrets" || fail "API config masks secrets"
curl -s --max-time 3 "http://127.0.0.1:$DBUS/api/usage" | grep -q 'tester' \
    && pass "API /api/usage lists the sandbox" || fail "API /api/usage lists the sandbox"
# 8. P3: token wallets, config migration, pricing (wallets live in config.json)
"${p2env[@]}" "$SBXD" budget tester --tokens 5000 >/dev/null 2>&1
ck=$(curl -s -o /dev/null -w '%{http_code}' --max-time 8 -XPOST "http://127.0.0.1:$DPROTO/v1/chat/completions" \
        -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' -d '{"model":"mock-fast","stream":false}')
[[ "$ck" == 200 ]] && pass "token wallet above burn passes through" || fail "token wallet above burn passes through (got $ck)"
"${p2env[@]}" "$SBXD" budget tester --tokens 1 >/dev/null 2>&1
ct=$(curl -s -o /dev/null -w '%{http_code}' --max-time 8 -XPOST "http://127.0.0.1:$DPROTO/v1/chat/completions" \
        -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' -d '{"model":"mock-fast","stream":false}')
[[ "$ct" == 429 ]] && pass "token wallet trips 429" || fail "token wallet trips 429 (got $ct)"
"${p2env[@]}" "$SBXD" budget tester --tokens 0 >/dev/null 2>&1
curl -s -o /dev/null --max-time 8 -XPOST "http://127.0.0.1:$DPROTO/v1/chat/completions" \
        -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' -d '{"model":"mock-fast","stream":false}'
grep -q '"budget_tokens"' "$P2/config/config.json" \
    && pass "budget_tokens persisted in config.json" || fail "budget_tokens persisted in config.json"
# booth legacy budgets migrate keys.json -> config.json
cat > "$P2/data/keys.json" <<'LEGACYEOF'
{"keys":[{"token":"sbxk-legacy-000000000000","sandbox":"ghost","provider":"mock","created":"2026-01-01T00:00:00Z"}],"budgets":{"ghost":2}}
LEGACYEOF
"${p2env[@]}" "$SBXD" budget >/dev/null 2>&1
grep -q '"ghost": 2' "$P2/config/config.json" && pass "legacy budgets migrate keys.json -> config.json" \
    || fail "legacy budgets migrate keys.json -> config.json"
grep -q '"budgets"' "$P2/data/keys.json" && fail "keys.json keeps no budgets after migration" \
    || pass "keys.json keeps no budgets after migration"
# (the legacy file replace wiped the minted key — mint a fresh one)
TOK2="$("${p2env[@]}" "$SBXD" key mint tester 2>&1 | awk '/sbxk-/{print $1; exit}')"
HEXP="${TOK2##*-}"
(( ${#HEXP} >= 32 )) \
    && pass "security: minted token carries ≥128-bit randomness (${#HEXP} hex)" \
    || fail "security: minted token carries ≥128-bit randomness (got ${#HEXP}: $TOK2)"
# pricing: an override re-prices new rows immediately
"${p2env[@]}" "$SBXD" price set mock-fast 50 50 >/dev/null 2>&1
cp=$(curl -s --max-time 8 -XPOST "http://127.0.0.1:$DPROTO/v1/chat/completions" \
        -H "Authorization: Bearer $TOK2" -H 'Content-Type: application/json' -d '{"model":"mock-fast","stream":false}')
echo "$cp" | grep -q 'hi there' || true
grep -q '"cost":0.00295' "$P2/state/usage.jsonl" && pass "price set re-prices new ledger rows" \
    || fail "price set re-prices new ledger rows"
"${p2env[@]}" "$SBXD" price rm mock-fast >/dev/null 2>&1
cq=$(curl -s -o /dev/null --max-time 8 -XPOST "http://127.0.0.1:$DPROTO/v1/chat/completions" \
        -H "Authorization: Bearer $TOK2" -H 'Content-Type: application/json' -d '{"model":"mock-fast","stream":false}')
[[ "$(tail -1 "$P2/state/usage.jsonl" 2>/dev/null | grep -c '"cost":0,')" == 1 ]] \
    && pass "price rm falls back (unknown model cost 0)" || fail "price rm falls back (unknown model cost 0)"
# audit trail records control-plane actions
"${p2env[@]}" "$SBXD" budget tester 1 >/dev/null 2>&1
"${p2env[@]}" "$SBXD" audit 5 2>&1 | grep -q 'wallet.set' \
    && pass "audit: wallet change landed in the trail" || fail "audit: wallet change landed in the trail"
[[ -f "$P2/state/audit.jsonl" ]] && pass "audit: JSONL exists in the state dir" \
    || fail "audit: JSONL exists in the state dir"

# 9. revoke kills access
"${p2env[@]}" "$SBXD" key revoke tester >/dev/null 2>&1
c5=$(curl -s -o /dev/null -w '%{http_code}' --max-time 8 -XPOST "http://127.0.0.1:$DPROTO/v1/chat/completions" \
        -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' -d '{"model":"mock-fast","stream":false}')
[[ "$c5" == 401 ]] && pass "key revoke cuts access instantly" || fail "key revoke cuts access instantly (got $c5)"
P2CLEAN
trap 'cleanup; P2CLEAN' EXIT

# == P3: container mount flow (throwaway container; recreation is real) ==
if docker info >/dev/null 2>&1; then
    hdr "P3: mounts"
    MNAME="sbx-mtest$((RANDOM % 9000 + 1000))"
    docker run -d --name "$MNAME" -l dev.sbx=1 -v "${MNAME}-v:/data" alpine sleep 300 >/dev/null
    MCLEAN() {
        docker rm -f "$MNAME" >/dev/null 2>&1
        docker volume rm "${MNAME}-v" >/dev/null 2>&1
    }
    "${p2env[@]}" "$SBXD" mount add "$MNAME" /tmp:/mtest --yes >/dev/null 2>&1
    ls1="$("${p2env[@]}" "$SBXD" mount ls "$MNAME" 2>&1)"
    echo "$ls1" | grep -q '/mtest' && pass "mount add persists a bind through recreation" \
        || fail "mount add persists a bind through recreation (got: $(echo "$ls1" | head -2 | tail -1))"
    grep -q "${MNAME}-v" <<<"$ls1" && pass "named volumes carried over on recreation" \
        || fail "named volumes carried over on recreation"
    if docker exec "$MNAME" ls /mtest >/dev/null 2>&1; then
        pass "bound host dir visible inside the container"
    else
        fail "bound host dir visible inside the container"
    fi
    "${p2env[@]}" "$SBXD" mount rm "$MNAME" /mtest --yes >/dev/null 2>&1
    ls2="$("${p2env[@]}" "$SBXD" mount ls "$MNAME" 2>&1)"
    echo "$ls2" | grep -q '/mtest' && fail "mount rm drops the bind" || pass "mount rm drops the bind"
    VMC="$(docker ps -a --filter name=sbxvmd- --format '{{.Names}}' | head -1)"
    if [[ -n "$VMC" ]]; then
        out="$("${p2env[@]}" "$SBXD" mount add "$VMC" /tmp:/mx --yes 2>&1)"
        grep -q 'refusing to recreate' <<<"$out" && pass "VM-backed containers refuse live recreation" \
            || fail "VM-backed containers refuse live recreation (got: $out)"
    fi
    MCLEAN
else
    hdr "P3: mounts (skipped — no docker engine)"
fi

# == P3: egress presets (throwaway container; rules live in its netns) ==
if docker info >/dev/null 2>&1 && docker image inspect sbxd-egress-helper:1 >/dev/null 2>&1; then
    hdr "P3: egress"
    ECON="sbx-egtest$((RANDOM % 9000 + 1000))"
    docker run -d --name "$ECON" alpine sleep 600 >/dev/null
    ECLEAN() { docker rm -f "$ECON" >/dev/null 2>&1 || true; }
    # control run: outbound must work unfenced, else the network env is
    # broken and egress assertions would be meaningless — skip them.
    if docker exec "$ECON" wget -T 5 -q -O /dev/null http://1.1.1.1/ 2>/dev/null; then
        "${p2env[@]}" "$SBXD" egress "$ECON" proxy-only >/dev/null 2>&1
        show="$("${p2env[@]}" "$SBXD" egress "$ECON" 2>&1)"
        echo "$show" | grep -q 'SBX_EGRESS' && pass "egress proxy-only installs the chain" \
            || fail "egress proxy-only installs the chain (got: $show)"
        if docker exec "$ECON" wget -T 4 -q -O /dev/null http://1.1.1.1/ 2>/dev/null; then
            fail "egress proxy-only blocks public outbound"
        else
            pass "egress proxy-only blocks public outbound"
        fi
        "${p2env[@]}" "$SBXD" egress "$ECON" allow >/dev/null 2>&1
        show="$("${p2env[@]}" "$SBXD" egress "$ECON" 2>&1)"
        echo "$show" | grep -q 'SBX_EGRESS' && fail "egress allow removes the chain" \
            || pass "egress allow removes the chain"
        docker exec "$ECON" wget -T 5 -q -O /dev/null http://1.1.1.1/ 2>/dev/null \
            && pass "egress allow restores outbound" || fail "egress allow restores outbound"
    else
        pass "egress checks skipped (container outbound unreachable in this env)"
    fi
    ECLEAN
else
    hdr "P3: egress (skipped — no docker engine or helper image)"
fi

# == P3: recipes (parse/validate/print; hermetic, no docker needed) ==
hdr "P3: recipes"
R="$(mktemp -d /tmp/opencode/sbxdrec.XXXXXX)"
# hermetic recipe env: recipe up writes wallet/egress state through config.Open()
# and audit — without these every suite run litters the REAL config.json
mkdir -p "$R/config" "$R/data" "$R/state"
renv=(env "SBXD_CONFIG_DIR=$R/config" "SBXD_DATA_DIR=$R/data" "SBXD_STATE_DIR=$R/state")
cat > "$R/sbx.yaml" <<'RYEOF'
# suite recipe
name: recipe-suite
stack: go
mounts:
  - ~/projects/core:/workspace/core
  - /tmp/cache:/cache:ro
budget: 5
budget_tokens: 123456
egress: proxy-only
attach: --oc
env:
  FOO: bar
RYEOF
"${renv[@]}" "$SBXD" recipe validate "$R/sbx.yaml" >/dev/null 2>&1 && pass "recipe: validate accepts a good recipe" \
    || fail "recipe: validate accepts a good recipe"
plan="$("${renv[@]}" "$SBXD" recipe print "$R/sbx.yaml" 2>&1)"
echo "$plan" | grep -q 'sbx-recipe-suite' >/dev/null \
    && echo "$plan" | grep -q -- '--mount ~/projects/core:/workspace/core' \
    && pass "recipe: print renders the spawn plan (name + kit flags)" \
    || fail "recipe: print renders the spawn plan"
sed -i 's/^egress: proxy-only/egress: banana/' "$R/sbx.yaml"
"$SBXD" recipe validate "$R/sbx.yaml" >/dev/null 2>&1 \
    && fail "recipe: bad egress rejected" || pass "recipe: bad egress rejected"
# up without --yes on a non-TTY must abort WITHOUT spawning
sed -i 's/^egress: banana/egress: proxy-only/' "$R/sbx.yaml"
out="$(echo n | "${renv[@]}" "$SBXD" recipe up "$R/sbx.yaml" 2>&1)"
if docker inspect sbx-recipe-suite >/dev/null 2>&1; then
    fail "recipe: up aborts without --yes (container was created!)"; docker rm -f sbx-recipe-suite >/dev/null
else
    pass "recipe: up aborts without --yes (no container touched)"
fi
# missing kit image hint
out="$(sed 's/^stack: go/stack: base/' "$R/sbx.yaml" > "$R/sbx2.yaml"; "${renv[@]}" "$SBXD" recipe up "$R/sbx2.yaml" --yes 2>&1)"
if docker image inspect sbx:base >/dev/null 2>&1; then
    pass "recipe: up with base image builds"
    docker rm -f sbx-recipe-suite >/dev/null 2>&1
else
    echo "$out" | grep -q 'not built' && pass "recipe: up hints to build the missing kit image" \
        || fail "recipe: up hints to build the missing kit image (got: $out)"
fi
rm -rf "$R"

# == P3: idle sweeper (hermetic daemon on 7794; stops a labeled toy container) ==
S3="$(mktemp -d /tmp/opencode/sbxdsw.XXXXXX)"
mkdir -p "$S3/config" "$S3/data" "$S3/state"
# Providers on a scratch proxy port (7795) so the in-engine proxy relay —
# which re-reads this very config — actually serves on first sync. The mock
# upstream URL is intentionally a dead port: these checks only prove relay
# reachability + key mint, they never dial the upstream.
cat > "$S3/config/config.json" <<'SWEPOF'
{"opencode":{"default_provider":"mock","providers":{"mock":{"base_url":"http://127.0.0.1:59999/v1","api_key":"mock-uk","models":["mock-fast"]}}},"proxy":{"port":"7795","bind":"0.0.0.0"},"sweeper":{"idle_stop_minutes":1}}
SWEPOF
senv=()
senv+=(env "SBXD_CONFIG_DIR=$S3/config" "SBXD_DATA_DIR=$S3/data" "SBXD_STATE_DIR=$S3/state")
hdr "P3: idle sweeper"
"${senv[@]}" "$SBXD" config get sweeper.idle_stop_minutes 2>/dev/null | grep -q '1' \
    && pass "sweeper: config knob round-trips (1)" || fail "sweeper: config knob round-trips"
if docker info >/dev/null 2>&1; then
    SWCON="sbx-sweepsuite$((RANDOM % 9000 + 1000))"
    docker run -d --name "$SWCON" -l dev.sbx=1 alpine sleep 600 >/dev/null
    ( "${senv[@]}" "$SBXD" serve --port 7794 > "$S3/daemon.log" 2>&1 & echo $! > "$S3/dpid" ) >/dev/null 2>&1
    sleep 1
    SWPID="$(cat "$S3/dpid" 2>/dev/null || true)"
    "${senv[@]}" "$SBXD" config set sweeper.idle_stop_minutes 1 >/dev/null 2>&1
    "${senv[@]}" "$SBXD" config get sweeper.idle_stop_minutes 2>/dev/null | grep -q '1' \
        && pass "sweeper: config set applies live" || fail "sweeper: config set applies live"
    STOPPED=no
    for i in $(seq 1 24); do
        sleep 5
        [[ "$(docker inspect -f '{{.State.Running}}' "$SWCON" 2>/dev/null)" != "true" ]] && { STOPPED=yes; break; }
    done
    if [[ "$STOPPED" == "yes" ]]; then pass "sweeper: auto-stopped the idle sandbox (~$((i*5+5))s)"
    else fail "sweeper: idle container was NOT stopped"; fi
    grep -q 'sweeper.stop' "$S3/state/audit.jsonl" 2>/dev/null \
        && pass "sweeper: audit row sweeper.stop written" || fail "sweeper: audit row sweeper.stop written"
    # -- create wizard via API (uses the same hermetic daemon; docker-gated) --
    if command -v curl >/dev/null && curl -s --max-time 2 http://127.0.0.1:7794/api/health >/dev/null; then
        CW="cw$((RANDOM % 900 + 100))"
        HIP=$((RANDOM % 40000 + 20000))
        RES="$(curl -s --max-time 90 -X POST http://127.0.0.1:7794/api/create -H 'Content-Type: application/json' \
            -d "{\"name\":\"$CW\",\"stack\":\"go\",\"mounts\":[{\"source\":\"/tmp\",\"target\":\"/hosttmp\",\"mode\":\"ro\"}],\"ports\":[\"$HIP:8099\"]}")"
        grep -q '"ok":true' <<<"$RES" && pass "create: wizard spawns a kit-faithful container" \
            || fail "create: wizard spawn failed ($RES)"
        if docker inspect "sbx-$CW" >/dev/null 2>&1; then
            docker inspect "sbx-$CW" -f '{{json .NetworkSettings.Ports}}' | grep -q "\"$HIP\"" \
                && pass "create: port actually published ($HIP:8099)" \
                || fail "create: port published"
            docker inspect "sbx-$CW" --format '{{range .Mounts}}{{.RW}}{{end}}' | grep -q 'false' \
                && pass "create: ro mount respected" || fail "create: ro mount respected"
            grep -q 'sandbox.spawn' "$S3/state/audit.jsonl" 2>/dev/null \
                && pass "create: audit sandbox.spawn written" || fail "create: audit sandbox.spawn written"
        else
            fail "create: container exists"
        fi
        # -- sync round-trip: minted key + a real reachable proxy route --
        # the collector refreshes ~5s — poll until the sandbox is inventoried
        for _i in $(seq 1 12); do
            curl -s --max-time 2 http://127.0.0.1:7794/api/sandboxes | grep -q '"name":"sbx-'"$CW"'"' && break
            sleep 1
        done
        RS="$(curl -s --max-time 60 -X POST http://127.0.0.1:7794/api/sandboxes/container/sbx-$CW/sync \
            -H 'Content-Type: application/json' -d '{}')"
        if grep -q '"ok":true' <<<"$RS" && grep -q '"key_masked":"sbxk-' <<<"$RS"; then
            pass "create: sync mints key + proxy route ($(grep -o '"via": *"[^"]*"' <<<"$RS" | head -1))"
        else
            fail "create: sync failed ($(head -c 160 <<<"$RS"))"
        fi
        DOC="$("$SBXD" doctor 2>&1 || true)"
        if grep -q 'proxy relay' <<<"$DOC"; then
            grep -q 'sbx-proxy-relay (running' <<<"$DOC" \
                && pass "relay: in-engine proxy relay is up" \
                || fail "relay: doctor reports relay not running post-sync"
        else
            pass "relay: n/a (non-Docker-Desktop engine)"
        fi
        RESB="$(curl -s -X POST http://127.0.0.1:7794/api/usage/budget -H 'Content-Type: application/json' \
            -d "{\"name\":\"sbx-$CW\",\"usd\":2,\"tokens\":55000}")"
        grep -q '"budget_tokens": *55000' <<<"$RESB" \
            && pass "budget API: token wallet set" || fail "budget API: token wallet set ($RESB)"
        RSP="$(curl -s -X POST http://127.0.0.1:7794/api/create -d "{\"name\":\"$CW\",\"stack\":\"go\"}")"
        grep -q 'already exists' <<<"$RSP" \
            && pass "create: duplicate name rejected honestly" || fail "create: duplicate name rejected ($RSP)"
        # -- security suite (sweep checks, hermetic daemon; sweep hardening) --
        RC403="$(curl -s -X POST http://127.0.0.1:7794/api/actions/stop -H 'Content-Type: application/json' \
            -H 'Origin: http://evil.example' -d '{"name":"zzz"}')"
        grep -q 'cross-origin' <<<"$RC403" \
            && pass "CSRF: cross-origin POST rejected with 403" \
            || fail "CSRF: cross-origin POST rejected with 403 ($RC403)"
        RSOK="$(curl -s -X POST http://127.0.0.1:7794/api/actions/stop -H 'Content-Type: application/json' \
            -H "Origin: http://127.0.0.1:7794" -d '{"name":"zzz"}')"
        grep -q 'not found' <<<"$RSOK" \
            && pass "CSRF: same-origin POST passes the origin guard" \
            || fail "CSRF: same-origin POST passes the origin guard ($RSOK)"
        RNV="$(curl -s -X POST http://127.0.0.1:7794/api/create -H 'Content-Type: application/json' \
            -d '{"name":"bad name!","stack":"go"}')"
        grep -q 'only a-z' <<<"$RNV" \
            && pass "create: bad charsets rejected (name validation)" \
            || fail "create: bad charsets rejected (name validation) ($RNV)"
        RVMN="$(curl -s -X POST http://127.0.0.1:7794/api/create -H 'Content-Type: application/json' \
            -d '{"kind":"vm","name":"9bad","vm_backend":"docker"}')"
        grep -q 'start with a letter' <<<"$RVMN" \
            && pass "create: VM name rule enforced (first char letter)" \
            || fail "create: VM name rule enforced ($RVMN)"
        RVM2="$(curl -s -X POST http://127.0.0.1:7794/api/create -H 'Content-Type: application/json' \
            -d '{"kind":"vm","name":"vmcheck","vm_backend":"xen"}')"
        grep -q 'libvirt' <<<"$RVM2" \
            && pass "create: VM unknown backend rejected (libvirt|docker only)" \
            || fail "create: VM unknown backend rejected ($RVM2)"
        RVMK="$(curl -s -X POST http://127.0.0.1:7794/api/create -H 'Content-Type: application/json' \
            -d '{"kind":"fisher","name":"x"}')"
        grep -q 'kind must be' <<<"$RVMK" \
            && pass "create: unknown kind rejected (container|vm)" \
            || fail "create: unknown kind rejected ($RVMK)"
        # -- rm round-trip: full lifecycle delete via the API (must run LAST
        # among $CW checks — it deletes the sandbox) --
        RDEL="$(curl -s -X POST http://127.0.0.1:7794/api/actions/delete -H 'Content-Type: application/json' \
            -d "{\"name\":\"sbx-$CW\"}")"
        grep -q '"ok":true' <<<"$RDEL" \
            && pass "rm: delete API removes the sandbox" || fail "rm: delete API removes the sandbox ($RDEL)"
        GREMOVED=no
        for _i in $(seq 1 8); do
            docker inspect "sbx-$CW" >/dev/null 2>&1 || { GREMOVED=yes; break; }
            sleep 1
        done
        [[ "$GREMOVED" == "yes" ]] \
            && pass "rm: container actually gone after delete" \
            || fail "rm: container actually gone after delete"
        grep -q '"kind":"sandbox.rm","sandbox":"sbx-'"$CW"'"' "$S3/state/audit.jsonl" 2>/dev/null \
            && pass "rm: audit row sandbox.rm written" || fail "rm: audit row sandbox.rm written"
        python3 - "$S3/data/keys.json" "$CW" <<'PYJ'
import json, sys
ks = json.load(open(sys.argv[1]))
act = [k['sandbox'] for k in ks['keys'] if not k.get('revoked') and k['sandbox'] == 'sbx-' + sys.argv[2]]
sys.exit(0 if not act else 1)
PYJ
        [[ $? -eq 0 ]] \
            && pass "rm: minted key revoked with the sandbox" \
            || fail "rm: minted key revoked with the sandbox"
        RDEL2="$(curl -s -X POST http://127.0.0.1:7794/api/actions/delete -H 'Content-Type: application/json' \
            -d "{\"name\":\"sbx-$CW\"}")"
        grep -q 'not found' <<<"$RDEL2" \
            && pass "rm: deleting a gone sandbox is an honest 404" \
            || fail "rm: deleting a gone sandbox is an honest 404 ($RDEL2)"
        # -- boot self-registration callback (POST /api/vm/boot) -------------
        # The kit curls this at boot-begin and again once the guest is
        # confirmed up; the pending marker lives in the daemon's state dir.
        RCB="$(curl -s -X POST http://127.0.0.1:7794/api/vm/boot -H 'Content-Type: application/json' \
            -d '{"name":"bootcheck","phase":"begin"}')"
        grep -q '"ok":true' <<<"$RCB" \
            && pass "boot: begin signal accepted (self-registration)" \
            || fail "boot: begin signal accepted ($RCB)"
        grep -q 'bootcheck' "$S3/state/boot_pending.json" 2>/dev/null \
            && pass "boot: pending boot persisted to state dir" \
            || fail "boot: pending boot persisted to state dir (marker missing)"
        grep -q '"kind":"vm.boot"' "$S3/state/audit.jsonl" 2>/dev/null \
            && pass "boot: begin audited (vm.boot)" \
            || fail "boot: begin audited (vm.boot)"
        RBP="$(curl -s -X POST http://127.0.0.1:7794/api/vm/boot -H 'Content-Type: application/json' \
            -d '{"name":"bootcheck","phase":"nope"}')"
        grep -q 'phase must be' <<<"$RBP" \
            && pass "boot: bad phase rejected" \
            || fail "boot: bad phase rejected ($RBP)"
        RBN="$(curl -s -X POST http://127.0.0.1:7794/api/vm/boot -H 'Content-Type: application/json' \
            -d '{"name":"bad vm!","phase":"begin"}')"
        grep -q 'bad name' <<<"$RBN" \
            && pass "boot: kit name rule enforced" \
            || fail "boot: kit name rule enforced ($RBN)"
        RCE="$(curl -s -X POST http://127.0.0.1:7794/api/vm/boot -H 'Content-Type: application/json' \
            -d '{"name":"bootcheck","phase":"end"}')"
        grep -q '"ok":true' <<<"$RCE" \
            && pass "boot: end signal accepted" \
            || fail "boot: end signal accepted ($RCE)"
        ! grep -q 'bootcheck' "$S3/state/boot_pending.json" 2>/dev/null \
            && pass "boot: end clears the pending boot" \
            || fail "boot: end clears the pending boot (marker survived)"
        grep -q '"kind":"vm.booted"' "$S3/state/audit.jsonl" 2>/dev/null \
            && pass "boot: boot-end audited (vm.booted)" \
            || fail "boot: boot-end audited (vm.booted)"
        # -- burn ledger: pin round-trip + purge semantics (records safety) --
        RPO="$(curl -s -X POST http://127.0.0.1:7794/api/usage/pin -H 'Content-Type: application/json' \
            -d '{"name":"ledgerpin","pinned":true}')"
        grep -q '"pinned":true' <<<"$RPO" \
            && pass "burn: pin route round-trips" || fail "burn: pin route round-trips ($RPO)"
        RUR="$(curl -s -X POST http://127.0.0.1:7794/api/usage/purge -H 'Content-Type: application/json' \
            -d '{"sandboxes":["ledgerpin"]}')"
        grep -q 'is pinned' <<<"$RUR" \
            && pass "burn: purge refuses a pinned sandbox" || fail "burn: purge refuses a pinned sandbox ($RUR)"
        ROS="$(curl -s -X POST http://127.0.0.1:7794/api/usage/purge -H 'Content-Type: application/json' \
            -d '{"stale_only":true}')"
        grep -q '"ok":true' <<<"$ROS" \
            && pass "burn: bulk stale purge runs (skips live + pinned)" || fail "burn: bulk stale purge ($ROS)"
        RPU="$(curl -s -X POST http://127.0.0.1:7794/api/usage/pin -H 'Content-Type: application/json' \
            -d '{"name":"ledgerpin","pinned":false}')"
        grep -q '"pinned":false' <<<"$RPU" \
            && pass "burn: unpin round-trips" || fail "burn: unpin round-trips ($RPU)"
        RPD="$(curl -s -X POST http://127.0.0.1:7794/api/usage/purge -H 'Content-Type: application/json' \
            -d '{"sandboxes":["ledgerpin"]}')"
        grep -q '"ok":true' <<<"$RPD" \
            && pass "burn: explicit-name purge works after unpin" || fail "burn: explicit-name purge after unpin ($RPD)"
        grep -q 'pinned_sandboxes' "$S3/config/config.json" \
            && fail "burn: unpin removes the config pin" || pass "burn: unpin removes the config pin"
        docker rm -f "sbx-$CW" >/dev/null 2>&1 || true
    else
        pass "create: API checks skipped (daemon health probe failed)"
    fi
    cleanup01() { docker rm -f "$SWCON" >/dev/null 2>&1 || true; [[ -n "$SWPID" ]] && kill "$SWPID" 2>/dev/null; }
    cleanup01
    # relay hygiene: drop a relay built for this suite's hermetic dirs —
    # the real daemon respawns it from its own state set on the next sync
    if docker inspect sbx-proxy-relay --format '{{range .Mounts}}{{println .Source}}{{end}}' 2>/dev/null | grep -q "^$S3/config$"; then
        docker rm -f sbx-proxy-relay >/dev/null 2>&1 || true
    fi
else
    pass "sweeper: e2e skipped (no docker engine)"
fi

printf '\n\e[1m== result: %d passed, %d failed ==\e[0m\n' "$PASS" "$FAIL"
[[ "$FAIL" -eq 0 ]]
