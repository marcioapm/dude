package fakegithub_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// A reply the token posts to a reply in a review thread is, as on GitHub,
// in reply to the thread's root, not to the reply it answered: in what
// the POST returns and in what the pull request then lists.
func TestAReplyToAReplyIsInReplyToTheRoot(t *testing.T) {
	fake, _, n := branchContract(t)
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	root := fake.LineComment(n, "alice", "greet.md", "Why lower case?")
	first := fake.ReplyInThread(n, "bob", root, "On purpose.")
	post := func(to int64) map[string]any {
		t.Helper()
		resp, err := http.Post(fmt.Sprintf("%s/repos/%s/pulls/%d/comments/%d/replies", srv.URL, fake.Slug, n, to),
			"application/json", strings.NewReader(`{"body":"Noted."}`))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("reply to %d: HTTP %d", to, resp.StatusCode)
		}
		var out map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	for _, to := range []int64{root, first} {
		out := post(to)
		if got, _ := out["in_reply_to_id"].(float64); int64(got) != root {
			t.Errorf("a reply to %d: in_reply_to_id %v, want the root %d", to, out["in_reply_to_id"], root)
		}
		id := int64(out["id"].(float64))
		var listed *fakegithub.Comment
		for _, c := range fake.Pull(n).LineComments {
			if c.ID == id {
				listed = &c
			}
		}
		if listed == nil || listed.InReplyTo != root || listed.Author != fakegithub.Login || listed.Path != "greet.md" {
			t.Errorf("a reply to %d listed as %+v, want in reply to %d", to, listed, root)
		}
	}
}
