# Hermes installs per user into ~/.hermes (checkout + pinned uv/Python),
# so the official installer runs as the sandbox user; setup wizard, browser
# and computer-use extras are skipped (sync configures the endpoint).
curl -fsSL https://hermes-agent.nousresearch.com/install.sh -o /tmp/hermes-install.sh
log "running the Hermes installer as $(stat -c %U "$HOTBED_HOME") (~2 min)"
as_user bash /tmp/hermes-install.sh --skip-setup --skip-browser --skip-computer-use >/tmp/hermes-install.log 2>&1 \
    || { tail -20 /tmp/hermes-install.log; exit 1; }
rm -f /tmp/hermes-install.sh
ln -sf "$HOTBED_HOME/.hermes/hermes-agent/.hermes/bin/hermes" /usr/local/bin/hermes
as_user hermes --version
