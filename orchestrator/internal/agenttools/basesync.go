package agenttools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// sync_base: lux fetches the base of each repository the Run may change
// into its checkout as lux/<base> (a sync in mode fetch, with the
// credentials a push takes), and nothing in the checkout moves. Merging it
// is the agent's: git merge, never a rebase, since every move of the task
// branch is a fast-forward.

// baseSyncers may call it: the roles that commit to the task branch.
var baseSyncers = []string{"implementer", "simplifier", delivery.RoleConductor}

// syncBaseWait bounds how long a call waits for lux's git.sync reports.
const syncBaseWait = 20 * time.Second

// syncBaseEvery is how often lux's events are read while it waits.
const syncBaseEvery = 250 * time.Millisecond

const syncBaseDescription = "Fetch the base your task branch merges into (main, or the base it continues) into your " +
	"checkout as lux/<base>, with credentials your checkout does not have: `git fetch origin` cannot. Nothing in your " +
	"checkout moves. It answers, per repository, the ref to merge, how many commits the base is ahead of you, and how " +
	"to see whether it conflicts (`git merge-tree --write-tree HEAD lux/<base>`). Then merge, never rebase, and never " +
	"force anything: `git merge lux/<base>`, resolve every conflict keeping both sides' intent, run the affected " +
	"tests, and commit with git's default merge message. It is published the normal way: your Run's work when it " +
	"finishes, or a conductor's publish."

// SyncBaseFunc asks lux for a sync of a running Run with that Run's push
// credentials (phases.Syncer.SyncBase).
type SyncBaseFunc func(ctx context.Context, org, runID, luxRunID, requestID string, refs []lux.SyncRef) error

type syncBaseIn struct{}

// baseAsk is what the call's transaction found: whose lux Run, where its
// events stood, and each repository's base.
type baseAsk struct {
	LuxRunID string
	After    int64
	Refs     []lux.SyncRef
	// Base by lux's name for the repository.
	Bases map[string]string
	// The repository's name as the task knows it, by lux's.
	Names map[string]string
}

// BaseSync is one repository's answer.
type BaseSync struct {
	Repo string `json:"repo"`
	Base string `json:"base"`
	// What to merge: lux/<base>.
	Ref string `json:"ref"`
	// fetched, up-to-date, failed, or asked (lux has not reported yet).
	Status string `json:"status"`
	// Commits the base has that HEAD lacks, and the reverse.
	Behind int    `json:"behind"`
	Ahead  int    `json:"ahead"`
	Error  string `json:"error,omitempty"`
	// What to do next, in a line.
	Next string `json:"next"`
}

type syncBaseOut struct {
	RequestID    string     `json:"requestId"`
	Repositories []BaseSync `json:"repositories"`
}

