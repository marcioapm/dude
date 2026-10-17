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
	define("feedback", feedbackRoles, listFeedback),
	define("feedback_reply", feedbackRoles, replyFeedback),
	define("feedback_resolve", []string{"implementer"}, resolveFeedback),
}

type feedbackIn struct {
	IDs []string `json:"ids,omitempty"`
	All bool     `json:"all,omitempty"`
}

func listFeedback(ctx context.Context, tx pgx.Tx, c Caller, in feedbackIn) (any, error) {
	out, err := delivery.AgentFeedbackTx(ctx, tx, c.TaskID, uniquePaths(in.IDs), in.All)
	return out, feedbackRefusal(err)
}

type feedbackReplyIn struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

func replyFeedback(ctx context.Context, tx pgx.Tx, c Caller, in feedbackReplyIn) (any, error) {
	if err := delivery.AgentReplyTx(ctx, tx, c.run(), strings.TrimSpace(in.ID), in.Text); err != nil {
		return nil, feedbackRefusal(err)
	}
	return map[string]any{"id": in.ID, "status": "replied"}, nil
}

type feedbackResolveIn struct {
	ID     string `json:"id"`
	Commit string `json:"commit"`
	Repo   string `json:"repo,omitempty"`
}

func resolveFeedback(ctx context.Context, tx pgx.Tx, c Caller, in feedbackResolveIn) (any, error) {
	repo, err := delivery.AgentResolveTx(ctx, tx, c.run(), delivery.Resolution{ItemID: strings.TrimSpace(in.ID),
		Commit: in.Commit, Repo: strings.TrimSpace(in.Repo)})
	if err != nil {
		return nil, feedbackRefusal(err)
	}
	return map[string]any{"id": in.ID, "status": "resolved", "commit": strings.ToLower(strings.TrimSpace(in.Commit)), "repo": repo,
		"next": feedbackResolvedNext}, nil
}

// feedbackRefusal answers a refused feedback call as the tool's refusal.
func feedbackRefusal(err error) error {
	var fe delivery.FeedbackError
	if errors.As(err, &fe) {
		return refuse("%s", fe.Msg)
	}
	return err
}
