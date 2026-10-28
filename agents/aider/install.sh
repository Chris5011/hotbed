# Aider pins its own Python (3.12) through uv — the stack's system Python
# (3.13 on trixie) stays untouched.
ensure_uv
export UV_TOOL_DIR="$AGENTS_OPT/aider/tools" UV_TOOL_BIN_DIR=/usr/local/bin \
       UV_PYTHON_INSTALL_DIR="$AGENTS_OPT/aider/python"
log "uv tool install aider-chat (python 3.12)"
uv tool install --quiet --python 3.12 aider-chat
# uv's managed python lives under root-owned dirs: readable for the sandbox user
chmod -R a+rX "$AGENTS_OPT/aider"
aider --version
