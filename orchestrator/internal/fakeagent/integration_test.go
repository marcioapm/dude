package fakeagent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const integrationBrief = "Base integration: this branch conflicts with `main` in target. Call sync_base, then " +
	"`git merge lux/main`. Resolve every conflict keeping both sides' intent."

// briefRecording is integrationBrief with the base head it records.
func briefRecording(head string) string {
	return integrationBrief + " The recorded base head is " + head + "."
}

// setScriptFetchWait sets each of the script's waits for the fetch for the
// test.
func setScriptFetchWait(t *testing.T, wait time.Duration) {
	scriptFetchWait = wait
	t.Cleanup(func() { scriptFetchWait = SyncBaseRetryFor / 2 })
}

// conflict is a checkout of a task branch whose FACTORY.md conflicts with
// main's, as lux checks one out: the task's work at HEAD, the base on
// origin's main, and nothing at lux/main until a fetch. Its git runs there.
type conflict struct {
	t          *testing.T
	work, base string
}

func newConflict(t *testing.T) *conflict {
	c := &conflict{t: t, work: t.TempDir()}
	c.git("init", "-q", "-b", "main")
	c.write("original\n")
	c.git("add", ".")
	c.git("commit", "-qm", "seed")
	c.git("switch", "-qc", "task")
	c.write("task intent\n")
	c.git("commit", "-qam", "task")
	c.git("switch", "-q", "main")
	c.write("base intent\n")
	c.git("commit", "-qam", "base")
	c.base = c.git("rev-parse", "HEAD")
	c.git("update-ref", "refs/remotes/origin/main", "main")
	c.git("switch", "-q", "task")
	return c
}

// write sets the checkout's FACTORY.md to content.
func (c *conflict) write(content string) {
	c.t.Helper()
	if err := os.WriteFile(filepath.Join(c.work, "FACTORY.md"), []byte(content), 0o644); err != nil {
		c.t.Fatal(err)
	}
}

