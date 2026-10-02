# The image phase Runs execute in, on lux.
#
# It holds the toolchain an agent needs and nothing else: no credentials
# baked in (lux supplies the LLM key per Run, as DUDE_LLM_KEY), no docker CLI.
#
# One constraint shapes it: lux puts the agent's home on a state volume, so
# the agent's session survives a move between hosts. That volume hides
# whatever the image had under /home/agent, so nothing the agent needs to
# *run* may live there — OpenCode and git's identity are installed system-wide.
#
#   scripts/runtime-image.sh dude-runtime:dev
#
# That script builds the dude CLI for the target architecture into bin/ first.

FROM debian:bookworm-slim
# Set by BuildKit (docker build, buildx) and by podman build.
ARG TARGETARCH

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

# A browser for the tester (the qa_browser role): Playwright, and the
# Chromium it drives with its system dependencies, installed system-wide
# (PLAYWRIGHT_BROWSERS_PATH), since the agent's home is a volume that would
# hide it. Recording video needs nothing else: Playwright writes webm.
ENV PLAYWRIGHT_BROWSERS_PATH=/opt/playwright
RUN python3 -m venv /opt/playwright-venv \
    && /opt/playwright-venv/bin/pip install --no-cache-dir playwright==1.49.0 \
    && /opt/playwright-venv/bin/playwright install --with-deps chromium \
    && ln -s /opt/playwright-venv/bin/python /usr/local/bin/playwright-python \
    && chmod -R a+rX /opt/playwright \
    && rm -rf /var/lib/apt/lists/*
# `python3 -m playwright` and `import playwright` work for the agent: the
# venv's site-packages are on the system Python's path.
RUN echo /opt/playwright-venv/lib/python3.11/site-packages > /usr/lib/python3/dist-packages/playwright-venv.pth

# The agent. Installed as root into /usr/local, so the agent cannot rewrite
# its own binary and the home volume does not hide it.
ARG OPENCODE_VERSION=latest
RUN curl -fsSL https://opencode.ai/install | HOME=/tmp/install bash \
    && install -m 0755 /tmp/install/.opencode/bin/opencode /usr/local/bin/opencode \
    && rm -rf /tmp/install

# The agent's providers: not secret, so they are the image's. They read the
# URL and key dude gives every Run (DUDE_LLM_URL, DUDE_LLM_KEY); a Run's
# model and effort come on top in OPENCODE_CONFIG_CONTENT, which declares
# the model under llm-anthropic (claude-*) or llm-openai (any other name), so
# a model the proxy serves needs no rebuild. The models listed in the file
# only give OpenCode their context and output limits and reasoning flag. Not in
# /etc/opencode/: OpenCode merges that directory above OPENCODE_CONFIG_CONTENT.
# See docs/operations.md, "Agent image contract".
COPY --chmod=0644 opencode.json /usr/local/share/dude/opencode.json
ENV OPENCODE_CONFIG=/usr/local/share/dude/opencode.json
# dude's CLI: the work the agent is part of, and dude's tools, from the shell.
# Built beside the image (bin/linux-<arch>/dude, static), system-wide like
# the agent.
COPY bin/linux-${TARGETARCH}/dude /usr/local/bin/dude

# Git identity for commits the agent makes, system-wide for the same reason.
RUN git config --system user.name "dude" \
    && git config --system user.email "dude@localhost" \
    && git config --system init.defaultBranch main

RUN useradd --create-home --shell /bin/bash --uid 1000 agent
USER agent
WORKDIR /home/agent
