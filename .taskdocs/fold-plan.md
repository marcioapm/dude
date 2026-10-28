# Fold plan (d5e88e32c)

1. Deadlock: CAS tx in prs.go locks base_sync_requests in created_at,base_sha order before updates. Barrier test via Syncer hook (red first).
2. API status: queued only if PR in latest live delivery pullRequestIds (same as baseSyncInScope). Test.
3. Wording: alreadyQueued returns queued=true; explicit mapping comment; awaitPullRequest pending/paused writer -> queued. Assert escalation branch status.
4. Two-live-writer test for ORDER BY created_at DESC.
5. Delete PendingBaseSyncPrompt; move assertion to ClaimBaseSyncPromptTx submitted prompt.
6. Move st.PersonSaid="" from awaitRead helper to openPullRequest call site. Test waiver+empty wake+feedback; assert empty person_note in TestAWaitNoteEndsAtAwaitRead.
7. api pickUp test: resume leaves personSaid empty.

Gates: env setup (podman pg17, bun, uv, playwright), red/green + mutations, full scripts/go-test.sh head vs base worktree, vet/gofmt/typecheck/bun tests (control-plane vs base), e2e, diff --check, test-id diff.
Report: /workspace/out/fold-report.md. git rm .taskdocs in final commit.