func (c *conflict) git(args ...string) string {
	c.t.Helper()
	out, err := exec.Command("git", append([]string{"-C", c.work, "-c", "user.name=Acme", "-c", "user.email=a@example.com"}, args...)...).CombinedOutput()
	if err != nil {
		c.t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// fetch is lux's fetch landing: the base at lux/main.
func (c *conflict) fetch() { c.git("update-ref", "refs/remotes/lux/main", c.base) }

// play runs an integration script as lux-fake runs its lines (http, cd,
// sh, if-exists, echo), with dude's sync_base answered by syncBase; how
// many times it was called.
func (c *conflict) play(script string, syncBase func(call int)) int {
	c.t.Helper()
	calls := 0
	var line func(string)
	line = func(l string) {
		cmd, rest, _ := strings.Cut(l, " ")
		switch cmd {
		case "http":
			if rest != "dude POST /tools/sync_base {}" {
				c.t.Fatalf("an http call other than sync_base: %q", l)
			}
			calls++
			syncBase(calls)
		case "cd":
			if rest != c.work {
				c.t.Fatalf("cd %s, want the checkout %s", rest, c.work)
			}
		case "sh":
			sh := exec.Command("sh", "-c", rest)
			sh.Dir = c.work
			sh.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Acme", "GIT_AUTHOR_EMAIL=a@example.com",
				"GIT_COMMITTER_NAME=Acme", "GIT_COMMITTER_EMAIL=a@example.com")
			// lux-fake reports a failing command and goes on with the script.
			_ = sh.Run()
		case "if-exists":
			path, then, _ := strings.Cut(rest, " ")
			if _, err := os.Stat(filepath.Join(c.work, path)); err == nil {
				line(then)
			}
		case "echo":
		default:
			c.t.Fatalf("a line lux-fake does not run: %q", l)
		}
	}
	for _, l := range strings.Split(script, "\n") {
		line(l)
	}
	return calls
}

// merged checks HEAD is git's default merge of the fetched base into the
// task, keeping both sides and leaving nothing uncommitted.
func (c *conflict) merged(taskHead string) {
	c.t.Helper()
	content, err := os.ReadFile(filepath.Join(c.work, "FACTORY.md"))
	if err != nil {
		c.t.Fatal(err)
	}
	if !strings.Contains(string(content), "task intent") || !strings.Contains(string(content), "base intent") ||
		strings.Contains(string(content), "<<<<<<<") {
		c.t.Fatalf("resolved file: %q", content)
	}
	if parents := strings.Fields(c.git("log", "-1", "--format=%P")); len(parents) != 2 || parents[0] != taskHead || parents[1] != c.base {
		c.t.Fatalf("not a merge of the base into the task: parents %v, want %s %s", parents, taskHead, c.base)
	}
	if msg := c.git("log", "-1", "--format=%s"); msg != "Merge remote-tracking branch 'lux/main' into task" {
		c.t.Fatalf("merge message %q, want git's default", msg)
	}
	if status := c.git("status", "--porcelain"); status != "" {
		c.t.Fatalf("left uncommitted: %s", status)
	}
}

// The script a real lux runs for an integration fixer, played as lux-fake
// plays it: sync_base through the dude service, then, in the repository, a
// merge of lux/<base> resolved keeping both sides.
func TestTheIntegrationScriptMergesTheBaseKeepingBothSides(t *testing.T) {
	if s := IntegrationScript("Fix the typo.", "/workspace/repos/target"); s != "" {
		t.Fatalf("a fix with no integration got a script: %q", s)
	}
	c := newConflict(t)
	task := c.git("rev-parse", "HEAD")
	brief := briefRecording(c.base)
	if calls := c.play(IntegrationScript(brief, c.work), func(int) { c.fetch() }); calls != 1 {
		t.Fatalf("sync_base called %d times for a fetch reported at once", calls)
	}
	c.merged(task)
}

// sync_base answered before lux reported the fetch: lux/main is not there
// yet when the script would merge. It waits for the fetch, and merges the
// base once it lands, rather than failing a merge of nothing.
func TestTheIntegrationScriptWaitsForAFetchReportedLate(t *testing.T) {
	setScriptFetchWait(t, 10*time.Second)
	c := newConflict(t)
	task := c.git("rev-parse", "HEAD")
	brief := briefRecording(c.base)
	calls := c.play(IntegrationScript(brief, c.work), func(int) {
		time.AfterFunc(1500*time.Millisecond, c.fetch)
	})
	if calls != 1 {
		t.Fatalf("sync_base called %d times; the first fetch landed within the wait", calls)
	}
	c.merged(task)
}

// lux/main is there but older than the base the brief recorded (an earlier
// fetch): merging it would be "Already up to date" and publish nothing. The
// script waits for the recorded head, and when the first fetch never lands
// calls sync_base again and merges what that one brings.
func TestTheIntegrationScriptCallsSyncBaseAgainWhenTheFetchNeverLands(t *testing.T) {
	setScriptFetchWait(t, 2*time.Second)
	c := newConflict(t)
	task := c.git("rev-parse", "HEAD")
	c.git("update-ref", "refs/remotes/lux/main", "main~1")
	brief := briefRecording(c.base)
	calls := c.play(IntegrationScript(brief, c.work), func(call int) {
		if call == 2 {
			c.fetch()
		}
	})
	if calls != 2 {
		t.Fatalf("sync_base called %d times, want a second call after the first fetch never landed", calls)
	}
	c.merged(task)
}

// Neither fetch lands, and lux/main holds an older base that conflicts too
// (an earlier fetch): merging it would publish a resolution against a base
// that is not the one recorded. Once both waits run out the script merges
// nothing, so the Run publishes nothing and ends in no_merge.
func TestTheIntegrationScriptMergesNothingWhenNoFetchLands(t *testing.T) {
	setScriptFetchWait(t, time.Second)
	c := newConflict(t)
	task := c.git("rev-parse", "HEAD")
	stale := c.base
	c.git("update-ref", "refs/remotes/lux/main", stale)
	c.git("switch", "-q", "main")
	c.write("later base intent\n")
	c.git("commit", "-qam", "later base")
	recorded := c.git("rev-parse", "HEAD")
	c.git("switch", "-q", "task")
	if out, err := exec.Command("git", "-C", c.work, "merge-tree", "--write-tree", "HEAD", stale).CombinedOutput(); err == nil {
		t.Fatalf("the stale lux/main merges cleanly, want a conflict: %s", out)
	}
	brief := briefRecording(recorded)
	if calls := c.play(IntegrationScript(brief, c.work), func(int) {}); calls != 2 {
		t.Fatalf("sync_base called %d times, want two", calls)
	}
	if head := c.git("rev-parse", "HEAD"); head != task {
		t.Fatalf("HEAD moved to %s (parents %s), want the task's %s unchanged", head, c.git("log", "-1", "--format=%P"), task)
	}
	if status := c.git("status", "--porcelain"); status != "" {
		t.Fatalf("left in the checkout: %s", status)
	}
}

func TestSyncBasePendingReadsTheAnswer(t *testing.T) {
	cases := []struct {
		answer string
		want   bool
	}{
		{`{"repositories":[{"repo":"target","status":"asked"}]}`, true},
		{`{"repositories":[{"repo":"target","status":"fetched","behind":2}]}`, false},
		{`{"repositories":[{"repo":"a","status":"fetched"},{"repo":"b","status":"asked"}]}`, true},
		{`{"repositories":[{"repo":"target","status":"up-to-date"}]}`, false},
		{`{"repositories":[{"repo":"target","status":"failed"}]}`, false},
		{`not json`, false},
	}
	for _, tc := range cases {
		if got := SyncBasePending([]byte(tc.answer)); got != tc.want {
			t.Errorf("SyncBasePending(%s) = %v, want %v", tc.answer, got, tc.want)
		}
	}
}

func TestSyncBaseLandedReadsTheAnswer(t *testing.T) {
	for answer, want := range map[string]bool{
		`{"repositories":[{"repo":"target","status":"fetched","behind":2}]}`:               true,
		`{"repositories":[{"repo":"target","status":"up-to-date"}]}`:                       true,
		`{"repositories":[{"repo":"a","status":"fetched"},{"repo":"b","status":"asked"}]}`: false,
		`{"repositories":[{"repo":"target","status":"failed"}]}`:                           false,
		`{"repositories":[]}`: false,
		`not json`:            false,
	} {
		if got := SyncBaseLanded([]byte(answer)); got != want {
			t.Errorf("SyncBaseLanded(%s) = %v, want %v", answer, got, want)
		}
	}
}
