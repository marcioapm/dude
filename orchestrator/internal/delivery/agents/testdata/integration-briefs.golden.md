## fix

Base integration: this branch conflicts with `main` in api. Call sync_base, then `git merge lux/main`. While sync_base answers status asked, lux has not fetched the base yet and lux/main may be missing or old: call it again in a moment. Merge once sync_base answers fetched or up-to-date, or `git merge-base --is-ancestor 0123456789abcdef lux/main` succeeds; if neither has happened after a few minutes, finish without merging. Resolve every conflict keeping both sides' intent, run the affected tests, and commit with git's default merge message. Never rebase or force. The recorded base head is 0123456789abcdef.

## review

Review integration at fedcba9876543210: run `git show --remerge-diff fedcba9876543210` in addition to reviewing the branch changes and overlapping automatically merged code. A clean textual merge does not prove semantic correctness.

## merge-when

Merge once sync_base answers fetched or up-to-date, or `git merge-base --is-ancestor abc123 lux/release/2` succeeds; if neither has happened after a few minutes, finish without merging

