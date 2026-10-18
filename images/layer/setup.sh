#!/bin/sh
# The dude layer's setup, run as root as the last step of every image-library
# build (dude-image-builder's finish step), after the layer's /rootfs/ was
# copied onto /:
#
#   COPY --from=<layer> /rootfs/ /
#   RUN /bin/sh /usr/local/share/dude/setup.sh && …
#
# POSIX sh and coreutils only: the base image is anybody's. Idempotent: a
# second run changes nothing. It
# - fails unless the image has glibc's loader (OpenCode is a glibc binary)
#   and git (agents commit with it);
# - adds the user `agent` (uid 1000, gid 1000, home /home/agent, shell
#   /bin/sh) unless a user of that name exists; a different user already on
#   uid 1000 (node in node images, ubuntu in ubuntu:24.04) keeps it, and
#   agent becomes a second name for the same uid;
# - creates /home/agent owned by 1000:1000;
# - sets git's system identity (dude <dude@localhost>), init.defaultBranch
#   main and core.hooksPath (dude's hook directory) in /etc/gitconfig, each
#   key only when the system config lacks it. Every Run includes
#   /usr/local/share/dude/git-config, which sets core.hooksPath in command
#   scope; the system setting is the fallback for a git started without the
#   Run's environment, as in images/runtime/Dockerfile.
set -eu

# Every path below is under ROOT: empty in a build; a directory standing in
# for / in the layer's tests (images.TestLayerSetup*), which run this script
# without a container.
ROOT=${DUDE_SETUP_ROOT:-}
AGENT=agent
AGENT_ID=1000
AGENT_HOME=/home/agent
AGENT_SHELL=/bin/sh
HOOKS=/usr/local/share/dude/git-hooks

fail() {
	echo "dude layer: $*" >&2
	exit 1
}

case "$(uname -m)" in
	aarch64 | arm64) loader=/lib/ld-linux-aarch64.so.1 ;;
	x86_64 | amd64) loader=/lib64/ld-linux-x86-64.so.2 ;;
	*) loader= ;;
esac
if [ -z "$loader" ] || [ ! -e "$ROOT$loader" ]; then
	fail "the dude layer needs /bin/sh and glibc in the image"
fi

if ! command -v git >/dev/null 2>&1 || ! git --version >/dev/null 2>&1; then
	fail "the image needs git: agents commit with it"
fi

# has_entry FILE NAME: exit 0 if FILE has an entry whose first field is NAME.
has_entry() {
	[ -f "$1" ] || return 1
	while IFS=: read -r name _rest || [ -n "$name" ]; do
		[ "$name" = "$2" ] && return 0
	done <"$1"
	return 1
}

# A file that does not end in a newline would glue the appended entry onto
# its last line. $(...) strips a trailing newline, so it is empty only then.
append_line() {
	if [ -s "$1" ] && [ -n "$(tail -c 1 "$1")" ]; then
		printf '\n' >>"$1"
	fi
	printf '%s\n' "$2" >>"$1"
}

if ! has_entry "$ROOT/etc/group" "$AGENT"; then
	if command -v groupadd >/dev/null 2>&1; then
		groupadd --non-unique --gid "$AGENT_ID" "$AGENT"
	else
		append_line "$ROOT/etc/group" "$AGENT:x:$AGENT_ID:"
	fi
fi

if ! has_entry "$ROOT/etc/passwd" "$AGENT"; then
	if command -v useradd >/dev/null 2>&1; then
		useradd --non-unique --uid "$AGENT_ID" --gid "$AGENT_ID" --no-create-home \
			--home-dir "$AGENT_HOME" --shell "$AGENT_SHELL" "$AGENT"
	else
		append_line "$ROOT/etc/passwd" "$AGENT:x:$AGENT_ID:$AGENT_ID::$AGENT_HOME:$AGENT_SHELL"
		if [ ! -e "$ROOT/etc/shadow" ]; then
			: >"$ROOT/etc/shadow"
			chmod 0600 "$ROOT/etc/shadow"
		fi
		# Locked password (!), and no last-change date: 0 there would mean
		# "must change now", which makes PAM (su agent) refuse the account.
		has_entry "$ROOT/etc/shadow" "$AGENT" || append_line "$ROOT/etc/shadow" "$AGENT:!::0:99999:7:::"
	fi
fi

mkdir -p "$ROOT$AGENT_HOME"
chown "$AGENT_ID:$AGENT_ID" "$ROOT$AGENT_HOME"
chmod 0755 "$ROOT$AGENT_HOME"

# git's system config, read without git: section names and keys are case
# insensitive; "[user]" and '[init]' headers, "key = value" lines.
gitconfig=$ROOT/etc/gitconfig
has_name=no
has_email=no
has_branch=no
has_hooks=no
if [ -f "$gitconfig" ]; then
	section=
	while IFS= read -r line || [ -n "$line" ]; do
		line=$(printf '%s' "$line" | tr -d ' \t' | tr '[:upper:]' '[:lower:]')
		case "$line" in
			\[*\]*) section=${line%%]*}; section=${section#[} ;;
			name=?*) [ "$section" = user ] && has_name=yes ;;
			email=?*) [ "$section" = user ] && has_email=yes ;;
			defaultbranch=?*) [ "$section" = init ] && has_branch=yes ;;
			hookspath=?*) [ "$section" = core ] && has_hooks=yes ;;
		esac
	done <"$gitconfig"
fi
if [ "$has_name" = no ] || [ "$has_email" = no ]; then
	append_line "$gitconfig" "[user]"
	[ "$has_name" = yes ] || append_line "$gitconfig" "	name = dude"
	[ "$has_email" = yes ] || append_line "$gitconfig" "	email = dude@localhost"
fi
if [ "$has_branch" = no ]; then
	append_line "$gitconfig" "[init]"
	append_line "$gitconfig" "	defaultBranch = main"
fi
if [ "$has_hooks" = no ]; then
	append_line "$gitconfig" "[core]"
	append_line "$gitconfig" "	hooksPath = $HOOKS"
fi
chmod 0644 "$gitconfig"
