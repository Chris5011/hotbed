#!/usr/bin/env bash
# static-check.sh — cheap statics that the compiler cannot do for us.
#
# 1. Kit shell scripts stay bash-clean.
# 2. Dashboard appJS: extract the raw-string from web.go, node --check it,
#    then run a free-call scan for undefined helpers. This exists because
#    `node --check` validates syntax only — and the render callbacks swallow
#    JS errors into .catch — so fmtTok and burnShowStale blanked whole views
#    (burn, purge dialogs) while every test passed (fixed 2026-10-26, gate
#    growing to cover the scan). The scan masks comments + string literals
#    (no more prose-word false positives) and only flags CALL positions:
#    NAME( where NAME is neither defined here, a keyword, nor a browser/
#    JS builtin. Add new browser APIs to KNOWN as they get used.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
FAIL=0

for f in vm/hotbedvm.sh tests/hotbedd-smoke.sh tests/vmsmoke.sh tests/vmdsmoke.sh tests/smoke.sh bin/hbd \
         install.sh quick-install.sh scripts/update.sh agents/_lib.sh agents/*/install.sh; do
    [ -f "$f" ] || continue
    if bash -n "$f" 2>/dev/null; then
        echo "bash  $f  ok"
    else
        echo "bash  $f  FAILED"
        FAIL=1
    fi
done

if ! command -v python3 >/dev/null 2>&1; then
    echo "python3 not found - appJS checks skipped (CI must have python3)"
    exit "$FAIL"
fi
TMP="${TMPDIR:-/tmp}/hotbed-appjs-check.js"
if ! python3 - "$TMP" <<'PY'
import sys
src = open('hotbed/internal/api/web.go').read()
marker = 'const appJS = `'
i = src.index(marker)
j = src.index('`' + chr(10), i + len(marker))
open(sys.argv[1], 'w').write(src[i + len(marker):j])
PY
then
    echo "appJS extraction FAILED"
    exit 1
fi
echo "appJS extracted ($(wc -c <"$TMP") bytes)"

if command -v node >/dev/null 2>&1; then
    if node --check "$TMP"; then
        echo "node --check  ok"
    else
        echo "node --check  FAILED"
        FAIL=1
    fi
else
    echo "node not found - node --check skipped (CI must have node)"
fi

if python3 - "$TMP" <<'PY'
import re, sys
js = open(sys.argv[1]).read()
KNOWN = set('''
arguments bigint body break case catch class const continue debugger default
delete document do else escape event fetch finally for function globalThis
if in instanceof let location Math localStorage navigator new of Object
Array String Number Boolean Date JSON Math Promise RegExp Error
parseFloat parseInt return switch this throw try typeof undefined var void
while window history alert confirm EventSource FormData Blob File crypto
clearInterval clearTimeout setInterval setTimeout queueMicrotask console
encodeURIComponent decodeURIComponent isFinite isNaN NaN Infinity Symbol
Map Set WeakMap WeakSet Reflect Proxy ArrayBuffer DataView TextEncoder
TextDecoder URL URLSearchParams structuredClone matchMedia requestIdleCallback
getComputedStyle location CustomEvent MediaQueryList self top parent frames
'''.split())
defs = set(re.findall(r'\bfunction\s+([A-Za-z_$][\w$]*)', js))
defs |= set(re.findall(r'\b(?:var|let|const)\s+([A-Za-z_$][\w$]*)\s*=', js))
defs |= set(re.findall(r'\b(?:var|let|const)\s+([A-Za-z_$][\w$]*)', js))
callnames = re.findall(r'(?<![\w$.])([A-Za-z_$][\w$]*)\s*\(', js)

def strip(js):
    # single authoritative walk: strings, template ${}, block/line comments.
    # comments MUST be handled in code-mode only - a regex-based pre-pass eats
    # '//'-containing string content (qemu:///system killed a help string's
    # closing quote and desynced the whole parse).
    out, i, mode = [], 0, None
    n = len(js)
    while i < n:
        c = js[i]
        if mode:
            if c == '\\' and i + 1 < n:
                i += 2
                continue
            if c == mode and (mode != '`' or js[i:i+2] != '${'):
                mode = None
                out.append(' ')
                i += 1
                continue
            if mode == '`' and js[i:i+2] == '${':
                depth, j2 = 1, i + 2
                while j2 < n and depth > 0:
                    if js[j2] == '{': depth += 1
                    elif js[j2] == '}': depth -= 1
                    j2 += 1
                out.append(js[i+2:j2-1])
                i = j2
                continue
            i += 1
            continue
        if c in ("'", '"', '`'):
            mode = c
            out.append(' ')
            i += 1
            continue
        if c == '/' and js[i+1:i+2] == '/':
            nl = js.find('\n', i)
            i = nl if nl != -1 else n
            continue
        if c == '/' and js[i+1:i+2] == '*':
            end = js.find('*/', i + 2)
            i = end + 2 if end != -1 else n
            continue
        out.append(c)
        i += 1
    return ''.join(out)

clean = strip(js)
defs = set(re.findall(r'\bfunction\s+([A-Za-z_$][\w$]*)', clean))
defs |= set(re.findall(r'\b(?:var|let|const)\s+([A-Za-z_$][\w$]*)', clean))
# \\ excluded from the lookbehind so regex-literal content (\B(?=...)) can
# never end a phantom "call" with its opening (.
calls = re.findall(r'(?<![\w$.\\])([A-Za-z_$][\w$]*)\s*\(', clean)
ghosts = sorted(n for n in set(calls) if n not in defs and n not in KNOWN)
if ghosts:
    print('UNDEFINED FREE-CALL CANDIDATES:', ', '.join(ghosts))
    print('(every identifier called bare must be defined, keyword, or browser builtin')
    print(' - add browser APIs to KNOWN in scripts/static-check.sh, nothing else)')
    sys.exit(1)
print('free-call scan  ok (%s distinct bare calls verified)' % len(set(calls)))
PY
then
    :
else
    echo "free-call scan  FAILED"
    FAIL=1
fi

if [ "$FAIL" -ne 0 ]; then
    echo "== static-check: FAILED =="
    exit 1
fi
echo "== static-check: all green =="
