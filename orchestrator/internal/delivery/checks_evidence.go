package delivery

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// The evidence a checks report is judged by: the shell calls a writer Run
// made at its latest commit, with how each ended as its harness recorded
// it. dude does not decide whether a call proves a check (the commands
// agents run come in too many shapes: env prefixes, pipes, annotations,
// subagents); it shows the calls and the conductor decides.
//
// A call is at the commit when it started after the commit was made. The
// commit's time is its committer time, read in the Run's container
// (CheckoutScript); a call's start is its agent.tool.called (or
// agent.child.tool.called) event's occurred_at, which is the orchestrator's
// clock when it translated the harness's event, not the container's. The
// script prints the container's clock too, so the commit time is moved onto
// the orchestrator's clock (Checkouts.Since) before calls are compared with
// it: the two clocks may differ (a lux host's clock is its own), and the
// skew is measured at the read, never assumed zero. A call recorded late
// (dude catching up on a Run's output) is recorded later than it ran, so
// the rule errs towards showing a call made just before the commit, which
// the conductor sees with the rest; it never hides a call made after it.
//
// Known gap: a call made while the tree was dirty, whose changes were then
// reverted to the committed tree, counts as at the commit. The conductor is
// shown whether the tree is clean at the report.

// CheckoutScript reads, per repository, its commit (HEAD by default), that
// commit's committer time, and whether the working tree is clean: nothing
// staged, unstaged or untracked (ignored files aside) and no merge, rebase,
// cherry-pick, revert or sequencer under way. Arguments: the revision ("-"
// for HEAD), then name and checkout per repository. It prints "now <unix>"
// (the container's clock) first, then per repository "repo <name> <sha>
// <committer unix> clean|dirty", "missing <name>" when the checkout has no
// such commit, or "fail <name>" when the checkout is missing or git cannot
// read it, and then exits 4. Read-only POSIX sh: git status refreshes the
// index's stat cache at most.
const CheckoutScript = `set -u
rev=$1; shift
[ "$rev" = - ] && rev=HEAD
status=0
echo "now $(date +%s)"
while [ $# -ge 2 ]; do
  name=$1 dir=$2; shift 2
  if [ ! -d "$dir" ]; then echo "fail $name"; status=4; continue; fi
  if ! head=$(git -C "$dir" rev-parse -q --verify "$rev^{commit}"); then
    if [ "$rev" = HEAD ]; then echo "fail $name"; status=4; else echo "missing $name"; fi
    continue
  fi
  if ! at=$(git -C "$dir" log -1 --format=%ct "$head"); then echo "fail $name"; status=4; continue; fi
  if ! st=$(git -C "$dir" status --porcelain=v1 --untracked-files=all --ignore-submodules=none); then echo "fail $name"; status=4; continue; fi
  op=
  for ref in MERGE_HEAD CHERRY_PICK_HEAD REVERT_HEAD REBASE_HEAD; do
    git -C "$dir" rev-parse -q --verify "$ref" > /dev/null 2>&1 && op=$ref
  done
  for d in rebase-merge rebase-apply sequencer/todo; do
    if ! p=$(git -C "$dir" rev-parse --git-path "$d"); then op=unknown; continue; fi
    case $p in /*) ;; *) p=$dir/$p ;; esac
    [ -e "$p" ] && op=$d
  done
  tree=dirty
  if [ -z "$st" ] && [ -z "$op" ]; then tree=clean; fi
  echo "repo $name $head $at $tree"
done
exit $status`

// CheckoutCommand is the exec that runs CheckoutScript over repos (name →
// checkout), sorted by name, at rev ("" for HEAD).
func CheckoutCommand(rev string, repos map[string]string) []string {
	if rev == "" {
		rev = "-"
	}
	cmd := []string{"sh", "-c", CheckoutScript, "dude-checkout", rev}
	names := make([]string, 0, len(repos))
	for name := range repos {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		cmd = append(cmd, name, repos[name])
	}
	return cmd
}

// CheckoutHead is one repository's commit as CheckoutScript read it.
type CheckoutHead struct {
	Repo string `json:"repo"`
	SHA  string `json:"sha"`
	// Committed: the commit's committer time, on the container's clock.
	Committed int64 `json:"committed"`
	// Clean: the working tree held nothing but that commit.
	Clean bool `json:"clean"`
}

// Checkouts is what CheckoutScript printed, read whole.
type Checkouts struct {
	// Now: the container's clock when it ran.
	Now   int64          `json:"now"`
	Heads []CheckoutHead `json:"heads"`
	// Missing: the repositories without the asked revision.
	Missing []string `json:"missing,omitempty"`
}

