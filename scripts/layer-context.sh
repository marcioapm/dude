#!/usr/bin/env bash
# layer-context.sh DEST writes the dude layer's build context, without the
# dude CLI, into DEST (made if missing): images/layer/'s Dockerfile and
# setup.sh, and the files it shares with images/runtime/ (opencode.json,
# git-config, git-hooks/dude-hook, link-git-hooks.sh), read from there so
# the two images carry one copy of each. dist.sh ships it as
# share/dude/layer/; dude-image-builder adds the release's bin/dude as `dude`
# and builds it (images.BuildLayer).
set -euo pipefail

DEST="${1:?usage: layer-context.sh <dest>}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

mkdir -p "$DEST/git-hooks"
install -m 0644 "$ROOT/images/layer/Dockerfile" "$DEST/Dockerfile"
install -m 0755 "$ROOT/images/layer/setup.sh" "$DEST/setup.sh"
install -m 0644 "$ROOT/images/runtime/opencode.json" "$DEST/opencode.json"
install -m 0644 "$ROOT/images/runtime/git-config" "$DEST/git-config"
install -m 0755 "$ROOT/images/runtime/git-hooks/dude-hook" "$DEST/git-hooks/dude-hook"
install -m 0755 "$ROOT/images/runtime/link-git-hooks.sh" "$DEST/link-git-hooks.sh"
