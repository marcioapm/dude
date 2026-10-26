#!/bin/sh
# install-cli.sh installs the dude CLI from a GitHub release of marcioapm/dude:
#
#   curl -fsSL https://raw.githubusercontent.com/marcioapm/dude/main/scripts/install-cli.sh | sh
#
# DUDE_VERSION       a release tag, vX.Y.Z (default: the latest)
# DUDE_INSTALL_DIR   where bin/dude goes (default: ~/.local/bin)
# DUDE_RELEASES_URL  for tests only: a fake GitHub releases API
#
# macOS and Linux, arm64 and amd64. The tarball's SHA-256 is checked against
# the release's SHA256SUMS before anything is installed.
set -eu

api="${DUDE_RELEASES_URL:-https://api.github.com/repos/marcioapm/dude/releases}"
api="${api%/}"
dir="${DUDE_INSTALL_DIR:-$HOME/.local/bin}"

fail() {
  echo "install-cli.sh: $*" >&2
  exit 1
}

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) fail "dude's CLI is built for macOS and Linux, not $(uname -s); see https://github.com/marcioapm/dude/releases" ;;
esac
case "$(uname -m)" in
  arm64 | aarch64) arch=arm64 ;;
  x86_64 | amd64) arch=amd64 ;;
  *) fail "dude's CLI is built for arm64 and amd64, not $(uname -m)" ;;
esac

if command -v sha256sum >/dev/null 2>&1; then
  sha256() { sha256sum "$1" | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then
  sha256() { shasum -a 256 "$1" | awk '{print $1}'; }
else
  fail "needs sha256sum or shasum to check the download"
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
trap 'exit 1' INT TERM

if [ -n "${DUDE_VERSION:-}" ]; then
  case "$DUDE_VERSION" in v*) tag="$DUDE_VERSION" ;; *) tag="v$DUDE_VERSION" ;; esac
  release_url="$api/tags/$tag"
else
  release_url="$api/latest"
fi
curl -fsSL -o "$tmp/release.json" "$release_url" || fail "no release at $release_url"
tag="$(sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$tmp/release.json" | head -n 1)"
[ -n "$tag" ] || fail "$release_url is not a release"

asset="dude-cli_${tag}_${os}_${arch}.tar.gz"
# asset_url is NAME's browser_download_url in the release's JSON.
asset_url() {
  tr ',' '\n' <"$tmp/release.json" |
    sed -n 's/.*"browser_download_url"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' |
    grep "/$1\$" | head -n 1 || true
}
tarball_url="$(asset_url "$asset")"
sums_url="$(asset_url SHA256SUMS)"
[ -n "$tarball_url" ] || fail "release $tag has no $asset"
[ -n "$sums_url" ] || fail "release $tag has no SHA256SUMS"

echo "downloading dude $tag for $os/$arch" >&2
curl -fsSL -o "$tmp/$asset" "$tarball_url" || fail "downloading $asset failed"
curl -fsSL -o "$tmp/SHA256SUMS" "$sums_url" || fail "downloading SHA256SUMS failed"

want="$(awk -v name="$asset" '$2 == name || $2 == "*" name {print $1; exit}' "$tmp/SHA256SUMS")"
[ -n "$want" ] || fail "SHA256SUMS has no line for $asset; refusing an unverified binary"
got="$(sha256 "$tmp/$asset")"
[ "$got" = "$want" ] || fail "$asset's SHA-256 is $got, but SHA256SUMS says $want; nothing was installed"

tar -xzf "$tmp/$asset" -C "$tmp" bin/dude || fail "$asset has no bin/dude"

mkdir -p "$dir" || fail "cannot create $dir; set DUDE_INSTALL_DIR"
# A copy in the same directory, then a rename: dude is never half written.
staged="$dir/.dude.install.$$"
cp "$tmp/bin/dude" "$staged" || fail "cannot write to $dir; set DUDE_INSTALL_DIR, or re-run with permission to write there"
chmod 0755 "$staged"
mv -f "$staged" "$dir/dude" || { rm -f "$staged"; fail "cannot replace $dir/dude"; }

echo "installed dude $tag to $dir/dude" >&2
case ":$PATH:" in
  *":$dir:"*) ;;
  *)
    echo "$dir is not on your PATH; add it, for example in ~/.profile or ~/.zshrc:" >&2
    echo "  export PATH=\"$dir:\$PATH\"" >&2
    ;;
esac
