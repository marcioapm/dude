# GitHub credentials and permissions

Dude uses an organization's GitHub credential both for Git clone/push (passed to lux) and for the forge API: pull requests, checks, reviewer requests, branch updates, merges and webhooks. Configure it in the organization's GitHub settings: a personal access token, or a [GitHub App](#github-app).

Use a **fine-grained personal access token (PAT)** limited to the repositories dude should work on, **unless dude must read CI check runs** (GitHub Actions and most CI apps report that way): GitHub offers fine-grained tokens no permission that reads check runs, so only a classic token with `repo` (or a GitHub App) can. With a fine-grained token, dude sees only commit statuses and every pull request shows "CI unavailable". For a fine-grained token, select its resource owner, then **Only select repositories**. The token owner must also have the necessary repository access. Token permissions do not grant rights the owner lacks; organization approval, SSO policies and branch rules still apply.

**Verify** (Organization settings → GitHub) tests each of these on every repository of the organization's projects without changing anything on GitHub, and says which is missing, or why one could not be tested.

## Fine-grained PAT

### Normal code delivery

Repository permissions:

- **Contents: Read and write** — clone, push commits, create/update/delete task branches, update a PR branch and merge when delivery policy permits it.
- **Pull requests: Read and write** — open/read PRs, read feedback and review threads, and request reviewers.
- **Metadata: Read** — repository information and collaborator permission checks; GitHub includes this automatically.
- **Commit statuses: Read** — read combined commit status, including CI that reports statuses rather than check runs.

Check runs cannot be granted: there is no Checks permission for fine-grained tokens. GitHub answers 403 ("Resource not accessible by personal access token", `X-Accepted-GitHub-Permissions: checks=read`) to the listing on any commit that has check runs, whatever else the token is granted. Use a classic PAT (below) when dude must see CI that reports check runs.

### Enable only for the operations you want

- **Workflows: Read and write** — required to push changes under `.github/workflows/`. This is separate from Contents and Actions. A task that adds `ci.yml` needs it even if ordinary code pushes succeed.
- **Actions: Read and write** — required if dude's CI recovery policy reruns failed GitHub Actions jobs. Read access alone does not permit reruns. Workflow files can be edited without granting Actions write; conversely Actions write does not authorize editing workflow files.
- **Webhooks: Read and write** — required for dude to install/update its repository webhook. The token owner's repository role must also permit managing hooks. If hooks are managed separately, do not grant this just for clone/push.
- Organization **Members: Read** — required to check private organization membership when the configured feedback policy accepts organization members rather than only repository writers. It is not needed just to push code or open a PR.

For full delivery including CI workflow edits, CI reruns and automatic webhook setup, use the normal-code permissions plus Workflows, Actions and Webhooks write. Members read is conditional on the feedback policy. Do not grant organization Administration, repository deletion, secrets, packages or other unrelated access.

## Classic PAT

Classic scopes are broader and not restricted to selected repositories, but a classic token is the only token kind that reads check runs today. If a classic token is used:

- **`repo`** — code and PR operations on private repositories, including repository hooks and reading check runs. For a strictly public-only setup, **`public_repo`** is the narrower alternative.
- **`workflow`** — additionally required to create/update workflow files under `.github/workflows/`.
- **`read:org`** — additionally required for private organization membership checks when that feedback policy is enabled.

Do not confuse the classic `workflow` scope with the fine-grained Workflows permission. GitHub's Git rejection may say “without `workflow` scope” even when the remedy for a fine-grained token is **Workflows: Read and write**.

## GitHub App

An organization can connect dude as a GitHub App instead of with a token (Organization settings → GitHub → Connect GitHub → **GitHub App**). Pull requests are then opened by the App (`<slug>[bot]`), not by whoever owns a token, and the agents' commits are authored and committed by the App's bot, `<slug>[bot] <<id>+<slug>[bot]@users.noreply.github.com>`. A token keeps working as before; the choice is per organization.

### Creating the App

On GitHub: Settings → Developer settings → GitHub Apps → New GitHub App (under the organization's settings for an organization-owned App).

Repository permissions:

- **Contents: Read and write** — clone, push, task branches, fast-forwards.
- **Pull requests: Read and write** — open and read pull requests, reviews and comments, request reviewers.
- **Workflows: Read and write** — push changes under `.github/workflows/`.
- **Checks: Read** — read check runs (GitHub Actions and most CI). Unlike a fine-grained token, an App can.
- **Actions: Read** — read workflow runs and jobs.
- **Commit statuses: Read** — read CI that reports statuses.
- **Metadata: Read** — granted automatically.

Subscribe to the events **pull_request**, **pull_request_review**, **pull_request_review_comment**, **issue_comment**, **check_run** and **check_suite**. The App's webhook URL and secret are the ones the Webhooks card shows once the App is connected (**Show secret**); content type `application/json`. An App has one webhook for every repository it is installed on, so dude registers no repository hooks for it.

An App that is **public** ("Any account" can install it) can be installed by link on more than one account; a private one only on the account that owns it. To have dude work on a personal account's repositories and an organization's, make it public and install it on each, giving it access to the repositories dude works on. One credential serves every installation: dude finds the installation of each repository's owner (`GET /repos/{owner}/{repo}/installation`) and keeps it.

### Connecting it

1. Generate a private key in the App's settings; GitHub downloads a `.pem` file.
2. In dude, choose **GitHub App**, enter the **App ID** (on the App's settings page) and paste the whole `.pem` file, `-----BEGIN RSA PRIVATE KEY-----` to `-----END RSA PRIVATE KEY-----`, into **Private key**. Like a token, the key is stored for the organization and never shown again.
3. **Installation ID** is optional: leave it empty and dude finds each owner's installation. When set, it is used for an owner GitHub names no installation for, and for requests about no repository.
4. Set the App's webhook URL and secret from the Webhooks card.
5. **Verify** mints an installation token for one linked repository and reads the App's bot, and says GitHub's reason when it cannot (the App not installed on that owner, a key that is not the App's).

dude signs a short-lived JWT with the key, exchanges it for an installation token (one hour), and mints a fresh one before every clone, push and sync it asks lux for, so a Run that outlives a token keeps pushing. A lux too old to take fresh credentials on a push is pushed with the token the Run was started or resumed with, and dude logs a warning: upgrade lux.

### Co-authors

Every commit an agent makes carries a `Co-authored-by:` trailer for each person on the task when the commit is made, owner first; with a token too, where the commits are authored by `dude`. If people change mid-task, earlier commits keep the people they had. Each person sets, in Settings → You → Profile:

- **GitHub username** — dude stores GitHub's numeric id for it and names them by their noreply address, `<id>+<login>@users.noreply.github.com`, which GitHub links to their account whatever addresses they keep private. A username GitHub does not know is refused.
- **Commit email** — used instead, when set: an address on their GitHub account.

The address used is the commit email, else the noreply address, else the email they sign in with; someone with none is left out, and nobody is named twice. The runtime image's commit hook adds the trailers (`dude git coauthors`), and runs the repository's own hooks after it. Agents are told not to add or remove them.

## Troubleshooting

### “Write access to repository not granted”

Check the token's resource owner, selected repository, Contents write permission, organization approval/SSO requirements and the token owner's repository access.

A REST repository response with `permissions.push: true` describes the **user's** repository role, not proof that this token can push. Successfully cloning proves read access only. Authenticated Git `git-receive-pack` discovery can establish ordinary push access without publishing a branch, but it cannot prove permission to change workflow files.

### “Refusing to allow a Personal Access Token to create or update workflow … without `workflow` scope”

The agent made a workflow-file change, but the token cannot publish it. Add **Workflows: Read and write** to a fine-grained PAT, or **`workflow`** to a classic PAT. If replacing/regenerating the token, replace the stored credential in dude as well. Permission changes may require organization approval.

This is not a model or connectivity failure. Do not change the workflow or broaden unrelated permissions to get around it. A failed phase's workspace/snapshot may retain the commit: inspect the run before asking an agent to rebuild the same work. Whether retry/resume reuses it depends on the supported recovery path; do not assume an existing Run receives a replaced credential automatically.

### API “Resource not accessible by personal access token”

Check the failed operation against the corresponding permission above. GitHub REST responses expose `X-Accepted-GitHub-Permissions`; multiple acceptable permission combinations may be listed. Classic tokens instead expose scope headers such as `X-OAuth-Scopes`.

A token verification check that can read `/user` does not establish code, workflow, PR, webhook or CI-rerun access. Permissions are per operation, not a single “GitHub connected” boolean; dude's Verify probes each one per repository for that reason.

### A pull request shows “CI pending” or “CI unavailable” while GitHub shows results

**CI unavailable**, with the warning *GitHub refused the check-runs read; a fine-grained token cannot read check runs, so use a classic token with the repo scope (or a GitHub App once supported), and check SSO authorization, organization token approval and the token's repository access*, means GitHub answered 403 to the check-runs listing for the pull request's head, without marking it as a rate limit. Check runs are how GitHub Actions and most CI apps report, so the checks dude shows are only what it could read, commonly commit statuses such as CodeRabbit's. dude keeps the pull request out of ready-to-merge and refuses to merge it, but wakes no fixer and re-runs nothing for this.

The 403 does not say why. With a **fine-grained token** it is expected and cannot be fixed by granting anything: GitHub has no Checks permission for fine-grained tokens. Replace it with a classic token with `repo` (or a GitHub App once dude supports one). With a classic token, a missing `repo` scope, SSO authorization not given to the token, organization token approval, or the owner losing access to the repository answer the same way; check each. Verify in Organization settings → GitHub says which applies per repository. The next sync (a webhook, or the reconciler within 15 minutes) clears the warning and records it on the task. A rate limit is not reported this way, including GitHub's "abuse detection" secondary limit: it fails the sync, which is retried.

**CI pending** with no warning means dude has no verdict on the head commit yet: checks queued or running, CI yet to register on a new push, or a run cancelled or waiting on approval. It does not claim that anything is running. Pending past the organization's patience asks a person.

Actions read access is not a substitute: a workflow run's result says nothing about other apps' check runs, so dude does not fall back to it.

## References

- [GitHub: permissions required for fine-grained PATs](https://docs.github.com/en/rest/authentication/permissions-required-for-fine-grained-personal-access-tokens)
- [GitHub: managing personal access tokens](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens)
- [GitHub: classic/OAuth scopes](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/scopes-for-oauth-apps)
