package forge

import (
	"context"
	"fmt"
	"testing"
)

// A reply in a review thread names the comment it answers.
func TestFeedbackReadsWhichLineCommentAReplyAnswers(t *testing.T) {
	fake, g, n := readinessFake(t)
	ctx := context.Background()
	root := fake.LineComment(n, "bob", "a.txt", "Why?")
	if _, err := g.ReplyToLineComment(ctx, "acme/repo", n, root, "Because."); err != nil {
		t.Fatal(err)
	}
	fs, err := g.Feedback(ctx, "acme/repo", Status{PullRequestRef: PullRequestRef{Number: n}}, "")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Feedback{}
	for _, f := range fs {
		byID[f.ID] = f
	}
	if f := byID[fmt.Sprintf("line-comment-%d", root)]; f.InReplyTo != 0 {
		t.Fatalf("the thread's first comment: %+v", f)
	}
	var reply *Feedback
	for _, f := range fs {
		if f.Body == "Because." {
			reply = &f
		}
	}
	if reply == nil || reply.InReplyTo != root || reply.Kind != KindLineComment {
		t.Fatalf("the reply: %+v, want InReplyTo %d", reply, root)
	}
}
