package agenttools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
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
	"force anything: `git merge lux/<base>`, resolve every conflict keeping both sides' intent, and commit with " +
	"git's default merge message. Implementer, fixer and simplifier: run the affected tests before finishing your Run. " +
	"Conductor: never build, install or run tests; resolve only small changes, publish, and delegate review and test " +
	"before shipping. Delegate larger resolutions to a fix Run."

// SyncBaseFunc asks lux for a sync of a running Run with that Run's push
// credentials (phases.Syncer.SyncBase).
type SyncBaseFunc func(ctx context.Context, org, runID, luxRunID, requestID string, refs []lux.SyncRef) error

type syncBaseIn struct{}

// baseAsk is what the call's transaction found: whose lux Run, where its
// events stood, and each repository's base (Refs).
type baseAsk struct {
	LuxRunID string
	After    int64
	Refs     []lux.SyncRef
	// The repository's name as the task knows it, by lux's.
	Names map[string]string
	// The base head the delivery recorded for the repository this Run
	// integrates (lux's name), when the Run is the delivery's integration.
	Recorded map[string]string
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
	var integration *delivery.Integration
	if d != nil {
		bases = d.State.Bases
		if i := d.State.Integration; i != nil && i.RunID == c.RunID && i.BaseHead != "" {
			integration = i
		}
	}
	repos, err := delivery.TaskRepositories(ctx, tx, c.TaskID)
	if err != nil {
		return ask, err
	}
	ask.Names, ask.Recorded = map[string]string{}, map[string]string{}
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
		ask.Names[name] = r.Name
		if integration != nil && integration.Repo == r.Name {
			ask.Recorded[name] = integration.BaseHead
		}
	}
	if len(ask.Refs) == 0 {
		return ask, refuse("this Run holds no repository it may change: there is no base to bring in")
	}
	return ask, nil
}

// fetchBase asks lux for the fetch, then reads its git.sync reports until
// each repository has one or syncBaseWait passes. A repository whose own
// report has not come is answered from the newest fetch lux reported
// landing for an earlier call of this Run (landedEarlier).
func fetchBase(ctx context.Context, c Caller, ask baseAsk) (syncBaseOut, error) {
	out := syncBaseOut{RequestID: baseRequestPrefix(c.RunID) + fmt.Sprint(time.Now().UnixNano())}
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
	if len(got) < len(ask.Refs) {
		for repo, res := range landedEarlier(ctx, c, ask, out.RequestID) {
			if _, ok := got[repo]; !ok {
				got[repo] = res
			}
		}
	}
	for _, ref := range ask.Refs {
		out.Repositories = append(out.Repositories, baseLine(ask.Names[ref.Repo], ref.Ref, ask.Recorded[ref.Repo], got[ref.Repo]))
	}
	return out, nil
}

// baseRequestPrefix starts the request id of each sync_base fetch of a Run.
func baseRequestPrefix(runID string) string { return "base-" + runID + "-" }

// landedEarlier is, per repository (lux's name), the newest git.sync that
// reports a fetch of its base landing for another sync_base call of this
// Run than own: read from the start of the lux Run's events, since the
// Run's follower may have read past them. The newest, as each fetch sets
// lux/<base> again. A lux Run started afresh has a new checkout, and none.
func landedEarlier(ctx context.Context, c Caller, ask baseAsk, own string) map[string]lux.SyncResult {
	refs := map[string]string{}
	for _, r := range ask.Refs {
		refs[r.Repo] = r.Ref
	}
	prefix := baseRequestPrefix(c.RunID)
	out := map[string]lux.SyncResult{}
	for after := int64(0); ; {
		frames, err := c.env.lux.Events(ctx, ask.LuxRunID, after)
		if err != nil || len(frames) == 0 {
			return out
		}
		for _, f := range frames {
			after = max(after, f.EventID)
			var res lux.SyncResult
			if f.EventType != "git.sync" || json.Unmarshal(f.EventData, &res) != nil || res.RequestID == own ||
				!strings.HasPrefix(res.RequestID, prefix) || refs[res.Repo] == "" || res.Ref != refs[res.Repo] ||
				(res.Status != "fetched" && res.Status != "up-to-date") {
				continue
			}
			out[res.Repo] = res
		}
	}
}

// baseLine is one repository's answer from its git.sync, or from none.
// recorded is the base head an integration of the repository recorded, ""
// for any other writer.
func baseLine(repo, base, recorded string, res lux.SyncResult) BaseSync {
	b := BaseSync{Repo: repo, Base: base, Ref: "lux/" + base, Status: res.Status, Behind: res.Behind, Ahead: res.Ahead,
		Error: res.Error}
	merge := fmt.Sprintf("`git merge-tree --write-tree HEAD lux/%s` shows whether it conflicts; then `git merge lux/%s`, "+
		"resolve, run the affected tests, commit", base, base)
	switch {
	case res.Status == "" && recorded != "":
		b.Status = "asked"
		b.Next = fmt.Sprintf("lux has not reported the fetch yet, so lux/%s may be missing or older than the base you "+
			"integrate: call sync_base again in a moment. %s.", base, delivery.IntegrationMergeWhen(base, recorded)) +
			" Then " + merge + "."
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
