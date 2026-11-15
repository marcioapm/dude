package delivery

import (
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// dudesThreads is DudesThreadsTx for the child world's task, after
// events of its (type, payload) are recorded.
func (w *childWorld) dudesThreads(events ...string) *forge.Threads {
	w.t.Helper()
	for i := 0; i < len(events); i += 2 {
		w.exec(`INSERT INTO events (id, organization_id, event_type, task_id, actor_type, actor_id, source, payload)
			VALUES ('ev_t' || $1::int, $2, $3, 'wi_life', 'system', 'test', 'test', $4::jsonb)`, i, w.org, events[i], events[i+1])
	}
	var out *forge.Threads
	if err := w.s.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) (err error) {
		out, err = DudesThreadsTx(w.ctx, tx, "wi_life")
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
	return out
}

var threadEvents = []string{
	EvReviewSubmitted, `{"comments":[{"commentId":1,"findingId":"rf_1"},{"commentId":2}]}`,
	EvReviewSubmitted, `{"comments":[{"commentId":1}]}`, // a later round's copy names no finding
	EvReviewThreadReplied, `{"replyId":"line-comment-3"}`,
	EvReviewThreadReplied, `{"replyId":"issue-comment-4"}`,
	EvChatMessage, `{"by":"conductor","github":{"feedbackId":"line-comment-5"}}`,
}

// A review-only task's threads: its reviews' inline comments are roots,
// each with the first finding named for it; dude's replies in them are
// its own. A conductor's reply is neither: on GitHub no reply's
// in_reply_to_id is ever a reply's id.
func TestDudesThreadsOfAReviewOnlyTask(t *testing.T) {
	got := reviewOnlyWorld(t).dudesThreads(threadEvents...)
	want := &forge.Threads{Roots: map[int64]string{1: "rf_1", 2: ""}, Own: map[int64]bool{3: true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("threads %+v, want %+v", got, want)
	}
}

// A writing task has no threads, whatever its ledger says.
func TestDudesThreadsOfAWritingTaskAreNil(t *testing.T) {
	for _, mode := range []string{"", ModeReview, ModeContinue} {
		w := newChildWorld(t)
		if mode != "" {
			w.exec(`INSERT INTO task_sources (task_id, organization_id, repository_id, kind, number, head_branch, base_branch, mode)
				VALUES ('wi_life', $1, 'repo_life', 'pr', 7, 'work', 'main', $2)`, w.org, mode)
		}
		if got := w.dudesThreads(threadEvents...); got != nil {
			t.Fatalf("mode %q: threads %+v", mode, got)
		}
	}
}

// A reply dude posted and GitHub lists before its thread_replied event is
// recorded is still dude's own: by dude's login, in one of its threads, in
// syncThreads' words. A person's words, or dude's account's other words,
// are not.
func TestOwnThreadRepliesNotRecordedYet(t *testing.T) {
	fake := fakegithub.New(t.TempDir(), "acme/repo")
	fake.Set(func(s *fakegithub.Server) { s.Poster = "pat-owner" })
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	gh := forge.NewGitHub(forge.Credential{Auth: "pat", Secret: "ghp", APIBaseURL: srv.URL})
	line := func(id, author, body string, to int64) forge.Feedback {
		return forge.Feedback{ID: id, Author: author, Body: body, Kind: forge.KindLineComment, InReplyTo: to}
	}
	threads := &forge.Threads{Roots: map[int64]string{1: "rf_1"}, Own: map[int64]bool{}}
	if err := OwnThreadReplies(t.Context(), gh, threads, []forge.Feedback{
		line("line-comment-10", "pat-owner", "Withdrawn: the caller checks.", 1),
		line("line-comment-11", "Pat-Owner", "Fixed at abc1234.", 1),
		line("line-comment-12", "alice", "Withdrawn: the caller checks.", 1),
		line("line-comment-13", "pat-owner", "Thanks, I will look.", 1),
		line("line-comment-14", "pat-owner", "Fixed at abc1234.", 2),
		{ID: "issue-comment-15", Author: "pat-owner", Body: "Fixed at abc1234.", Kind: forge.KindComment},
		// What syncThreads posts, whatever its words: both statuses, a
		// note over lines, a head not known.
		line("line-comment-20", "pat-owner", threadReplyText("accepted", "wrong: the caller\nchecks it.", "abc1234def"), 1),
		line("line-comment-21", "pat-owner", threadReplyText("accepted", "duplicate: rf_2", "abc1234def"), 1),
		line("line-comment-22", "pat-owner", threadReplyText("resolved", "", "abc1234def5678"), 1),
		line("line-comment-23", "pat-owner", threadReplyText("resolved", "", ""), 1),
		// Near misses by dude's own account: its owner's words, not dude's.
		line("line-comment-30", "pat-owner", "Fixed the typo, thanks.", 1),
		line("line-comment-31", "pat-owner", "Withdrawn:x.", 1),
		line("line-comment-32", "pat-owner", "Fixed at abc1234. Also the other one.", 1),
		line("line-comment-33", "pat-owner", "Withdrawn: ok", 1),
		line("line-comment-34", "pat-owner", "Fixed at abc1234", 1),
		line("line-comment-35", "pat-owner", "Fixed at abc 1234.", 1),
	}); err != nil {
		t.Fatal(err)
	}
	if want := map[int64]bool{10: true, 11: true, 20: true, 21: true, 22: true, 23: true}; !reflect.DeepEqual(threads.Own, want) {
		t.Fatalf("own %v, want %v", threads.Own, want)
	}
	if err := OwnThreadReplies(t.Context(), nil, nil, []forge.Feedback{line("line-comment-10", "pat-owner", "Fixed at a.", 1)}); err != nil {
		t.Fatalf("a writing task's nil threads: %v", err)
	}
}