func checkSyncBase(ctx context.Context, tx pgx.Tx, c Caller, _ syncBaseIn) (baseAsk, error) {
	var ask baseAsk
	var luxRun, luxState string
	var held []string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(lux_run_id, ''), COALESCE(lux_state, ''), lux_after_event, lux_repositories
		FROM runs WHERE id = $1`, c.RunID).Scan(&luxRun, &luxState, &ask.After, &held); err != nil {
		return ask, err
	}
	if luxRun == "" || luxState != "running" {
		return ask, refuse("your container is not running as lux sees it; call sync_base again in a moment")
	}
	ask.LuxRunID = luxRun
	d, err := delivery.ReadDelivery(ctx, tx, c.TaskID)
	if err != nil {
		return ask, err
	}
	var bases map[string]string
	if d != nil {
		bases = d.State.Bases
	}
	repos, err := delivery.TaskRepositories(ctx, tx, c.TaskID)
	if err != nil {
		return ask, err
	}
	ask.Bases, ask.Names = map[string]string{}, map[string]string{}
	for _, r := range repos {
		name := lux.SpecName(r.Name)
		if r.Access != "write" || !slices.Contains(held, name) {
			continue
		}
		base := r.DefaultBranch
		if b := bases[r.Name]; b != "" {
			base = b
		}
		ask.Refs = append(ask.Refs, lux.SyncRef{Repo: name, Ref: base, Mode: lux.SyncFetch})
		ask.Bases[name], ask.Names[name] = base, r.Name
	}
	if len(ask.Refs) == 0 {
		return ask, refuse("this Run holds no repository it may change: there is no base to bring in")
	}
	return ask, nil
}

// fetchBase asks lux for the fetch, then reads its git.sync reports until
// each repository has one or syncBaseWait passes.
func fetchBase(ctx context.Context, c Caller, ask baseAsk) (syncBaseOut, error) {
	out := syncBaseOut{RequestID: fmt.Sprintf("base-%s-%d", c.RunID, time.Now().UnixNano())}
	if c.env.syncBase == nil || c.env.lux == nil {
		return out, refuse("this dude cannot ask lux for a sync: sync_base is not available here")
	}
	err := c.env.syncBase(ctx, c.Org, c.RunID, ask.LuxRunID, out.RequestID, ask.Refs)
	if lux.SyncModesRefused(err) {
		return out, refuse("this lux has no sync modes, so it cannot fetch your base without moving your checkout: " +
			"sync_base needs a lux with mode fetch. Ask a person to bring the base in, or carry on without it")
	}
	if le, ok := lux.AsError(err); ok && !le.Retryable() {
		return out, refuse("lux would not fetch your base: %s", le.Message)
	}
	if err != nil {
		return out, err
	}
	wait, cancel := context.WithTimeout(ctx, c.env.syncBaseWait)
	defer cancel()
	got := map[string]lux.SyncResult{}
	deadline := time.Now().Add(c.env.syncBaseWait)
	after := ask.After
	for len(got) < len(ask.Refs) {
		frames, err := c.env.lux.Events(wait, ask.LuxRunID, after)
		if err != nil {
			break
		}
		for _, f := range frames {
			after = max(after, f.EventID)
			if f.EventType != "git.sync" {
				continue
			}
			var res lux.SyncResult
			if json.Unmarshal(f.EventData, &res) == nil && res.RequestID == out.RequestID {
				got[res.Repo] = res
			}
		}
		if len(got) == len(ask.Refs) || time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-time.After(syncBaseEvery):
		}
	}
	for _, ref := range ask.Refs {
		out.Repositories = append(out.Repositories, baseLine(ask.Names[ref.Repo], ref.Ref, got[ref.Repo]))
	}
	return out, nil
}

// baseLine is one repository's answer from its git.sync, or from none.
func baseLine(repo, base string, res lux.SyncResult) BaseSync {
	b := BaseSync{Repo: repo, Base: base, Ref: "lux/" + base, Status: res.Status, Behind: res.Behind, Ahead: res.Ahead,
		Error: res.Error}
	merge := fmt.Sprintf("`git merge-tree --write-tree HEAD lux/%s` shows whether it conflicts; then `git merge lux/%s`, "+
		"resolve, run the affected tests, commit", base, base)
	switch {
	case res.Status == "":
		b.Status = "asked"
		b.Next = fmt.Sprintf("lux has not reported the fetch yet: in a moment, `git rev-list --count HEAD..lux/%s` says "+
			"how far behind you are (an error: not fetched yet); then %s.", base, merge)
	case res.Status == "failed":
		b.Next = "lux could not fetch it; call sync_base again, or carry on without it."
	case res.Behind == 0:
		b.Next = fmt.Sprintf("You already have everything on %s: nothing to merge.", base)
	default:
		b.Next = fmt.Sprintf("%s is %d commits ahead of you: %s.", base, res.Behind, merge)
	}
	return b
}

func settleSyncBase(_ context.Context, _ pgx.Tx, _ Caller, _ baseAsk, out syncBaseOut) (syncBaseOut, error) {
	return out, nil
}

var baseSyncTools = []tool{
	around(define("sync_base", syncBaseDescription, baseSyncers, checkSyncBase), fetchBase, settleSyncBase),
}
