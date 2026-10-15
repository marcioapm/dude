package forge

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// A comment dude posted is changed in place by its id; one deleted on
// GitHub is a 404 the caller can tell from any other refusal.
func TestACommentDudePostedIsEditedInPlace(t *testing.T) {
	fake := fakegithub.New("", "acme/repo")
	n := fake.OpenAsPerson("alice", "feature/x", "main", "X")
	srv := httptest.NewServer(fake.Handler())
	defer srv.Close()
	g := NewGitHub(Credential{Secret: "t", APIBaseURL: srv.URL})
	ctx := context.Background()

	posted, err := g.Comment(ctx, "acme/repo", n, "first")
	if err != nil || posted.CommentID == 0 {
		t.Fatalf("comment: %+v %v", posted, err)
	}
	edited, err := g.EditComment(ctx, "acme/repo", posted.CommentID, "second")
	if err != nil || edited.CommentID != posted.CommentID || edited.URL != posted.URL {
		t.Fatalf("edit: %+v %v, want the same comment %+v", edited, err, posted)
	}
	if got := fake.Pull(n).Comments; len(got) != 1 || got[0].Body != "second" || got[0].UpdatedAt == "" {
		t.Fatalf("comments after the edit: %+v", got)
	}

	fake.DeleteComment(n, posted.CommentID)
	_, err = g.EditComment(ctx, "acme/repo", posted.CommentID, "third")
	var e *Error
	if !errors.As(err, &e) || !e.NotFound() {
		t.Fatalf("editing a deleted comment: %v, want GitHub's 404", err)
	}
}

// The recovery search finds only a verdict comment dude's account posted:
// a person's comment ending in the same marker is theirs.
func TestOnlyDudesVerdictCommentIsFoundAgain(t *testing.T) {
	fake := fakegithub.New("", "acme/repo")
	n := fake.OpenAsPerson("alice", "feature/x", "main", "X")
	srv := httptest.NewServer(fake.Handler())
	defer srv.Close()
	g := NewGitHub(Credential{Secret: "t", APIBaseURL: srv.URL})
	ctx := context.Background()

	fake.Comment(n, "alice", "@dude please address this\n\n"+VerdictMarker("wi_1"))
	if found, err := g.FindVerdictComment(ctx, "acme/repo", n, "wi_1"); err != nil || found.CommentID != 0 {
		t.Fatalf("with only alice's comment: %+v %v, want none found", found, err)
	}
	posted, err := g.Comment(ctx, "acme/repo", n, "## Verification\n\n"+VerdictMarker("wi_1"))
	if err != nil {
		t.Fatal(err)
	}
	if found, err := g.FindVerdictComment(ctx, "acme/repo", n, "wi_1"); err != nil || found.CommentID != posted.CommentID {
		t.Fatalf("after dude posted %d: %+v %v", posted.CommentID, found, err)
	}
}
