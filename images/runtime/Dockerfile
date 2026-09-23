# The image phase Runs execute in, on lux.
#
# It holds the toolchain an agent needs and nothing else: no credentials
# baked in (lux supplies them per Run, on a tmpfs), no docker CLI.
#
# One constraint shapes it: lux puts the agent's home on a state volume, so
# the agent's session survives a move between hosts. That volume hides
# whatever the image had under /home/agent, so nothing the agent needs to
# *run* may live there — OpenCode and git's identity are installed system-wide.
#
#   docker build -t dude-runtime:dev images/runtime

FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    # Agents must never be prompted for credentials; fail fast instead.
    GIT_TERMINAL_PROMPT=0

RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates \
      curl \
      git \
      jq \
      openssh-client \
      python3 \
      python3-pytest \
      python3-venv \
      ripgrep \
      unzip \
      xz-utils \
    && rm -rf /var/lib/apt/lists/*

# The agent. Installed as root into /usr/local, so the agent cannot rewrite
# its own binary and the home volume does not hide it.
ARG OPENCODE_VERSION=latest
RUN curl -fsSL https://opencode.ai/install | HOME=/tmp/install bash \
    && install -m 0755 /tmp/install/.opencode/bin/opencode /usr/local/bin/opencode \
    && rm -rf /tmp/install

# Git identity for commits the agent makes, system-wide for the same reason.
RUN git config --system user.name "dude" \
    && git config --system user.email "dude@localhost" \
    && git config --system init.defaultBranch main

RUN useradd --create-home --shell /bin/bash --uid 1000 agent
USER agent
WORKDIR /home/agent
