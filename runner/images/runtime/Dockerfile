# Work runtime image.
#
# This is the container a Run executes in. It holds the toolchain an agent
# needs, and nothing that would let it escape: no docker CLI, no host mounts
# beyond the Session Workspace, no credentials baked in.
#
# Credentials arrive at runtime, scoped to one Session, and artifacts leave
# through the runner rather than through object-store credentials handed to
# the agent (plan §66).

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
      python3-venv \
      ripgrep \
      unzip \
      xz-utils \
    && rm -rf /var/lib/apt/lists/*

# Run as a non-root user. Combined with cap-drop and no-new-privileges on the
# container, this keeps a compromised agent from escalating inside its own
# sandbox.
RUN useradd --create-home --shell /bin/bash --uid 1000 agent

# The Session Workspace is bind-mounted here by the runner.
WORKDIR /workspace
RUN chown agent:agent /workspace

USER agent
ENV HOME=/home/agent

# The agent harness. Installed as the unprivileged user so a compromised
# session cannot rewrite the binary for the next one.
ARG OPENCODE_VERSION=latest
RUN curl -fsSL https://opencode.ai/install | bash
ENV PATH="/home/agent/.opencode/bin:${PATH}"

# Git identity for commits the agent makes. Overridden per-Run when the
# factory knows the requesting human.
RUN git config --global user.name "dude" \
    && git config --global user.email "dude@localhost" \
    && git config --global init.defaultBranch main \
    # The workspace is owned by the host user, not by `agent`; without this
    # git refuses to operate on it.
    && git config --global --add safe.directory '*'

CMD ["sleep", "infinity"]
