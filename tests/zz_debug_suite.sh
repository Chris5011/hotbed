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
if command -v python3 >/dev/null; then pass "python3 present (mock upstream)"; else
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
    [[ -n "$P2MOCK_PID" ]] && kill "$P2MOCK_PID" 2>/dev/null
    [[ -n "$P2DAEMON_PID" ]] && kill "$P2DAEMON_PID" 2>/dev/null
    rm -rf "$P2" 2>/dev/null || true
}
p2env=(env "SBXD_CONFIG_DIR=$P2/config" "SBXD_DATA_DIR=$P2/data" "SBXD_STATE_DIR=$P2/state")
MOCK_PORT=$MPORT python3 "$P2/mock.py" >"$P2/mock.log" 2>&1 & P2MOCK_PID=$!
"${p2env[@]}" "$SBXD" serve --port "$DBUS" >"$P2/daemon.log" 2>&1 & P2DAEMON_PID=$!
for _ in $(seq 1 20); do
    curl -s --max-time 1 "http://127.0.0.1:$DPROTO/v1" >/dev/null 2>&1 && break
    sleep 0.25
done
ss -ltnn | grep -q ":8787 " || echo MOCK_NOT_LISTENING_8787 >&2; ps -p "$P2MOCK_PID" >/dev/null || echo "MOCK_PID_GONE($P2MOCK_PID)" >&2
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
echo "DEBUG c2=[$c2]" >&2
"$c2" | grep -q .hi there. && pass "proxy: non-stream passthrough" || fail "proxy: non-stream passthrough"
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
# 8. revoke kills access
"${p2env[@]}" "$SBXD" key revoke tester >/dev/null 2>&1
c5=$(curl -s -o /dev/null -w '%{http_code}' --max-time 8 -XPOST "http://127.0.0.1:$DPROTO/v1/chat/completions" \
        -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' -d '{"model":"mock-fast","stream":false}')
[[ "$c5" == 401 ]] && pass "key revoke cuts access instantly" || fail "key revoke cuts access instantly (got $c5)"
trap cleanup EXIT
trap 'cleanup; P2CLEAN' EXIT

printf '\n\e[1m== result: %d passed, %d failed ==\e[0m\n' "$PASS" "$FAIL"
[[ "$FAIL" -eq 0 ]]
