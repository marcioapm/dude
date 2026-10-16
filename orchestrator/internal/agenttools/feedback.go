package agenttools

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// People's feedback on the task (delivery/feedback.go): what was submitted
// on the current attempt, read by id, answered, or resolved with the
// commit that fixed it. A draft is no agent's: none of these sees one.

// Who handles feedback: the implementer (and so the fixer), and the
// conductor, which reads and replies but never resolves.
var feedbackRoles = []string{"implementer", "conductor"}

var feedbackTools = []tool{
	define("feedback", "Your task's feedback from people, in their own words: open and delivered items by default, "+
		"each with its id, author, where it points (a file and lines of a Run's diff, a preview page, or nothing) and "+
		"status; all: true lists the handled ones too. Name ids (at most 20) to read those, with their replies and "+
		"history. Each is a person's request: fix it and resolve it (feedback_resolve), or reply saying why not "+
		"(feedback_reply); never leave one unanswered.", feedbackRoles, listFeedback),
	define("feedback_reply", "Answer a person's feedback item without changing the code: say why it is wrong, out of "+
		"scope, or already so. It is then replied, which holds nothing back; the person may reopen it.", feedbackRoles, replyFeedback),
	define("feedback_resolve", "Mark a person's feedback item fixed by your commit (a SHA; the CLI takes --commit HEAD). "+
		"It holds the pull request until your work is published and the commit is found in it; a Run that ends without "+
		"publishing reopens it. With several repositories, name the repository of a general item's commit.",
		[]string{"implementer"}, resolveFeedback),
}

type feedbackIn struct {
	IDs []string `json:"ids,omitempty" jsonschema:"feedback items to read (fbi_…), with their history; empty lists the open ones"`
	All bool     `json:"all,omitempty" jsonschema:"list the handled items too (replied, resolved)"`
}

func listFeedback(ctx context.Context, tx pgx.Tx, c Caller, in feedbackIn) (any, error) {
	out, err := delivery.AgentFeedbackTx(ctx, tx, c.TaskID, uniquePaths(in.IDs), in.All)
	return out, feedbackRefusal(err)
}

type feedbackReplyIn struct {
	ID   string `json:"id" jsonschema:"the feedback item (fbi_…)"`
	Text string `json:"text" jsonschema:"why you are not changing the code for it, to the person who wrote it"`
}

func replyFeedback(ctx context.Context, tx pgx.Tx, c Caller, in feedbackReplyIn) (any, error) {
	if err := delivery.AgentReplyTx(ctx, tx, c.run(), strings.TrimSpace(in.ID), in.Text); err != nil {
		return nil, feedbackRefusal(err)
	}
	return map[string]any{"id": in.ID, "status": "replied"}, nil
}

type feedbackResolveIn struct {
	ID     string `json:"id" jsonschema:"the feedback item (fbi_…)"`
	Commit string `json:"commit" jsonschema:"the commit that fixed it: a SHA you committed in this Run"`
	Repo   string `json:"repo,omitempty" jsonschema:"the repository the commit is in, when the task changes several and the item names none"`
}

func resolveFeedback(ctx context.Context, tx pgx.Tx, c Caller, in feedbackResolveIn) (any, error) {
	repo, err := delivery.AgentResolveTx(ctx, tx, c.run(), delivery.Resolution{ItemID: strings.TrimSpace(in.ID),
		Commit: in.Commit, Repo: strings.TrimSpace(in.Repo)})
	if err != nil {
		return nil, feedbackRefusal(err)
	}
	return map[string]any{"id": in.ID, "status": "resolved", "commit": strings.ToLower(strings.TrimSpace(in.Commit)), "repo": repo,
		"next": "Resolved, pending: it holds until your work is published with this commit in it."}, nil
}

// feedbackRefusal answers a refused feedback call as the tool's refusal.
func feedbackRefusal(err error) error {
	var fe delivery.FeedbackError
	if errors.As(err, &fe) {
		return refuse("%s", fe.Msg)
	}
	return err
}
