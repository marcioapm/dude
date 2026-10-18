#!/bin/sh
# link-git-hooks.sh DIR links DIR/dude-hook under every client hook's name,
# for the runtime image and the dude layer alike. Not linked, so a
# repository's own of these do not run: reference-transaction and
# post-index-change, which git calls on every ref and index write.
set -eu
cd "$1"
for h in applypatch-msg pre-applypatch post-applypatch pre-commit pre-merge-commit prepare-commit-msg \
	commit-msg post-commit pre-rebase post-checkout post-merge pre-push post-rewrite pre-auto-gc \
	sendemail-validate; do
	ln -s dude-hook "$h"
done
