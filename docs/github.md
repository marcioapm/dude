# GitHub credentials and permissions

Dude uses an organization's GitHub credential both for Git clone/push (passed to lux) and for the forge API: pull requests, checks, reviewer requests, branch updates, merges and webhooks. Configure it in the organization's GitHub settings.

Use a **fine-grained personal access token (PAT)** limited to the repositories dude should work on. Select their resource owner, then **Only select repositories**. The token owner must also have the necessary repository access. Token permissions do not grant rights the owner lacks; organization approval, SSO policies and branch rules still apply.

## Fine-grained PAT

### Normal code delivery

Repository permissions:

- **Contents: Read and write** — clone, push commits, create/update/delete task branches, update a PR branch and merge when delivery policy permits it.
- **Pull requests: Read and write** — open/read PRs, read feedback and review threads, and request reviewers.
- **Metadata: Read** — repository information and collaborator permission checks; GitHub includes this automatically.
- **Checks: Read** — read check runs, their conclusions and annotations.
- **Commit statuses: Read** — read combined commit status, including CI that reports statuses rather than check runs.

### Enable only for the operations you want

- **Workflows: Read and write** — required to push changes under `.github/workflows/`. This is separate from Contents and Actions. A task that adds `ci.yml` needs it even if ordinary code pushes succeed.
- **Actions: Read and write** — required if dude's CI recovery policy reruns failed GitHub Actions jobs. Read access alone does not permit reruns. Workflow files can be edited without granting Actions write; conversely Actions write does not authorize editing workflow files.
- **Webhooks: Read and write** — required for dude to install/update its repository webhook. The token owner's repository role must also permit managing hooks. If hooks are managed separately, do not grant this just for clone/push.
- Organization **Members: Read** — required to check private organization membership when the configured feedback policy accepts organization members rather than only repository writers. It is not needed just to push code or open a PR.

For full delivery including CI workflow edits, CI reruns and automatic webhook setup, use the normal-code permissions plus Workflows, Actions and Webhooks write. Members read is conditional on the feedback policy. Do not grant organization Administration, repository deletion, secrets, packages or other unrelated access.

## Classic PAT

Prefer a fine-grained PAT because classic scopes are broader and not restricted to selected repositories. If a classic token is necessary:

- **`repo`** — code and PR operations on private repositories, including repository hooks. For a strictly public-only setup, **`public_repo`** is the narrower alternative.
- **`workflow`** — additionally required to create/update workflow files under `.github/workflows/`.
- **`read:org`** — additionally required for private organization membership checks when that feedback policy is enabled.

Do not confuse the classic `workflow` scope with the fine-grained Workflows permission. GitHub's Git rejection may say “without `workflow` scope” even when the remedy for a fine-grained token is **Workflows: Read and write**.

## GitHub Apps

The current orchestrator supports PAT authentication. Its schema/UI may mention GitHub Apps, but `GitHub.Token()` currently rejects `github_app` authentication as not implemented. Do not use an App private key or installation id as a working substitute for a PAT until installation-token support is implemented.

## Troubleshooting

### “Write access to repository not granted”

Check the token's resource owner, selected repository, Contents write permission, organization approval/SSO requirements and the token owner's repository access.

A REST repository response with `permissions.push: true` describes the **user's** repository role, not proof that this token can push. Successfully cloning proves read access only. Authenticated Git `git-receive-pack` discovery can establish ordinary push access without publishing a branch, but it cannot prove permission to change workflow files.

### “Refusing to allow a Personal Access Token to create or update workflow … without `workflow` scope”

The agent made a workflow-file change, but the token cannot publish it. Add **Workflows: Read and write** to a fine-grained PAT, or **`workflow`** to a classic PAT. If replacing/regenerating the token, replace the stored credential in dude as well. Permission changes may require organization approval.

This is not a model or connectivity failure. Do not change the workflow or broaden unrelated permissions to get around it. A failed phase's workspace/snapshot may retain the commit: inspect the run before asking an agent to rebuild the same work. Whether retry/resume reuses it depends on the supported recovery path; do not assume an existing Run receives a replaced credential automatically.

### API “Resource not accessible by personal access token”

Check the failed operation against the corresponding permission above. GitHub REST responses expose `X-Accepted-GitHub-Permissions`; multiple acceptable permission combinations may be listed. Classic tokens instead expose scope headers such as `X-OAuth-Scopes`.

A token verification check that can read `/user` does not establish code, workflow, PR, webhook or CI-rerun access. Permissions are per operation, not a single “GitHub connected” boolean.

### A pull request shows “CI pending” or “CI unavailable” while GitHub shows results

**CI unavailable**, with the warning *Cannot read GitHub check runs (access denied)*, means GitHub answered 403 to the check-runs listing for the pull request's head. Check runs are how GitHub Actions and most CI apps report, so the checks dude shows are only what it could read, commonly commit statuses such as CodeRabbit's. dude keeps the pull request out of ready-to-merge and refuses to merge it, but wakes no fixer and re-runs nothing for this.

Grant the token **Checks: Read** (classic PAT: `repo`), and check that the repository is selected, the owner still has access, and organization approval/SSO is complete. The next sync (a webhook, or the reconciler within 15 minutes) clears the warning and records it on the task. A rate limit is not reported this way: it fails the sync, which is retried.

**CI pending** with no warning means dude has no verdict on the head commit yet: checks queued or running, CI yet to register on a new push, or a run cancelled or waiting on approval. It does not claim that anything is running. Pending past the organization's patience asks a person.

Actions read access is not a substitute: a workflow run's result says nothing about other apps' check runs, so dude does not fall back to it.

## References

- [GitHub: permissions required for fine-grained PATs](https://docs.github.com/en/rest/authentication/permissions-required-for-fine-grained-personal-access-tokens)
- [GitHub: managing personal access tokens](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens)
- [GitHub: classic/OAuth scopes](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/scopes-for-oauth-apps)
