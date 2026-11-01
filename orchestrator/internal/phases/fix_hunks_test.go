package phases

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// fixPublish is a fix Run of w publishing a's checkout to the task branch
// through a fake GitHub over a's origin, as the phase sweep's finish does.
type fixPublish struct {
	w  *resumeWorld
	a  *accountingRun
	gh *forge.GitHub
}

func newFixPublish(t *testing.T) *fixPublish {
	w := newResumeWorld(t)
	a := newAccountingRun(t)
	w.exec(`UPDATE runs SET phase = 'fix' WHERE id = $1`, w.run.ID)
	w.run.Phase = delivery.PhaseFix
	w.exec(`INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		SELECT 'repo_' || organization_id, organization_id, project_id, 'target', 'https://github.com/acme/target.git', 'main' FROM runs WHERE id = $1`, w.run.ID)
	w.exec(`INSERT INTO task_repositories (organization_id, task_id, repository_id, access)
		SELECT organization_id, task_id, 'repo_' || organization_id, 'write' FROM runs WHERE id = $1`, w.run.ID)
	w.run.Attempt = 1
	w.run.BaseSHAs = map[string]string{"target": a.start}
	return &fixPublish{w: w, a: a}
}

// publish pushes the checkout's head and publishes it; the fake GitHub is
// made on the first, with a's intercept and answer as they are then.
func (f *fixPublish) publish() error {
	f.a.push()
	if f.gh == nil {
		f.gh = f.a.github()
		f.w.s.Forges = forgeFor(func(context.Context, string) (*forge.GitHub, error) { return f.gh, nil })
	}
	f.w.run.PushResult, _ = json.Marshal(map[string]any{"results": []map[string]string{
		{"repo": "target", "branch": "dude/run", "commit": f.a.head(), "status": "pushed"}}})
	_, _, err := f.w.s.publish(f.w.ctx, f.w.run)
	return err
}

// hunks is the Run's runs.fix_hunks; nil when NULL.
func (f *fixPublish) hunks() delivery.FixHunks {
	f.w.t.Helper()
	var raw []byte
	if err := f.w.owner.QueryRow(f.w.ctx, `SELECT fix_hunks FROM runs WHERE id = $1`, f.w.run.ID).Scan(&raw); err != nil {
		f.w.t.Fatal(err)
	}
	if raw == nil {
		return nil
	}
	var h delivery.FixHunks
	if err := json.Unmarshal(raw, &h); err != nil {
		f.w.t.Fatal(err)
	}
	return h
}

func fileOf(h delivery.FixHunks, repo, file string) *delivery.HunkFile {
	for i, f := range h[repo].Files {
		if f.File == file {
			return &h[repo].Files[i]
		}
	}
	return nil
}

// A fix Run that publishes keeps its change per repository: each file's
// new-side ranges and its lines added and removed.
func TestAFixsPublishKeepsItsHunks(t *testing.T) {
	f := newFixPublish(t)
	f.a.commit(map[string]string{"FACTORY.md": "one\ntwo\nthree\n"}, "fix")
	// GitHub's patch, its body and all (the fake shows only the header).
	f.a.answer = func(base, head string, answer map[string]any) {
		for _, file := range answer["files"].([]any) {
			if file := file.(map[string]any); file["filename"] == "FACTORY.md" {
				file["patch"] = "@@ -0,0 +1,3 @@\n+one\n+two\n+three"
			}
		}
	}
	if err := f.publish(); err != nil {
		t.Fatal(err)
	}
	got := fileOf(f.hunks(), "target", "FACTORY.md")
	if got == nil || got.Add != 3 || got.Del != 0 || got.Unknown || len(got.New) != 1 || got.New[0] != [2]int{1, 3} {
		t.Fatalf("FACTORY.md's kept change: %+v (all: %+v)", got, f.hunks())
	}
}

// A fix that publishes twice keeps its whole change, from where its
// checkout started to its last head: both commits' files.
func TestAFixThatPublishesTwiceKeepsItsWholeChange(t *testing.T) {
	f := newFixPublish(t)
	f.a.commit(map[string]string{"first.go": "package a\n"}, "first")
	if err := f.publish(); err != nil {
		t.Fatal(err)
	}
	f.a.commit(map[string]string{"second.go": "package a\n\nfunc B() {}\n"}, "second")
	if err := f.publish(); err != nil {
		t.Fatal(err)
	}
	h := f.hunks()
	if fileOf(h, "target", "first.go") == nil || fileOf(h, "target", "second.go") == nil {
		t.Fatalf("kept after two publishes: %+v, want first.go and second.go", h)
	}
}

// Only a fix keeps its hunks: an implementer's publish does not.
func TestOnlyAFixKeepsItsHunks(t *testing.T) {
	f := newFixPublish(t)
	f.w.exec(`UPDATE runs SET phase = 'implement' WHERE id = $1`, f.w.run.ID)
	f.w.run.Phase = delivery.PhaseImplement
	f.a.commit(map[string]string{"FACTORY.md": "one\n"}, "implement")
	if err := f.publish(); err != nil {
		t.Fatal(err)
	}
	if h := f.hunks(); h != nil {
		t.Fatalf("an implementer's kept hunks: %+v", h)
	}
}

// A compare that fails for good while the publish is counted (what the
// base brought in, refused) still publishes; the change is not known, so
// none is kept.
func TestAFailedCompareStillPublishesAndKeepsNoHunks(t *testing.T) {
	f := newFixPublish(t)
	f.a.commit(map[string]string{"FACTORY.md": "one\n"}, "fix")
	f.a.intercept = func(r *http.Request) int {
		if strings.Contains(r.URL.Path, "/compare/main...") {
			return 422
		}
		return 0
	}
	if err := f.publish(); err != nil {
		t.Fatalf("the publish failed: %v", err)
	}
	if n := f.w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'git.commit_created'`); n != 1 {
		t.Fatalf("published %d times, want 1", n)
	}
	if h := f.hunks(); h != nil {
		t.Fatalf("kept hunks of a change not known: %+v", h)
	}
}

// A compare cut short at GitHub's file cap keeps the repository as
// truncated, with no files: every fact read from it is unknown.
func TestATruncatedCompareKeepsTheRepositoryTruncated(t *testing.T) {
	f := newFixPublish(t)
	f.a.commit(map[string]string{"FACTORY.md": "one\n"}, "fix")
	f.a.answer = func(base, head string, answer map[string]any) {
		files := make([]any, forge.CompareFileCap)
		for i := range files {
			files[i] = map[string]any{"filename": "f" + string(rune('a'+i%26)) + ".txt", "status": "modified", "additions": 1, "patch": "@@ -1 +1 @@"}
		}
		answer["files"] = files
	}
	if err := f.publish(); err != nil {
		t.Fatal(err)
	}
	h := f.hunks()
	if r, ok := h["target"]; !ok || !r.Truncated || len(r.Files) != 0 {
		t.Fatalf("kept after a truncated compare: %+v", h)
	}
}
