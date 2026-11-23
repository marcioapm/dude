package agenttools

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// Previews starts previews for an agent: servers.Service's StartPreview
// and StartBranchPreview, in this package's words (servers imports this
// package, through phases, so it is wired in by cmd/dude-orchestrator).
// actor is the calling Run, identity ledger.ActorAgent and no person.
type Previews interface {
	StartPreview(ctx context.Context, org, taskID, actor string, identity ...string) (Preview, error)
	StartBranchPreview(ctx context.Context, gh *forge.GitHub, org, projectID, branch, actor string, identity ...string) (Preview, error)
}

// Preview is a started preview as an agent is told of it: what a member
// sees of it on the task or project, bounded.
type Preview struct {
	ID      string          `json:"id"`
	State   string          `json:"state"`
	Branch  string          `json:"branch,omitempty"`
	Servers []PreviewServer `json:"servers"`
}

// PreviewServer is one of a preview's servers and where it is served; url
// is empty until lux has given it one.
type PreviewServer struct {
	Name  string `json:"name"`
	URL   string `json:"url,omitempty"`
	State string `json:"state,omitempty"`
}

// PreviewRefusal is the preview service declining: its code and words,
// which are said to the agent.
type PreviewRefusal struct{ Code, Message string }

func (e *PreviewRefusal) Error() string { return e.Message }

// maxPreviewServers bounds the servers a preview tool answers with.
const maxPreviewServers = 20

// previewTools are the preview tools: a conductor's of its own task
// (capabilities.PreviewTask), a session agent's of a linked project's
// branch (capabilities.PreviewBranch).
var previewTools = []tool{
	around(define("start_preview", checkTaskPreview), startTaskPreview, recordPreview),
	around(defineSession("preview_branch", checkBranchPreview).before(forgeFor), startBranchPreview, recordPreview),
}

// start_preview has no input: it is always the calling Run's task.
type startPreviewIn struct{}

type taskPreviewCheck struct{ taskID string }

func checkTaskPreview(_ context.Context, _ pgx.Tx, c Caller, _ startPreviewIn) (taskPreviewCheck, error) {
	if c.env.previews == nil {
		return taskPreviewCheck{}, refuseText(rPreviewsUnavailable)
	}
	if c.TaskID == "" || c.SessionID != "" {
		return taskPreviewCheck{}, refuseText(rPreviewNeedsOwnTask)
	}
	return taskPreviewCheck{taskID: c.TaskID}, nil
}

func startTaskPreview(ctx context.Context, c Caller, p taskPreviewCheck) (Preview, error) {
	out, err := c.env.previews.StartPreview(ctx, c.Org, p.taskID, c.RunID, ledger.ActorAgent, "")
	return bounded(out), previewRefused(err)
}

type previewBranchIn struct {
	Project string `json:"project,omitempty"`
	Branch  string `json:"branch"`
}

type branchPreviewCheck struct{ projectID, branch string }

// checkBranchPreview scopes the call to a project linked to the session
// now (linkedProject), before anything is started.
func checkBranchPreview(ctx context.Context, tx pgx.Tx, c Caller, in previewBranchIn) (branchPreviewCheck, error) {
	if c.env.previews == nil {
		return branchPreviewCheck{}, refuseText(rPreviewsUnavailable)
	}
	p, err := linkedProject(ctx, tx, c, in.Project)
	if err != nil {
		return branchPreviewCheck{}, err
	}
	return branchPreviewCheck{projectID: p.ID, branch: strings.TrimSpace(in.Branch)}, nil
}

func startBranchPreview(ctx context.Context, c Caller, p branchPreviewCheck) (Preview, error) {
	gh, err := githubOf(c)
	if err != nil {
		return Preview{}, err
	}
	out, err := c.env.previews.StartBranchPreview(ctx, gh, c.Org, p.projectID, p.branch, c.RunID, ledger.ActorAgent, "")
	return bounded(out), previewRefused(err)
}

func recordPreview[Checked any](_ context.Context, _ pgx.Tx, _ Caller, _ Checked, p Preview) (Preview, error) {
	return p, nil
}

func bounded(p Preview) Preview {
	if len(p.Servers) > maxPreviewServers {
		p.Servers = p.Servers[:maxPreviewServers]
	}
	if p.Servers == nil {
		p.Servers = []PreviewServer{}
	}
	return p
}

// previewRefused says the service's refusal to the agent, in its words.
func previewRefused(err error) error {
	var r *PreviewRefusal
	if errors.As(err, &r) {
		return refusal{msg: renderRefusal(rPreviewRefused, r.Message), code: r.Code}
	}
	return err
}

func renderRefusal(format string, a ...any) string { return refuse(format, a...).Error() }
