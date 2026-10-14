package forge

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// What dude does on GitHub for a task's conductor, beyond posting: resolve
// a review thread, mark a draft ready. GitHub's REST API has neither; both
// are GraphQL mutations.

// ReviewThread is a pull request review thread as GraphQL has it.
type ReviewThread struct {
	ID string
	// The pull request it is on: its repository (owner/name) and number.
	Repo     string
	Number   int
	Resolved bool
	// The REST ids of its comments, the first one opening it: what dude's
	// feedback names it by (line-comment-<id>).
	CommentIDs []int64
}

// ReviewThread reads one review thread by its node id; nil when GitHub
// has no such thread (or it is not a thread).
func (g *GitHub) ReviewThread(ctx context.Context, slug, id string) (*ReviewThread, error) {
	const query = `query($id:ID!){node(id:$id){... on PullRequestReviewThread{id isResolved ` +
		`pullRequest{number repository{nameWithOwner}} comments(first:100){nodes{databaseId}}}}}`
	var data struct {
		Node *struct {
			ID          string `json:"id"`
			IsResolved  bool   `json:"isResolved"`
			PullRequest *struct {
				Number     int `json:"number"`
				Repository struct {
					NameWithOwner string `json:"nameWithOwner"`
				} `json:"repository"`
			} `json:"pullRequest"`
			Comments struct {
				Nodes []struct {
					DatabaseID int64 `json:"databaseId"`
				} `json:"nodes"`
			} `json:"comments"`
		} `json:"node"`
	}
	errs, err := g.graphqlIn(ctx, slug, query, map[string]any{"id": id}, &data)
	if err != nil {
		return nil, err
	}
	if data.Node == nil || data.Node.PullRequest == nil || data.Node.ID == "" {
		if len(errs) > 0 && errs[0].Type != "NOT_FOUND" {
			return nil, &Error{Status: 422, Message: "GraphQL: " + errs[0].Message}
		}
		return nil, nil
	}
	t := &ReviewThread{ID: data.Node.ID, Repo: data.Node.PullRequest.Repository.NameWithOwner,
		Number: data.Node.PullRequest.Number, Resolved: data.Node.IsResolved}
	for _, c := range data.Node.Comments.Nodes {
		t.CommentIDs = append(t.CommentIDs, c.DatabaseID)
	}
	return t, nil
}

// ThreadOfComment is the review thread of pull request number that a line
// comment (by its REST id) is in; nil when none is.
func (g *GitHub) ThreadOfComment(ctx context.Context, slug string, number int, comment int64) (*ReviewThread, error) {
	owner, name, _ := strings.Cut(slug, "/")
	const query = `query($owner:String!,$name:String!,$number:Int!,$after:String){repository(owner:$owner,name:$name){` +
		`pullRequest(number:$number){reviewThreads(first:100,after:$after){nodes{id isResolved comments(first:100){nodes{databaseId}}}` +
		`pageInfo{hasNextPage endCursor}}}}}`
	var after *string
	for range 20 {
		var data struct {
			Repository *struct {
				PullRequest *struct {
					ReviewThreads struct {
						Nodes []struct {
							ID         string `json:"id"`
							IsResolved bool   `json:"isResolved"`
							Comments   struct {
								Nodes []struct {
									DatabaseID int64 `json:"databaseId"`
								} `json:"nodes"`
							} `json:"comments"`
						} `json:"nodes"`
						PageInfo struct {
							HasNextPage bool   `json:"hasNextPage"`
							EndCursor   string `json:"endCursor"`
						} `json:"pageInfo"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		}
		errs, err := g.graphql(ctx, query, map[string]any{"owner": owner, "name": name, "number": number, "after": after}, &data)
		if err != nil {
			return nil, err
		}
		if len(errs) > 0 || data.Repository == nil || data.Repository.PullRequest == nil {
			msg := "no pull request"
			if len(errs) > 0 {
				msg = errs[0].Message
			}
			return nil, &Error{Status: 422, Message: "GraphQL: " + msg}
		}
		threads := data.Repository.PullRequest.ReviewThreads
		for _, n := range threads.Nodes {
			t := &ReviewThread{ID: n.ID, Repo: slug, Number: number, Resolved: n.IsResolved}
			for _, c := range n.Comments.Nodes {
				t.CommentIDs = append(t.CommentIDs, c.DatabaseID)
			}
			if slices.Contains(t.CommentIDs, comment) {
				return t, nil
			}
		}
		if !threads.PageInfo.HasNextPage {
			return nil, nil
		}
		c := threads.PageInfo.EndCursor
		after = &c
	}
	return nil, nil
}

// ResolveReviewThread marks a review thread resolved.
func (g *GitHub) ResolveReviewThread(ctx context.Context, slug, id string) error {
	const mutation = `mutation($id:ID!){resolveReviewThread(input:{threadId:$id}){thread{id isResolved}}}`
	return g.mutate(ctx, slug, mutation, map[string]any{"id": id})
}

// MarkReadyForReview takes a draft pull request out of draft, by its node id.
func (g *GitHub) MarkReadyForReview(ctx context.Context, slug, nodeID string) error {
	const mutation = `mutation($id:ID!){markPullRequestReadyForReview(input:{pullRequestId:$id}){pullRequest{id isDraft}}}`
	return g.mutate(ctx, slug, mutation, map[string]any{"id": nodeID})
}

// mutate runs a GraphQL mutation: an error GraphQL answers with is GitHub
// refusing it (a 422 *Error, its message GitHub's).
func (g *GitHub) mutate(ctx context.Context, slug, mutation string, vars map[string]any) error {
	var data map[string]any
	errs, err := g.graphqlIn(ctx, slug, mutation, vars, &data)
	if err != nil {
		return err
	}
	if len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Message
		}
		status := 422
		if errs[0].Type == "FORBIDDEN" {
			status = 403
		}
		return &Error{Status: status, Message: "GraphQL: " + strings.Join(msgs, "; ")}
	}
	return nil
}

// LineCommentID is the REST id a line comment's feedback id names
// (line-comment-<id>); 0 for any other.
func LineCommentID(feedbackID string) int64 {
	var id int64
	if _, err := fmt.Sscanf(feedbackID, "line-comment-%d", &id); err != nil {
		return 0
	}
	return id
}