// ParseCheckouts reads CheckoutScript's output for repos: ok only with exit
// 0, a clock, and exactly one line for each repository.
func ParseCheckouts(out string, exit int, repos []string) (Checkouts, bool) {
	var c Checkouts
	if exit != 0 {
		return c, false
	}
	seen := map[string]bool{}
	for i, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		f := strings.Fields(line)
		if i == 0 {
			if len(f) != 2 || f[0] != "now" {
				return c, false
			}
			n, err := strconv.ParseInt(f[1], 10, 64)
			if err != nil {
				return c, false
			}
			c.Now = n
			continue
		}
		if len(f) < 2 || !slices.Contains(repos, f[1]) || seen[f[1]] {
			return c, false
		}
		seen[f[1]] = true
		switch {
		case f[0] == "missing" && len(f) == 2:
			c.Missing = append(c.Missing, f[1])
		case f[0] == "repo" && len(f) == 5 && (f[4] == "clean" || f[4] == "dirty") && isHexSHA(f[2]):
			at, err := strconv.ParseInt(f[3], 10, 64)
			if err != nil {
				return c, false
			}
			c.Heads = append(c.Heads, CheckoutHead{Repo: f[1], SHA: f[2], Committed: at, Clean: f[4] == "clean"})
		default:
			return c, false
		}
	}
	if c.Now == 0 || len(seen) != len(repos) {
		return c, false
	}
	return c, true
}

func isHexSHA(s string) bool {
	return len(s) == 40 && strings.Trim(s, "0123456789abcdef") == ""
}

// Clean says every repository read is clean.
func (c Checkouts) Clean() bool {
	for _, h := range c.Heads {
		if !h.Clean {
			return false
		}
	}
	return len(c.Heads) > 0
}

// Since is when, on the orchestrator's clock, the latest of the commits
// read was made: their newest committer time, moved by how far the
// orchestrator's clock was ahead of the container's. readStart is the
// orchestrator's clock as the exec was sent: the container read its clock
// (Now, whole seconds, so up to a second behind its true time) after that,
// so the skew is at least readStart - (Now + 1 s), and that least skew is
// used: every uncertainty (the exec's latency, the clocks' seconds, the
// commit time's whole second) moves the bound earlier, showing a call made
// just before the commit rather than hiding one made after it. Zero when
// no commit was read.
func (c Checkouts) Since(readStart time.Time) time.Time {
	var latest int64
	for _, h := range c.Heads {
		latest = max(latest, h.Committed)
	}
	if latest == 0 {
		return time.Time{}
	}
	skew := readStart.Sub(time.Unix(c.Now+1, 0))
	return time.Unix(latest, 0).Add(skew).UTC()
}

// Headline names the commits read, "api a1b2c3d4e5f6, web …".
func (c Checkouts) Headline() string {
	out := make([]string, len(c.Heads))
	for i, h := range c.Heads {
		out[i] = h.Repo + " " + h.SHA[:12]
	}
	return strings.Join(out, ", ")
}

// EvidenceCalls caps the calls a checks ask shows: one per check a report
// may list (the gate's checksCountMax), newest first. A writer's final
// checks are its last calls; past the cap are older ones, which run_calls
// lists by a filter. EvidenceTail bounds each call's output tail: enough
// for a test runner's summary line or an error, not a log.
const (
	EvidenceCalls = 30
	EvidenceTail  = 160
)

// ChecksEvidence is what a checks ask shows the conductor of the Run's
// calls: its commits at the report and whether the tree was clean then,
// and the shell calls (its own and its subagents') started after the
// latest of those commits was made, newest first.
type ChecksEvidence struct {
	// Read: dude read the Run's checkouts; without it no call is shown.
	Read  bool           `json:"read"`
	Heads []CheckoutHead `json:"heads,omitempty"`
	Clean bool           `json:"clean,omitempty"`
	// Since: the latest commit's time on the orchestrator's clock.
	Since time.Time `json:"since,omitzero"`
	Calls []RunCall `json:"calls,omitempty"`
	// More: calls past EvidenceCalls were left out.
	More bool `json:"more,omitempty"`
}

// EvidenceTx reads the calls at the commits c (read at readStart) for
// runID's checks ask.
func EvidenceTx(ctx context.Context, tx pgx.Tx, runID string, c Checkouts, readStart time.Time) (*ChecksEvidence, error) {
	e := &ChecksEvidence{Read: true, Heads: c.Heads, Clean: c.Clean(), Since: c.Since(readStart)}
	var err error
	e.Calls, e.More, err = RunCallsTx(ctx, tx, runID, e.Since, "", EvidenceCalls, EvidenceTail)
	return e, err
}

