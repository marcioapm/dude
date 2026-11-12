package forge

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"time"

	"go.yaml.in/yaml/v3"
)

// A workflow_dispatch: dude starts a GitHub Actions workflow on a ref and
// finds its run afterwards. GitHub answers a dispatch with 204 and no run
// id, so the run is found by what it is: a run of that workflow, on that
// branch, of event workflow_dispatch, created since the dispatch.

// WorkflowFile is a workflow's file name under .github/workflows: what
// run_workflow names and GitHub's dispatch and runs endpoints take.
var WorkflowFile = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}\.ya?ml$`)

// DispatchWorkflow starts workflow (its file name or id) on ref with
// inputs: POST /repos/{slug}/actions/workflows/{workflow}/dispatches.
// GitHub answers 204 with no run id.
func (g *GitHub) DispatchWorkflow(ctx context.Context, slug, workflow, ref string, inputs map[string]any) error {
	if inputs == nil {
		inputs = map[string]any{}
	}
	return g.do(ctx, "POST", fmt.Sprintf("/repos/%s/actions/workflows/%s/dispatches", slug, url.PathEscape(workflow)),
		map[string]any{"ref": ref, "inputs": inputs}, nil)
}

// RunState is one Actions run as dude supervises it: its status
// (queued, in_progress, completed, …), conclusion once completed, and the
// attempt GitHub reports now (a re-run is the same id, attempt n+1).
type RunState struct {
	ID                 int64
	Status, Conclusion string
	Attempt            int
	URL, CreatedAt     string
}

type runStateJSON struct {
	ID         int64  `json:"id"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Attempt    int    `json:"run_attempt"`
	HTMLURL    string `json:"html_url"`
	CreatedAt  string `json:"created_at"`
	Event      string `json:"event"`
	HeadBranch string `json:"head_branch"`
}

func (r runStateJSON) state() RunState {
	return RunState{ID: r.ID, Status: r.Status, Conclusion: r.Conclusion, Attempt: max(r.Attempt, 1), URL: r.HTMLURL, CreatedAt: r.CreatedAt}
}

// dispatchedRunsPage bounds the candidates read for one dispatch: more
// than one is already "cannot tell".
const dispatchedRunsPage = 20

// DispatchedRuns lists workflow's runs on branch of event
// workflow_dispatch created at or after since, newest first. A run of
// another event or branch GitHub returns anyway is left out.
func (g *GitHub) DispatchedRuns(ctx context.Context, slug, workflow, branch string, since time.Time) ([]RunState, error) {
	var listed struct {
		Runs []runStateJSON `json:"workflow_runs"`
	}
	q := url.Values{"branch": {branch}, "event": {"workflow_dispatch"}, "per_page": {fmt.Sprint(dispatchedRunsPage)},
		"created": {">=" + since.UTC().Format(time.RFC3339)}}
	if err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/actions/workflows/%s/runs?%s", slug, url.PathEscape(workflow), q.Encode()),
		nil, &listed); err != nil {
		return nil, err
	}
	out := []RunState{}
	for _, r := range listed.Runs {
		if r.Event != "workflow_dispatch" || r.HeadBranch != branch {
			continue
		}
		if at, err := time.Parse(time.RFC3339, r.CreatedAt); err == nil && at.Before(since) {
			continue
		}
		out = append(out, r.state())
	}
	return out, nil
}

// RunStateOf reads Actions run id's current status, conclusion and attempt.
func (g *GitHub) RunStateOf(ctx context.Context, slug string, id int64) (RunState, error) {
	var r runStateJSON
	if err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/actions/runs/%d", slug, id), nil, &r); err != nil {
		return RunState{}, err
	}
	return r.state(), nil
}

// WorkflowTriggersAt reads workflow file name's on: at ref, as an agent's
// read (its budget): the events it runs on, sorted. A file that does not
// parse, or names no on:, is an error.
func (g *GitHub) WorkflowTriggersAt(ctx context.Context, slug, name, ref string) ([]string, error) {
	ctx = agentReadContext(ctx)
	if err := g.readBudget(ctx, slug, "core"); err != nil {
		return nil, err
	}
	raw, err := g.FileAt(ctx, slug, ".github/workflows/"+name, ref, maxWorkflowBytes)
	if err != nil {
		return nil, err
	}
	events, ok := WorkflowTriggers(raw)
	if !ok {
		return nil, fmt.Errorf(".github/workflows/%s at %s has no on: dude can read", name, ref)
	}
	return events, nil
}

// WorkflowTriggers reads one workflow file's on: as WorkflowRunsOnPullRequests
// does: the events named, sorted; ok false when it cannot be read.
func WorkflowTriggers(raw []byte) (events []string, ok bool) {
	on, found := workflowOn(raw)
	if !found {
		return nil, false
	}
	switch on.Kind {
	case yaml.ScalarNode:
		events = []string{on.Value}
	case yaml.SequenceNode:
		for _, n := range on.Content {
			events = append(events, n.Value)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(on.Content); i += 2 {
			events = append(events, on.Content[i].Value)
		}
	default:
		return nil, false
	}
	slices.Sort(events)
	return slices.Compact(events), true
}
