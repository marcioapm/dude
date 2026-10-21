package phases

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/judge"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// workspaceLux runs exec commands with sh on this machine, /workspace in
// their arguments read as root: the Run's container, its checkouts real git
// repositories. fail, set, refuses every exec as lux would; exit replaces
// the script's own exit code.
type workspaceLux struct {
	lux.Client
	root  string
	mu    sync.Mutex
	execs int
	fail  error
	exit  *int
	stops int
}

func (l *workspaceLux) Exec(ctx context.Context, _ string, command []string) (lux.ExecResult, error) {
	l.mu.Lock()
	l.execs++
	fail, exit := l.fail, l.exit
	l.mu.Unlock()
	if fail != nil {
		return lux.ExecResult{}, fail
	}
	args := make([]string, len(command))
	for i, a := range command {
		args[i] = strings.ReplaceAll(a, workspaceDir, l.root)
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code := 0
	var ee *exec.ExitError
	if err := cmd.Run(); errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		return lux.ExecResult{}, err
	}
	if exit != nil {
		code = *exit
	}
	return lux.ExecResult{Stdout: []byte(stdout.String()), Stderr: []byte(stderr.String()), ExitCode: code}, nil
}

func (l *workspaceLux) Stop(context.Context, string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stops++
	return nil
}

func (l *workspaceLux) execCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.execs
}

// unchangedWorld is a gated Run whose pushable checkouts are real git
// repositories under a temporary /workspace, each at the commit lux reported
// it starting from, its turn ended.
type unchangedWorld struct {
	*harnessWorld
	lux   *workspaceLux
	bases map[string]string
}

func newUnchangedWorld(t *testing.T, repos ...string) *unchangedWorld {
	t.Helper()
	w := &unchangedWorld{harnessWorld: newHarnessWorld(t), lux: &workspaceLux{root: t.TempDir()}, bases: map[string]string{}}
	for _, name := range repos {
		dir := filepath.Join(w.lux.root, "repos", name)
		w.git(dir, "init", "-q", "-b", "main", dir)
		w.write(name, "README.md", "hello\n")
		w.git(dir, "add", "-A")
		w.git(dir, "commit", "-q", "-m", "start")
		w.bases[name] = strings.TrimSpace(w.git(dir, "rev-parse", "HEAD"))
	}
	raw, _ := json.Marshal(w.bases)
	w.exec(`UPDATE runs SET lux_pushes = $2, base_shas = $3::jsonb, push_branch = 'dude/run' WHERE id = $1`, w.run.ID, repos, raw)
	w.run.LuxRunID, w.run.LuxState, w.run.PushBranch, w.run.HoldsPushable, w.run.BaseSHAs = "lrun_1", "running", "dude/run", true, w.bases
	w.s.Lux = w.lux
	w.endTurn()
	return w
}

func (w *unchangedWorld) exec(q string, args ...any) {
	w.t.Helper()
	if _, err := w.owner.Exec(context.Background(), q, args...); err != nil {
		w.t.Fatal(err)
	}
}