// RunCall is one shell call a Run made, as its harness recorded it.
type RunCall struct {
	At      time.Time `json:"at"`
	Command string    `json:"command"`
	// Exit: the exit code the harness recorded; nil when it recorded none
	// (Claude Code records none), or the call has not ended.
	Exit *int `json:"exit,omitempty"`
	// Status: the completion's status (completed, failed, error), "" with
	// none recorded.
	Status string `json:"status,omitempty"`
	Ended  bool   `json:"ended"`
	// Tail: the end of its recorded output.
	Tail string `json:"tail,omitempty"`
	// Child: the subagent (child session) that made it, "" for the Run's
	// own agent.
	Child string `json:"child,omitempty"`
}

// ExitWords is how the call ended, in the words the conductor reads:
// "exit N", or "no exit recorded" with what was.
func (c RunCall) ExitWords() string {
	switch {
	case c.Exit != nil:
		return fmt.Sprintf("exit %d", *c.Exit)
	case !c.Ended:
		return "no exit recorded (not ended)"
	case c.Status != "":
		return "no exit recorded (" + c.Status + ")"
	}
	return "no exit recorded"
}

// Shell calls, as RunCallsTx reads them: the tool events of
// events_run_tool_calls_idx (migration 268), whose WHERE is written here
// with literals so the planner proves the index's.
const runToolEvents = `event_type IN ('agent.tool.called', 'agent.tool.completed', 'agent.child.tool.called', 'agent.child.tool.completed')`

// RunCallsQuery reads a Run's shell calls started after $2, newest first,
// at most $3 of them, those whose command holds $4 (any with ""): each
// call's start, command, child, and its completion's exit code, status and
// output. Both sides are bounded by the Run and the time: the calls by
// their start, their completions by the oldest start among them (a call
// ends after it starts).
const RunCallsQuery = `WITH c AS (
	SELECT cursor, occurred_at, event_type, payload->>'callId' AS call_id, COALESCE(payload->>'childId', '') AS child,
		payload->'input'->>'command' AS command
	FROM events
	WHERE run_id = $1 AND ` + runToolEvents + ` AND event_type IN ('agent.tool.called', 'agent.child.tool.called')
		AND occurred_at > $2 AND payload->'input' ? 'command'
		AND strpos(lower(payload->'input'->>'command'), lower($4)) > 0
	ORDER BY occurred_at DESC, cursor DESC LIMIT $3
), d AS (
	SELECT DISTINCT ON (event_type, payload->>'callId', COALESCE(payload->>'childId', ''))
		event_type, payload->>'callId' AS call_id, COALESCE(payload->>'childId', '') AS child, payload
	FROM events
	WHERE run_id = $1 AND ` + runToolEvents + ` AND event_type IN ('agent.tool.completed', 'agent.child.tool.completed')
		AND occurred_at >= (SELECT min(occurred_at) FROM c)
		AND payload->>'callId' IN (SELECT call_id FROM c)
	ORDER BY event_type, payload->>'callId', COALESCE(payload->>'childId', ''), cursor
)
SELECT c.occurred_at, c.command, c.child, d.payload IS NOT NULL, d.payload->'exitCode', COALESCE(d.payload->>'status', ''),
	concat_ws(E'\n', COALESCE(d.payload->'output'->>'tail', d.payload->'output'->>'head'),
		COALESCE(d.payload->'stdout'->>'tail', d.payload->'stdout'->>'head'),
		COALESCE(d.payload->'stderr'->>'tail', d.payload->'stderr'->>'head'))
FROM c LEFT JOIN d ON d.call_id = c.call_id AND d.child = c.child
	AND d.event_type = CASE c.event_type WHEN 'agent.tool.called' THEN 'agent.tool.completed' ELSE 'agent.child.tool.completed' END
ORDER BY c.occurred_at DESC, c.cursor DESC`

// RunCallsTx is runID's shell calls started after since, newest first: at
// most limit, those whose command holds filter (case-insensitively; all
// for ""), each output's tail cut to tailChars. more: there are others
// past limit.
func RunCallsTx(ctx context.Context, tx pgx.Tx, runID string, since time.Time, filter string, limit, tailChars int) (calls []RunCall, more bool, err error) {
	rows, err := tx.Query(ctx, RunCallsQuery, runID, since, limit+1, filter)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	calls = []RunCall{}
	for rows.Next() {
		var c RunCall
		var exit *float64
		var output string
		if err := rows.Scan(&c.At, &c.Command, &c.Child, &c.Ended, &exit, &c.Status, &output); err != nil {
			return nil, false, err
		}
		if exit != nil {
			n := int(*exit)
			c.Exit = &n
		}
		c.Tail = tailOf(output, tailChars)
		calls = append(calls, c)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(calls) > limit {
		calls, more = calls[:limit], true
	}
	return calls, more, nil
}

// tailOf is the last n characters of s, its whitespace runs made one
// space, "…" before when it was cut.
func tailOf(s string, n int) string {
	s = oneLine(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return "…" + string(r[len(r)-n+1:])
}
