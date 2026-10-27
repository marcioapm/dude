package chat_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/chat"
)

// refuseSessions makes the database refuse every new session, as a
// deterministic refusal of a brainstorm (SQLSTATE P0001, not transient).
func (w *world) refuseSessions() {
	w.t.Helper()
	w.exec(`CREATE FUNCTION refuse_session() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'sessions refused'; END $$`)
	w.exec(`CREATE TRIGGER refuse_session BEFORE INSERT ON sessions FOR EACH ROW EXECUTE FUNCTION refuse_session()`)
}

const proxyKey = "sk-proxy-test-key-0123456789"

// Requests reads why a failed request failed from chat_requests.failure,
// apart from the model's reasoning, and what it still made from
// made_route: each failure site, the order causes accumulate in, the
// secrets redacted before anything is stored, and the 2000-character bound.
func TestChatAFailedRequestKeepsItsCauseForRequests(t *testing.T) {
	t.Run("the routing call answered 400", func(t *testing.T) {
		w := setup(t)
		w.svc.Judge.LLM.Key = proxyKey
		w.model.refuseNext(http.StatusBadRequest, `{"error":{"message":"invalid request: bad credentials `+proxyKey+`"}}`)
		w.mention("Eli", "C1", "", "why are exports slow?")
		got := w.requests()
		if len(got) != 1 || got[0].Route != "failed" || got[0].ResultKind != "session" || got[0].MadeRoute != "brainstorm" {
			t.Fatalf("requests %+v", got)
		}
		if f := got[0].Failure; f != "the routing call failed: the LLM proxy answered 400: invalid request: bad credentials [REDACTED:DUDE_LLM_KEY]" ||
			strings.Contains(got[0].Reasoning, proxyKey) {
			t.Fatalf("failure %q, reasoning %q", f, got[0].Reasoning)
		}
		if kept := w.one(`SELECT decision->>'failed' FROM chat_events WHERE organization_id = $1`, w.org); kept == "" || strings.Contains(kept, proxyKey) {
			t.Fatalf("decision.failed %q", kept)
		}
	})
	t.Run("a routing error kept unredacted before a deploy is redacted on its retry", func(t *testing.T) {
		w := setup(t)
		w.svc.Judge.LLM.Key = proxyKey
		_, ev := w.receive("Eli", "C1", "", "why are exports slow?")
		w.exec(`UPDATE chat_events SET decision = $3 WHERE organization_id = $1 AND event_id = $2`, w.org, ev,
			`{"failed":"the LLM proxy answered 400: bad credentials `+proxyKey+`","rounds":0}`)
		w.once()
		got := w.requests()
		if len(got) != 1 || got[0].Route != "failed" || got[0].ResultKind != "session" {
			t.Fatalf("requests %+v", got)
		}
		if f := got[0].Failure; f != "the routing call failed: the LLM proxy answered 400: bad credentials [REDACTED:DUDE_LLM_KEY]" ||
			strings.Contains(got[0].Reasoning, proxyKey) {
			t.Fatalf("failure %q, reasoning %q", f, got[0].Reasoning)
		}
		if w.model.calls() != 0 {
			t.Fatalf("the model was asked %d times", w.model.calls())
		}
	})
	t.Run("the routing call answered 400, then its brainstorm was refused", func(t *testing.T) {
		w := setup(t)
		w.refuseSessions()
		w.model.refuseNext(http.StatusBadRequest, `{"error":{"message":"invalid model"}}`)
		w.mention("Eli", "C1", "", "why are exports slow?")
		got := w.requests()
		if len(got) != 1 || got[0].Route != "failed" || got[0].ResultKind != "" || got[0].MadeRoute != "" {
			t.Fatalf("requests %+v", got)
		}
		if f := got[0].Failure; f != "the routing call failed: the LLM proxy answered 400: invalid model; ERROR: sessions refused (SQLSTATE P0001)" {
			t.Fatalf("failure %q", f)
		}
	})
	t.Run("a decision refused, then its brainstorm refused too", func(t *testing.T) {
		w := setup(t)
		w.refuseSessions()
		w.model.queue(answer(map[string]any{"route": "task", "confidence": 0.9, "project": "BIL", "epic": "Exports", "title": "x", "goal": "g"}))
		w.svc.SetBeforeMake(func(ctx context.Context) error {
			_, err := w.owner.Exec(ctx, `DELETE FROM epics WHERE id = $1`, "epc_exp_"+w.org)
			return err
		})
		w.mention("Eli", "C1", "", "file it")
		got := w.requests()
		if len(got) != 1 || got[0].Route != "failed" || got[0].AskedRoute != "task" || got[0].ResultKind != "" || got[0].MadeRoute != "" {
			t.Fatalf("requests %+v", got)
		}
		causes := strings.Split(got[0].Failure, "; ")
		if len(causes) != 2 || !strings.Contains(causes[0], "SQLSTATE 23503") || causes[1] != "ERROR: sessions refused (SQLSTATE P0001)" {
			t.Fatalf("failure %q", got[0].Failure)
		}
	})
	t.Run("a brainstorm made after a failed routing call, its reply never taken: given up", func(t *testing.T) {
		w := setup(t)
		w.model.refuseNext(http.StatusBadRequest, `{"error":{"message":"invalid model"}}`)
		w.fake.FailTimes("chat.postMessage", "internal_error", 100)
		_, ev := w.receive("Eli", "C1", "", "why are exports slow?")
		w.once()
		for range chat.MaxAttempts {
			w.due()
			w.once()
		}
		if e := w.event(ev); !e.Processed {
			t.Fatalf("not given up: %+v", e)
		}
		got := w.requests()
		if len(got) != 1 || got[0].Route != "failed" || got[0].ResultKind != "session" || got[0].MadeRoute != "brainstorm" {
			t.Fatalf("requests %+v", got)
		}
		if f := got[0].Failure; f != "the routing call failed: the LLM proxy answered 400: invalid model; its reply was not posted: Slack did not answer after 6 tries" {
			t.Fatalf("failure %q", f)
		}
	})
	t.Run("a long proxy refusal, then the reply refused, stays within the bound", func(t *testing.T) {
		w := setup(t)
		w.model.refuseNext(http.StatusBadRequest, `{"error":{"message":"`+strings.Repeat("the body is not valid. ", 65)+`"}}`)
		w.fake.FailNext("chat.postMessage", "invalid_arguments")
		w.mention("Eli", "C1", "", "why are exports slow?")
		got := w.requests()
		if len(got) != 1 || got[0].Route != "failed" || len(got[0].Failure) > 2000 ||
			!strings.HasPrefix(got[0].Failure, "the routing call failed: the LLM proxy answered 400: the body is not valid.") ||
			!strings.HasSuffix(got[0].Failure, "; its reply was not posted: slack chat.postMessage: invalid_arguments") {
			t.Fatalf("requests %+v", got)
		}
	})
	t.Run("Slack refused the reply", func(t *testing.T) {
		w := setup(t)
		token := w.one(`SELECT bot_token FROM chat_installations WHERE organization_id = $1`, w.org)
		w.model.queue(answer(map[string]any{"route": "task", "confidence": 0.9, "reasoning": "a clear task", "project": "BIL",
			"title": "Stream it", "goal": "g"}))
		w.fake.FailNext("chat.postMessage", "invalid_arguments "+token)
		w.mention("Eli", "C1", "", "file: stream it")
		got := w.requests()
		if len(got) != 1 || got[0].Route != "failed" || got[0].AskedRoute != "task" || got[0].ResultKind != "task" || got[0].MadeRoute != "task" {
			t.Fatalf("requests %+v", got)
		}
		if f := got[0].Failure; f != "its reply was not posted: slack chat.postMessage: invalid_arguments [REDACTED:bot_token]" {
			t.Fatalf("failure %q", f)
		}
		// The model's own words stay the reasoning, first, and the token is in neither.
		if !strings.HasPrefix(got[0].Reasoning, "a clear task") || strings.Contains(got[0].Reasoning, token) {
			t.Fatalf("reasoning %q", got[0].Reasoning)
		}
	})
	t.Run("a model's resume made a brainstorm instead, its reply refused: made as a brainstorm", func(t *testing.T) {
		w := setup(t)
		w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "title": "My ideas"}))
		w.dm("Márcia", "my private ideas")
		private := w.last().ResultID
		w.model.queue(resume(private))
		w.fake.FailNext("chat.postMessage", "invalid_arguments")
		w.mention("Márcia", "C1", "", "continue that")
		got := w.last()
		if got.Route != "failed" || got.AskedRoute != "resume" || got.ResultKind != "session" || got.ResultID == private || got.MadeRoute != "brainstorm" {
			t.Fatalf("request %+v", got)
		}
	})
	t.Run("a thread bound to an epic, its reply refused: made as a task link", func(t *testing.T) {
		w := setup(t)
		w.model.queue(answer(map[string]any{"route": "epic", "confidence": 0.9, "project": "BIL", "new_epic": map[string]any{"title": "Fast exports",
			"tasks": []map[string]any{{"ref": "a", "title": "Measure"}}}}))
		root := w.mention("Márcia", "C1", "", "open an epic")
		w.fake.FailNext("chat.postMessage", "invalid_arguments")
		w.mention("Eli", "C1", root, "where are we?")
		got := w.last()
		if got.Route != "failed" || got.AskedRoute != "task_link" || got.ResultKind != "epic" || got.MadeRoute != "task_link" {
			t.Fatalf("request %+v", got)
		}
	})
	t.Run("refused before anything was made", func(t *testing.T) {
		w := setup(t)
		w.fake.FailNext("conversations.info", "missing_scope")
		w.receive("Eli", "C1", "", "file it")
		w.once()
		got := w.requests()
		if len(got) != 1 || got[0].Route != "failed" || got[0].ResultKind != "" || got[0].MadeRoute != "" ||
			!strings.HasPrefix(got[0].Failure, "dude could not act on it: ") || !strings.Contains(got[0].Failure, "missing_scope") {
			t.Fatalf("requests %+v", got)
		}
	})
	t.Run("a request that succeeded has none", func(t *testing.T) {
		w := setup(t)
		w.model.queue(answer(map[string]any{"route": "task", "confidence": 0.9, "project": "BIL", "title": "Stream it", "goal": "g"}))
		w.mention("Eli", "C1", "", "file: stream it")
		if got := w.requests(); len(got) != 1 || got[0].Route != "task" || got[0].Failure != "" || got[0].MadeRoute != "" {
			t.Fatalf("requests %+v", got)
		}
	})
}
