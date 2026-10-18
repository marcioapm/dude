# The dude layer: the files dude-image-builder copies onto every
# image-library image as its last build step, so any image works as an
# agent image without a copy of dude's setup (docs/design/images.md, "The
# builder"). dude-image-builder builds it from the installed release's
# share/dude/layer/, which scripts/layer-context.sh assembles from this
# directory and images/runtime/ (one copy of the hook, git-config and
# opencode.json for both images), plus the release's bin/dude, copied in as
# `dude`. Its tag is a hash of those files, not the dude version: a release
# with identical inputs makes no new layer (images.LayerTag).
#
# The image is FROM scratch and holds only /rootfs/, exactly what goes onto /:
#   /rootfs/usr/local/bin/dude                     the release's CLI
#   /rootfs/usr/local/bin/opencode                 OpenCode's native glibc binary
#   /rootfs/usr/local/bin/rg                       static ripgrep (OpenCode searches with it)
#   /rootfs/usr/local/share/dude/opencode.json     OpenCode's providers and models
#   /rootfs/usr/local/share/dude/setup.sh          the agent user and git identity
#   /rootfs/usr/local/share/dude/git-config        core.hooksPath, included by every Run
#   /rootfs/usr/local/share/dude/git-hooks/        dude's commit hook under each client hook's name
#
# Upgrading OpenCode or ripgrep is a change to the ARGs below, checksums
# included: both come from the release's own SHA256 (GitHub's asset digest).

# The fetch stage runs on the build host's own platform and downloads the
# binaries for the target's: no emulation, whatever builds it.
FROM --platform=$BUILDPLATFORM docker.io/library/debian:bookworm-slim AS fetch

ARG TARGETARCH
# https://github.com/anomalyco/opencode/releases (opencode-linux-<arch>.tar.gz)
ARG OPENCODE_VERSION=1.18.32
ARG OPENCODE_SHA256_ARM64=568461b7d4d8c19865c97e9a1102e613049c6039d01fe772154de873c1865840
ARG OPENCODE_SHA256_X64=3046e0404fdc60fb80307e7a47824ba07477364178a4d09baa8548496dd6d43b
# https://github.com/BurntSushi/ripgrep/releases (<triple>-musl: statically linked)
ARG RIPGREP_VERSION=15.2.0
ARG RIPGREP_SHA256_ARM64=800b1e7206afe799dfb5a6901f23147cfaabe0e52210538100f61e86e1740915
ARG RIPGREP_SHA256_X64=33e15bcf1624b25cdd2a55813a47a2f95dbe126268203e76aa6a585d1e7b149c

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*

RUN set -eu; \
    case "$TARGETARCH" in \
      arm64) oc=arm64; oc_sum="$OPENCODE_SHA256_ARM64"; rg=aarch64; rg_sum="$RIPGREP_SHA256_ARM64" ;; \
      amd64) oc=x64; oc_sum="$OPENCODE_SHA256_X64"; rg=x86_64; rg_sum="$RIPGREP_SHA256_X64" ;; \
      *) echo "unsupported architecture: $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    install -d -m 0755 /rootfs/usr/local/bin /rootfs/usr/local/share/dude/git-hooks /tmp/oc /tmp/rg; \
    curl -fsSLo /tmp/oc.tar.gz \
      "https://github.com/anomalyco/opencode/releases/download/v${OPENCODE_VERSION}/opencode-linux-${oc}.tar.gz"; \
    echo "$oc_sum  /tmp/oc.tar.gz" | sha256sum -c -; \
    tar -xzf /tmp/oc.tar.gz -C /tmp/oc --no-same-owner opencode; \
    install -m 0755 /tmp/oc/opencode /rootfs/usr/local/bin/opencode; \
    rgdir="ripgrep-${RIPGREP_VERSION}-${rg}-unknown-linux-musl"; \
    curl -fsSLo /tmp/rg.tar.gz \
      "https://github.com/BurntSushi/ripgrep/releases/download/${RIPGREP_VERSION}/${rgdir}.tar.gz"; \
    echo "$rg_sum  /tmp/rg.tar.gz" | sha256sum -c -; \
    tar -xzf /tmp/rg.tar.gz -C /tmp/rg --no-same-owner "${rgdir}/rg"; \
    install -m 0755 "/tmp/rg/${rgdir}/rg" /rootfs/usr/local/bin/rg; \
    rm -rf /tmp/oc /tmp/rg /tmp/oc.tar.gz /tmp/rg.tar.gz

COPY --chmod=0755 dude /rootfs/usr/local/bin/dude
COPY --chmod=0644 opencode.json /rootfs/usr/local/share/dude/opencode.json
COPY --chmod=0755 setup.sh /rootfs/usr/local/share/dude/setup.sh
COPY --chmod=0644 git-config /rootfs/usr/local/share/dude/git-config
COPY --chmod=0755 git-hooks/dude-hook /rootfs/usr/local/share/dude/git-hooks/dude-hook
RUN --mount=type=bind,source=link-git-hooks.sh,target=/tmp/link-git-hooks.sh \
    sh /tmp/link-git-hooks.sh /rootfs/usr/local/share/dude/git-hooks

FROM scratch
COPY --from=fetch /rootfs/ /rootfs/
