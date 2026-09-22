// Publishing a Run's work: push the branch so a pull request can be opened.
//
// The push happens on the host, not inside the Run container, for two
// reasons. The container is network-isolated for untrusted repositories, so
// it could not reach the forge even if we wanted it to; and the credential
// must never enter a filesystem the agent can read. The workspace is a
// host directory the container has mounted, so the host can see the same
// commits the agent just made.
//
// The credential is fetched per push and discarded, so a node whose lease
// expired cannot write to the repository (plan §61).

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/marciomartins/dude/runner/internal/client"
	"github.com/marciomartins/dude/runner/internal/protocol"
	"github.com/marciomartins/dude/runner/internal/workspace"
)

// How long a push may take before it is abandoned. Generous because a cold
// remote with a large first push is slow, but bounded so a hung network does
// not hold a lease forever.
const pushTimeout = 5 * time.Minute

/*
publish pushes each repository whose HEAD moved, and reports the result.

Returns the events to append. A repository the agent did not touch is not
pushed: an empty branch on the forge is noise, and a pull request against it
would have nothing to review.
*/
func (d *daemon) publish(
	ctx context.Context,
	r client.Run,
	repos []workspace.MaterializedRepo,
	changed map[string]string,
	log *slog.Logger,
) []client.Event {
	if len(changed) == 0 {
		return nil
	}

	credential, err := d.api.PushCredential(ctx, r.ID)
	if err != nil {
		// Not fatal to the Run: the agent's work is committed locally and the
		// workspace survives, so a push can be retried. Saying so plainly
		// beats failing a Run that actually succeeded.
		log.Warn("no push credential; leaving commits unpublished", "error", err)
		return nil
	}

	var events []client.Event
	for _, repo := range repos {
		head, ok := changed[repo.Name]
		if !ok {
			continue
		}

		pushCtx, cancel := context.WithTimeout(ctx, pushTimeout)
		err := pushBranch(pushCtx, repo.Path, repo.URL, credential, credential.Branch)
		cancel()

		if err != nil {
			log.Warn("push failed", "repo", repo.Name, "branch", credential.Branch, "error", err)
			events = append(events, pushEvent(r, repo.Name, credential.Branch, head, err))
			continue
		}

		log.Info("pushed", "repo", repo.Name, "branch", credential.Branch, "head", head)
		events = append(events, pushEvent(r, repo.Name, credential.Branch, head, nil))
	}
	return events
}

/*
pushBranch pushes HEAD to `branch` on the repository's real remote.

The credential goes in the URL of a one-off push rather than into the
repository's config or a credential helper, so nothing on disk holds it after
this returns — the workspace outlives the Run, and a token left in
`.git/config` would outlive it too.

`--force-with-lease` rather than `--force`: a retried Run should be able to
replace its own earlier attempt, but must not silently discard a commit
someone else pushed to the same branch.
*/
func pushBranch(ctx context.Context, repoPath, remoteURL string, cred *client.PushCredential, branch string) error {
	authenticated, err := withCredential(remoteURL, cred)
	if err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, "git", "push", "--force-with-lease",
		authenticated, "HEAD:refs/heads/"+branch)
	cmd.Dir = repoPath
	// Never prompt: a push that needs input would hang until the lease died.
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0")

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git push: %w: %s", err, redact(string(out), cred.Token))
	}
	return nil
}

// withCredential rewrites an https remote to carry the token.
//
// An ssh remote is returned unchanged: it authenticates with a key the node
// already has, and there is nothing to inject.
func withCredential(remoteURL string, cred *client.PushCredential) (string, error) {
	if !strings.HasPrefix(remoteURL, "https://") {
		return remoteURL, nil
	}
	rest := strings.TrimPrefix(remoteURL, "https://")
	return fmt.Sprintf("https://%s:%s@%s", cred.Username, cred.Token, rest), nil
}

// redact keeps a token out of logs and out of the event ledger.
//
// Git echoes the remote URL in several of its error messages, and that URL
// carries the credential we just injected.
func redact(text, token string) string {
	if token == "" {
		return text
	}
	return strings.ReplaceAll(text, token, "***")
}

func pushEvent(r client.Run, repo, branch, head string, err error) client.Event {
	payload := map[string]any{
		"repo":    repo,
		"branch":  branch,
		"headSha": head,
		"ok":      err == nil,
	}
	if err != nil {
		payload["error"] = err.Error()
	}
	return client.Event{
		EventType:  protocol.EventGitPushCompleted,
		RunID:      r.ID,
		ProjectID:  r.ProjectID,
		WorkItemID: r.WorkItemID,
		ActorType:  "system",
		ActorID:    "runner",
		Payload:    payload,
	}
}
