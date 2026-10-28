# OpenClaw via npm - it requires Node 24+. npm 11.16+ gates package install
# scripts: allow OpenClaw's own, fall back to a plain install on older npm.
ensure_node 24
log "npm install -g openclaw"
npm install -g --prefix /usr/local --no-fund --no-audit --loglevel=error \
        openclaw@latest --allow-scripts=openclaw 2>/dev/null \
    || npm_global openclaw@latest
openclaw --version
