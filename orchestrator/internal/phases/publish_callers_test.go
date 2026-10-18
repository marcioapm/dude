package phases

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

func TestPhasePublishExcludesImportedBaseFiles(t *testing.T) {
	w := newResumeWorld(t)
	a := newAccountingRun(t)
	a.commit(map[string]string{"own.txt": "own\n"}, "Own work")
	a.advanceBase(map[string]string{"imported.txt": lines(80, "base")})
	a.syncBase()
	a.mergeBase(nil)
	a.push()
	gh := a.github() // fakegithub returns merge_base_commit as a commit object.
	w.s.Forges = forgeFor(func(context.Context, string) (*forge.GitHub, error) { return gh, nil })
	w.exec(`INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		SELECT 'repo_' || organization_id, organization_id, project_id, 'target', 'https://github.com/acme/target.git', 'main' FROM runs WHERE id = $1`, w.run.ID)
	w.exec(`INSERT INTO task_repositories (organization_id, task_id, repository_id, access)
		SELECT organization_id, task_id, 'repo_' || organization_id, 'write' FROM runs WHERE id = $1`, w.run.ID)
	w.run.Attempt = 1
	w.run.BaseSHAs = map[string]string{"target": a.start}
	w.run.PushResult, _ = json.Marshal(map[string]any{"results": []map[string]string{{"repo": "target", "branch": "dude/run", "commit": a.head(), "status": "pushed"}}})
	heads, branch, err := w.s.publish(w.ctx, w.run)
	if err != nil {
		t.Fatal(err)
	}
	if branch != delivery.BranchFor(w.run.TaskID, 1) || heads["target"].SHA != a.head() || !slices.Equal(heads["target"].ChangedPaths, []string{"own.txt"}) {
		t.Fatalf("phase publish: branch %q, heads %+v", branch, heads)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'git.commit_created'
		AND payload->'changedPaths' = '["own.txt"]'::jsonb`); n != 1 {
		t.Fatal("phase publication did not record its own paths")
	}
}
