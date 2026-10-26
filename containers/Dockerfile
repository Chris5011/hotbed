# syntax=docker/dockerfile:1
# OpenCode-SBX sandbox images
#
# One Dockerfile, one shared "common" base layer, and one minimal target per
# stack. sbx builds them as  sbx:<stack>  images:
#
#   docker build --target node   -t sbx:node .
#   docker build --target python -t sbx:python .
#   ...
#
# All stacks share git/network/ssh/build tooling + a fallback opencode binary,
# so any sandbox is always self-sufficient even without the layer-specific
# language runtime.
#
# The sandbox user is created with uid/gid 1000; at container start the
# entrypoint (containers/entrypoint.sh) adjusts ownership for the actual
# host uid, so kept images stay generic and shareable.

ARG DEBIAN_RELEASE=trixie
ARG OPENCODE_FALLBACK_VERSION=1.19.4

###### shared base of every stack #############################################
FROM debian:${DEBIAN_RELEASE}-slim AS common

ARG OPENCODE_FALLBACK_VERSION

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      bash \
      ca-certificates \
      curl \
      wget \
      xz-utils \
      tar \
      zip \
      unzip \
      less \
      git \
      jq \
      ripgrep \
      tmux \
      file \
      openssh-client \
      sudo \
      procps \
      iproute2 \
      build-essential \
      gcc \
      make \
      pkg-config \
      python3 \
      python3-pip \
      python3-venv \
 && git config --system --add safe.directory '*' \
 # agents commonly pip-install tools; trixie blocks system pip by default
 && printf '[global]\nbreak-system-packages = true\n' > /etc/pip.conf \
 # keep our toolchain dirs on PATH even inside login shells (-l) which reset it
 && printf '%s\n' \
      '#!/bin/sh' \
      'case ":$PATH:" in' \
      '  *":/usr/local/go/bin:"*) ;;' \
      '  *) export PATH="/usr/local/go/bin:/usr/local/cargo/bin:$PATH" ;;' \
      'esac' \
      > /etc/profile.d/sbx-path.sh \
      && chmod 0755 /etc/profile.d/sbx-path.sh \
 && rm -rf /var/lib/apt/lists/*

# fallback opencode binary (official release channel); if the host has its own
# opencode binary it gets bind-mounted over /usr/local/bin/opencode at runtime.
# The download retries a few times: a transient TLS sway (curl exit 35) used to
# kill the whole one-time build on fresh boxes.
RUN set -e \
 && for a in 1 2 3 4 5; do \
      curl -fsSL \
        "https://github.com/sst/opencode/releases/download/v${OPENCODE_FALLBACK_VERSION}/opencode-linux-x64.tar.gz" \
        -o /tmp/opencode.tgz && break \
        || { rm -f /tmp/opencode.tgz; echo "opencode download attempt $a failed — retrying"; sleep 3; }; \
    done \
 && [ -s /tmp/opencode.tgz ] \
 && tar -xzf /tmp/opencode.tgz -C /usr/local/bin opencode \
 && chmod 0755 /usr/local/bin/opencode \
 && rm /tmp/opencode.tgz \
 && opencode --version

# sandbox user + shared-cache directory skeletons
RUN groupadd -g 1000 sbx \
 && useradd -m -d /home/sbx -g sbx -u 1000 -s /bin/bash sbx \
 && echo 'sbx ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/sbx \
 && chmod 0440 /etc/sudoers.d/sbx \
 && mkdir -p \
      /home/sbx/.config/opencode \
      /home/sbx/.local/share/opencode \
      /home/sbx/.npm \
      /home/sbx/.cache/pip \
 && chown -R sbx:sbx /home/sbx

COPY containers/entrypoint.sh /usr/local/bin/sbx-entry
RUN chmod 0755 /usr/local/bin/sbx-entry

ENTRYPOINT ["/usr/local/bin/sbx-entry"]

# greet the shell with a clearly-marked sandbox prompt + handy env
RUN cat >> /home/sbx/.bashrc <<'EOF'

# --- sandbox conveniences (added by OpenCode-SBX) ---
export PS1='\[\e[36m\]sbx\[\e[0m\]:\w\$ '
export PATH="$HOME/go/bin:$PATH"
export GOPATH="$HOME/go"
export GOMODCACHE="$HOME/go/pkg/mod"
EOF
RUN chown sbx:sbx /home/sbx/.bashrc

WORKDIR /workspace

###### stack: node #############################################################
FROM common AS node

ARG NODE_MAJOR=22

RUN set -e \
 && curl -fsSL "https://deb.nodesource.com/setup_${NODE_MAJOR}.x" -o /tmp/node-setup.sh \
 && bash /tmp/node-setup.sh \
 && apt-get install -y --no-install-recommends nodejs \
 && rm /tmp/node-setup.sh \
 && rm -rf /var/lib/apt/lists/* \
 && node --version && npm --version

###### stack: python ###########################################################
FROM common AS python

RUN apt-get update \
 && apt-get install -y --no-install-recommends python3-dev pipx \
 && rm -rf /var/lib/apt/lists/* \
 # uv — the fast package manager agents keep asking for
 && curl -fsSL https://astral.sh/uv/install.sh | sh \
 && cp -a /root/.local/bin/uv /usr/local/bin/uv \
 && if [ -f /root/.local/bin/uvx ]; then cp -a /root/.local/bin/uvx /usr/local/bin/uvx; fi \
 && uv --version && python3 --version

###### stack: go ###############################################################
FROM common AS go

RUN set -e \
 # latest stable go from the official dist API, fall back to a pinned version
 && GVER="$(curl -fsSL 'https://go.dev/dl/?mode=json' | jq -r '.[0].version' | sed 's/^go//')" \
 && if [ -z "$GVER" ]; then GVER=1.24.0; fi \
 && echo "installing go $GVER" \
 && curl -fsSL "https://go.dev/dl/go${GVER}.linux-amd64.tar.gz" | tar -xz -C /usr/local \
 && /usr/local/go/bin/go version

ENV PATH="/usr/local/go/bin:${PATH}" \
    GOPATH=/home/sbx/go \
    GOMODCACHE=/home/sbx/go/pkg/mod \
    GOBIN=/home/sbx/go/bin

###### stack: rust #############################################################
FROM common AS rust

ENV RUSTUP_HOME=/opt/rustup \
    CARGO_HOME=/usr/local/cargo

RUN curl -fsSL https://sh.rustup.rs \
      | sh -s -- -y --profile minimal --default-toolchain stable \
 && "$CARGO_HOME/bin/rustc" --version && "$CARGO_HOME/bin/cargo" --version

ENV PATH="/usr/local/cargo/bin:${PATH}"

###### default target (plain `docker build .`) #################################
FROM common AS base
