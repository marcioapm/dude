package forge

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// A workflow_dispatch: dude starts a GitHub Actions workflow on a ref.
// Asked with return_run_details, GitHub answers 200 with the run it
// created (workflow-dispatch-response): that run is the dispatch's, no
// guess. An older GitHub (or GHES) answers 204 with no run id: the
// dispatch went out, and which run it started is not known.

// WorkflowFile is a workflow's file name under .github/workflows: what
// run_workflow names and GitHub's dispatch and runs endpoints take.
var WorkflowFile = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}\.ya?ml$`)

// DispatchedRun is the run GitHub says a dispatch created; ID 0 when it
// answered 204 (no run details).
type DispatchedRun struct {
	ID  int64
	URL string
}

// DispatchWorkflow starts workflow (its file name or id) on ref with
// inputs: POST /repos/{slug}/actions/workflows/{workflow}/dispatches, with
// return_run_details. GitHub answers 200 with the run's id and URLs; an
// older one 204 with none (ID 0).
func (g *GitHub) DispatchWorkflow(ctx context.Context, slug, workflow, ref string, inputs map[string]any) (DispatchedRun, error) {
	if inputs == nil {
		inputs = map[string]any{}
	}
	var out struct {
		ID      int64  `json:"workflow_run_id"`
		HTMLURL string `json:"html_url"`
	}
	err := g.do(ctx, "POST", fmt.Sprintf("/repos/%s/actions/workflows/%s/dispatches", slug, url.PathEscape(workflow)),
		map[string]any{"ref": ref, "inputs": inputs, "return_run_details": true}, &out)
	return DispatchedRun{ID: out.ID, URL: out.HTMLURL}, err
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

// RunStateOf reads Actions run id's current status, conclusion and attempt.
func (g *GitHub) RunStateOf(ctx context.Context, slug string, id int64) (RunState, error) {
	var r runStateJSON
	if err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/actions/runs/%d", slug, id), nil, &r); err != nil {
		return RunState{}, err
	}
	return r.state(), nil
}

// WorkflowTriggersAt reads workflow file name's on: at ref, as an agent's
// read (its budget): the events it runs on, sorted, and the file itself. A
// file that does not parse, or names no on:, is an error.
func (g *GitHub) WorkflowTriggersAt(ctx context.Context, slug, name, ref string) ([]string, []byte, error) {
	ctx = agentReadContext(ctx)
	if err := g.readBudget(ctx, slug, "core"); err != nil {
		return nil, nil, err
	}
	raw, err := g.FileAt(ctx, slug, ".github/workflows/"+name, ref, maxWorkflowBytes)
	if err != nil {
		return nil, nil, err
	}
	events, ok := WorkflowTriggers(raw)
	if !ok {
		return nil, nil, fmt.Errorf(".github/workflows/%s at %s has no on: dude can read", name, ref)
	}
	return events, raw, nil
}

// WorkflowRunsForChange reads one workflow file's on: for a task's own
// change, its branch pushed and its pull request into base: ActionsExpected
// when it runs on a pull_request or pull_request_target whose branch
// filters admit base, or on a push whose branch filters admit branch (a
// push filtered to tags alone does not); ActionsNotExpected when it runs
// on neither (schedule, release, a push to main, tags); "" when it cannot
// tell: the file does not parse, or a filter dude does not evaluate (paths,
// types, branches-ignore, a glob beyond * and **). As
// WorkflowRunsOnPullRequests, "cannot tell" is never read as yes.
func WorkflowRunsForChange(raw []byte, branch, base string) string {
	on, ok := workflowOn(raw)
	if !ok {
		return ""
	}
	admits := func(event string, filter *yaml.Node) string {
		var target string
		switch event {
		case "pull_request", "pull_request_target":
			target = base
		case "push":
			target = branch
		default:
			return ActionsNotExpected
		}
		if filter == nil || filter.Kind == yaml.ScalarNode && (filter.Value == "" || filter.Tag == "!!null") {
			return ActionsExpected
		}
		if filter.Kind != yaml.MappingNode {
			return ""
		}
		var branches *yaml.Node
		tags := false
		for i := 0; i+1 < len(filter.Content); i += 2 {
			switch k := filter.Content[i].Value; {
			case k == "branches":
				branches = filter.Content[i+1]
			case event == "push" && (k == "tags" || k == "tags-ignore"):
				tags = true
			default:
				// paths, paths-ignore, branches-ignore, types: not read here.
				return ""
			}
		}
		if branches == nil {
			if tags {
				// A push filtered to tags alone runs on no branch.
				return ActionsNotExpected
			}
			return ActionsExpected
		}
		var patterns []string
		switch branches.Kind {
		case yaml.ScalarNode:
			patterns = []string{branches.Value}
		case yaml.SequenceNode:
			for _, n := range branches.Content {
				if n.Kind != yaml.ScalarNode {
					return ""
				}
				patterns = append(patterns, n.Value)
			}
		default:
			return ""
		}
		for _, p := range patterns {
			matched, ok := branchGlob(p, target)
			if !ok {
				return ""
			}
			if matched {
				return ActionsExpected
			}
		}
		return ActionsNotExpected
	}
	out := ActionsNotExpected
	merge := func(v string) {
		switch {
		case v == ActionsExpected:
			out = ActionsExpected
		case v == "" && out != ActionsExpected:
			out = ""
		}
	}
	switch on.Kind {
	case yaml.ScalarNode:
		merge(admits(on.Value, nil))
	case yaml.SequenceNode:
		for _, n := range on.Content {
			merge(admits(n.Value, nil))
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(on.Content); i += 2 {
			merge(admits(on.Content[i].Value, on.Content[i+1]))
		}
	default:
		return ""
	}
	return out
}

// branchGlob matches a workflow's branch filter pattern as GitHub does,
// for the patterns dude evaluates: a plain name, * (any characters but /)
// and ** (any). ok false for any other special character (?, +, [, ], !)
// or a pattern dude does not read.
func branchGlob(pattern, name string) (matched, ok bool) {
	if pattern == "" || strings.ContainsAny(pattern, "?+[]!\\") {
		return false, false
	}
	var re strings.Builder
	re.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch {
		case strings.HasPrefix(pattern[i:], "**"):
			re.WriteString(".*")
			i++
		case pattern[i] == '*':
			re.WriteString("[^/]*")
		default:
			re.WriteString(regexp.QuoteMeta(pattern[i : i+1]))
		}
	}
	re.WriteString("$")
	r, err := regexp.Compile(re.String())
	if err != nil {
		return false, false
	}
	return r.MatchString(name), true
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