func (w *unchangedWorld) git(dir string, args ...string) string {
	w.t.Helper()
	c := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=a", "-c", "user.email=a@x"}, args...)...)
	if args[0] == "init" {
		c = exec.Command("git", args...)
	}
	out, err := c.CombinedOutput()
	if err != nil {
		w.t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

func (w *unchangedWorld) dir(repo string) string { return filepath.Join(w.lux.root, "repos", repo) }

func (w *unchangedWorld) write(repo, path, content string) {
	w.t.Helper()
	full := filepath.Join(w.dir(repo), path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

// endTurn records the agent's turn ending, as the translator does.
func (w *unchangedWorld) endTurn() {
	w.t.Helper()
	if err := w.s.DB.InOrg(context.Background(), w.run.Org, func(tx pgx.Tx) error {
		return w.s.event(context.Background(), tx, w.run, evSessionStopped, ledger.ActorAgent, map[string]any{"reason": "turn_complete"})
	}); err != nil {
		w.t.Fatal(err)
	}
}

// decisions are the gate's recorded judgements of the Run, oldest first.
func (w *unchangedWorld) decisions() []checksDecision {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT payload FROM events WHERE run_id = $1 AND event_type = $2 ORDER BY cursor`,
		w.run.ID, delivery.EvRunChecksJudged)
	if err != nil {
		w.t.Fatal(err)
	}
	raws, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		w.t.Fatal(err)
	}
	out := make([]checksDecision, len(raws))
	for i, raw := range raws {
		if err := json.Unmarshal(raw, &out[i]); err != nil {
			w.t.Fatal(err)
		}
	}
	return out
}

// gate runs the checks gate with a judge that fails every report it is
// asked about: what passes without it passed unjudged.
func (w *unchangedWorld) gate() (pass bool, judged int) {
	w.t.Helper()
	fake := &checksFake{backed: judge.No, missing: judge.Yes}
	w.s.Judge = fake
	pass, _, err := w.s.checksBeforePush(context.Background(), w.run)
	if err != nil {
		w.t.Fatal(err)
	}
	return pass, fake.calls
}

// A turn that left every pushable checkout where it started passes the gate
// with no report and no judge, recorded as no change: verified, not skipped,
// with the heads it read.
func TestAnUnchangedTurnPassesTheGateWithoutAReportOrAJudge(t *testing.T) {
	w := newUnchangedWorld(t, "api", "web")
	pass, judged := w.gate()
	if !pass || judged != 0 {
		t.Fatalf("pass %v after %d judge calls, want a pass with none", pass, judged)
	}
	got := w.decisions()
	if len(got) != 1 || !got[0].Unchanged || !got[0].Verified || got[0].Skipped || len(got[0].Verdicts) != 0 ||
		got[0].Heads["api"] != w.bases["api"] || got[0].Heads["web"] != w.bases["web"] {
		t.Fatalf("recorded %+v, want one unchanged decision at the bases %v", got, w.bases)
	}
	if !strings.HasPrefix(got[0].Message, "No changes; checks not needed.") {
		t.Errorf("message %q", got[0].Message)
	}
}

// Any change in any pushable checkout keeps the Run gated: its report (here
// none at all) is judged as before and sent back.
func TestAnyChangeInAnyCheckoutKeepsTheRunGated(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(w *unchangedWorld)
	}{
		{"unstaged edit", func(w *unchangedWorld) { w.write("web", "README.md", "changed\n") }},
		{"staged edit", func(w *unchangedWorld) {
			w.write("web", "README.md", "staged\n")
			w.git(w.dir("web"), "add", "README.md")
		}},
		{"staged then restored worktree", func(w *unchangedWorld) {
			w.write("web", "README.md", "staged\n")
			w.git(w.dir("web"), "add", "README.md")
			w.write("web", "README.md", "hello\n")
		}},
		{"untracked file", func(w *unchangedWorld) { w.write("web", "notes/new.txt", "new\n") }},
		{"untracked binary", func(w *unchangedWorld) { w.write("web", "image.bin", "\x00\x01\x02\xff") }},
		{"deleted file", func(w *unchangedWorld) { _ = os.Remove(filepath.Join(w.dir("web"), "README.md")) }},
		{"commit", func(w *unchangedWorld) {
			w.write("web", "README.md", "committed\n")
			w.git(w.dir("web"), "commit", "-q", "-am", "work")
		}},
		{"empty commit, same tree", func(w *unchangedWorld) { w.git(w.dir("web"), "commit", "-q", "--allow-empty", "-m", "nothing") }},
		{"commit then revert, same tree", func(w *unchangedWorld) {
			w.write("web", "README.md", "committed\n")
			w.git(w.dir("web"), "commit", "-q", "-am", "work")
			w.git(w.dir("web"), "revert", "--no-edit", "HEAD")
		}},
		{"merge in progress", func(w *unchangedWorld) {
			if err := os.WriteFile(filepath.Join(w.dir("web"), ".git", "MERGE_HEAD"), []byte(w.bases["web"]+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newUnchangedWorld(t, "api", "web")
			c.change(w)
			if pass, judged := w.gate(); pass || judged != 0 {
				t.Fatalf("pass %v (%d judge calls), want the reportless turn sent back", pass, judged)
			}
			if got := w.decisions(); len(got) != 1 || got[0].Unchanged || got[0].Verified {
				t.Fatalf("recorded %+v, want one unverified judgement", got)
			}
		})
	}
}

// Whatever keeps the inspection from establishing that nothing changed
// leaves the Run gated, never passed: a missing checkout, a repository with
// no recorded base, a checkout git cannot read, lux refusing the exec, or an
// exit it reports as failed.
func TestAnInspectionThatCannotEstablishNoChangeKeepsTheRunGated(t *testing.T) {
	for _, c := range []struct {
		name  string
		spoil func(w *unchangedWorld)
	}{
		{"missing checkout", func(w *unchangedWorld) {
			if err := os.RemoveAll(w.dir("web")); err != nil {
				t.Fatal(err)
			}
		}},
		{"unreadable checkout", func(w *unchangedWorld) {
			if err := os.RemoveAll(filepath.Join(w.dir("web"), ".git")); err != nil {
				t.Fatal(err)
			}
		}},
		{"no recorded base", func(w *unchangedWorld) {
			w.exec(`UPDATE runs SET base_shas = base_shas - 'web' WHERE id = $1`, w.run.ID)
		}},
		{"a pushable repository never checked out", func(w *unchangedWorld) {
			w.exec(`UPDATE runs SET lux_pushes = array_append(lux_pushes, 'docs'),
				base_shas = base_shas || jsonb_build_object('docs', $2::text) WHERE id = $1`, w.run.ID, w.bases["web"])
		}},
		{"exec refused", func(w *unchangedWorld) {
			w.lux.fail = &lux.Error{Status: 409, Code: "not_running", Message: "run is stopping"}
		}},
		{"exec failed", func(w *unchangedWorld) { one := 1; w.lux.exit = &one }},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newUnchangedWorld(t, "api", "web")
			c.spoil(w)
			if pass, _ := w.gate(); pass {
				t.Fatal("passed without establishing that nothing changed")
			}
			if got := w.decisions(); len(got) != 1 || got[0].Unchanged {
				t.Fatalf("recorded %+v, want the report judged", got)
			}
		})
	}
}

// A recorded no-change decision is reused by a restarted orchestrator for
// the same turn and placement without inspecting again. A later turn, or
// the same turn read after the agent's session moved to another placement,
// is inspected afresh: an edit made since an earlier empty inspection is
// judged.
func TestANoChangeDecisionHoldsOnlyForItsTurnAndPlacement(t *testing.T) {
	t.Run("restart", func(t *testing.T) {
		w := newUnchangedWorld(t, "api")
		if pass, _ := w.gate(); !pass {
			t.Fatal("the unchanged turn did not pass")
		}
		execs := w.lux.execCount()
		w.write("api", "late.txt", "after the decision\n")
		restarted := &Syncer{DB: w.s.DB, Lux: w.lux, Log: w.s.Log, Judge: &checksFake{err: errors.New("must not be called")}}
		if pass, _, err := restarted.checksBeforePush(context.Background(), w.run); err != nil || !pass {
			t.Fatalf("restart: pass %v, %v", pass, err)
		}
		if w.lux.execCount() != execs {
			t.Error("the restart inspected the checkouts again")
		}
	})
	t.Run("a later turn", func(t *testing.T) {
		w := newUnchangedWorld(t, "api")
		if pass, _ := w.gate(); !pass {
			t.Fatal("the unchanged turn did not pass")
		}
		w.write("api", "src/main.go", "package main\n")
		w.endTurn()
		if pass, _ := w.gate(); pass {
			t.Fatal("an edit after an empty inspection passed")
		}
		if got := w.decisions(); len(got) != 2 || !got[0].Unchanged || got[1].Unchanged {
			t.Fatalf("recorded %+v", got)
		}
	})
	t.Run("a resumed placement", func(t *testing.T) {
		w := newUnchangedWorld(t, "api")
		if pass, _ := w.gate(); !pass {
			t.Fatal("the unchanged turn did not pass")
		}
		w.exec(`UPDATE runs SET agent_session_epoch = agent_session_epoch + 1 WHERE id = $1`, w.run.ID)
		w.write("api", "src/main.go", "package main\n")
		if pass, _ := w.gate(); pass {
			t.Fatal("a decision from another placement was reused over a changed checkout")
		}
	})
	t.Run("a resumed placement still unchanged", func(t *testing.T) {
		w := newUnchangedWorld(t, "api")
		if pass, _ := w.gate(); !pass {
			t.Fatal("the unchanged turn did not pass")
		}
		w.exec(`UPDATE runs SET agent_session_epoch = agent_session_epoch + 1 WHERE id = $1`, w.run.ID)
		execs := w.lux.execCount()
		if pass, _ := w.gate(); !pass || w.lux.execCount() == execs {
			t.Fatalf("pass %v; inspected again: %v", pass, w.lux.execCount() != execs)
		}
	})
}

// A changed turn's judgement is reused as before, whatever the placement.
func TestAJudgedTurnIsReusedAcrossPlacements(t *testing.T) {
	w := newUnchangedWorld(t, "api")
	w.write("api", "src/main.go", "package main\n")
	checksReply(w.harnessWorld, passingChecks)
	acceptingJudge(w.harnessWorld)
	if pass, _, err := w.s.checksBeforePush(context.Background(), w.run); err != nil || !pass {
		t.Fatalf("pass %v, %v", pass, err)
	}
	w.exec(`UPDATE runs SET agent_session_epoch = agent_session_epoch + 1 WHERE id = $1`, w.run.ID)
	w.s.Judge = &checksFake{err: errors.New("must not be called")}
	if pass, _, err := w.s.checksBeforePush(context.Background(), w.run); err != nil || !pass {
		t.Fatalf("after a move: pass %v, %v", pass, err)
	}
}

// At publication, a Run whose turn was recorded unchanged publishes only if
// lux pushed exactly the heads inspection read: a push that moved any
// repository fails the Run, kept, and nothing is published.
func TestAnUnchangedTurnPublishesOnlyWhatInspectionRead(t *testing.T) {
	push := func(w *unchangedWorld, commit string) {
		raw, _ := json.Marshal(map[string]any{"results": []map[string]string{
			{"repo": "api", "branch": "dude/run", "commit": commit, "status": "up-to-date"}}})
		w.run.PushRequestID, w.run.PushResult = "push_1", raw
		w.exec(`UPDATE runs SET push_request_id = 'push_1', push_result = $2::jsonb, turn_done_at = now() WHERE id = $1`, w.run.ID, raw)
		w.exec(`INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
			VALUES ('repo_'||$1, $1, 'prj_'||$1, 'api', 'git://h/api.git', 'main') ON CONFLICT DO NOTHING`, w.run.Org)
		w.exec(`INSERT INTO task_repositories (organization_id, task_id, repository_id, access) VALUES ($1, 'wi_'||$1, 'repo_'||$1, 'write')
			ON CONFLICT DO NOTHING`, w.run.Org)
	}
	status := func(w *unchangedWorld) (string, bool) {
		var s string
		var keep bool
		if err := w.owner.QueryRow(context.Background(), `SELECT status::text, keep FROM runs WHERE id = $1`, w.run.ID).Scan(&s, &keep); err != nil {
			t.Fatal(err)
		}
		return s, keep
	}
	t.Run("the heads read", func(t *testing.T) {
		w := newUnchangedWorld(t, "api")
		if pass, _ := w.gate(); !pass {
			t.Fatal("the unchanged turn did not pass")
		}
		push(w, w.bases["api"])
		if _, err := w.s.finish(context.Background(), w.run); err != nil {
			t.Fatal(err)
		}
		if s, _ := status(w); s != "completed" {
			t.Fatalf("status %s, want completed", s)
		}
	})
	t.Run("a moved head", func(t *testing.T) {
		w := newUnchangedWorld(t, "api")
		if pass, _ := w.gate(); !pass {
			t.Fatal("the unchanged turn did not pass")
		}
		push(w, strings.Repeat("c", 40))
		if _, err := w.s.finish(context.Background(), w.run); err != nil {
			t.Fatal(err)
		}
		if s, keep := status(w); s != "failed" || !keep {
			t.Fatalf("status %s keep %v, want failed and kept", s, keep)
		}
		var commits int
		if err := w.owner.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2`,
			w.run.ID, delivery.EvGitCommitCreated).Scan(&commits); err != nil || commits != 0 {
			t.Fatalf("%d commits published (%v)", commits, err)
		}
	})
}

func TestParseUnchangedAcceptsOnlyAWholeCleanAccount(t *testing.T) {
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	bases := map[string]string{"api": a, "web": b}
	pushes := []string{"api", "web"}
	for _, c := range []struct {
		name, out string
		exit      int
		want      bool
	}{
		{"both clean", "clean api " + a + "\nclean web " + b + "\n", 0, true},
		{"one missing", "clean api " + a + "\n", 0, false},
		{"one twice", "clean api " + a + "\nclean api " + a + "\n", 0, false},
		{"one changed", "clean api " + a + "\nchanged web " + b + "\n", 0, false},
		{"one failed", "clean api " + a + "\nfail web\n", 4, false},
		{"another head", "clean api " + a + "\nclean web " + a + "\n", 0, false},
		{"a stranger", "clean api " + a + "\nclean web " + b + "\nclean docs " + a + "\n", 0, false},
		{"clean but a failed exit", "clean api " + a + "\nclean web " + b + "\n", 1, false},
		{"nothing", "", 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, got := parseUnchanged(c.out, c.exit, pushes, bases); got != c.want {
				t.Errorf("unchanged %v, want %v", got, c.want)
			}
		})
	}
}
