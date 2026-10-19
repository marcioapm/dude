package orchestrator_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// uploadToSession records an image uploaded to a session, as the backend's
// POST /v1/brainstorms/:id/attachments does.
func (s *sessionWorld) uploadToSession(b bucket, id, session, name string, data []byte) {
	s.t.Helper()
	key := "attachments/" + id
	b[key] = data
	mustExec(s.t, s.owner, `INSERT INTO attachments (id, organization_id, session_id, name, content_type, width, height, bytes,
		sha256, object_key, original_content_type, original_width, original_height, original_bytes, original_key)
		VALUES ($1, $2, $3, $4, 'image/png', 10, 10, $5, 'x', $6, 'image/png', 20, 20, $5, $6||'.o')`,
		id, s.org, session, name, len(data), key)
}

// liveSession is a session whose agent runs between turns, with the fake
// lux Run's id.
func (s *sessionWorld) liveSession() (session, run, luxRun string) {
	s.t.Helper()
	s.lux.Decide = hang
	session = s.ok(s.marcio, "POST", "/internal/sessions", map[string]any{"message": "hello"})["id"].(string)
	run, _, _, _ = s.submitted(session)
	s.runningIdle(run)
	_ = s.owner.QueryRow(context.Background(), `SELECT lux_run_id FROM runs WHERE id = $1`, run).Scan(&luxRun)
	return session, run, luxRun
}

// A member's message with an image goes to the session's live agent as a
// directive carrying it: Chat shows it on the message, and the agent's lux
// input has its bytes. An image alone is a message.
func TestASessionMessageCarriesItsImagesToTheAgent(t *testing.T) {
	s := newSessionWorld(t)
	b := s.withImages()
	id, run, luxRun := s.liveSession()
	s.uploadToSession(b, "att_shot", id, "shot.png", screenshot)
	s.uploadToSession(b, "att_alone", id, "alone.png", screenshot)

	out := s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "see this", "attachmentIds": []string{"att_shot"}})
	s.queuedOn(out, run)
	directive := out["directiveId"].(string)
	if n := s.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'chat.message'
		AND payload->>'directiveId' = $2 AND payload->'attachments'->0->>'id' = 'att_shot'`, run, directive); n != 1 {
		t.Errorf("chat.message does not carry the image")
	}
	s.until("lux to have the image", func() bool { return len(s.lux.Attachments(luxRun)[directive]) == 1 })
	if got := s.lux.Attachments(luxRun)[directive][0]; got.Name != "shot.png" || got.ContentType != "image/png" || !bytes.Equal(got.Data, screenshot) {
		t.Errorf("lux got %s %s (%d bytes)", got.Name, got.ContentType, len(got.Data))
	}

	alone := s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "", "attachmentIds": []string{"att_alone"}})
	s.until("lux to have the image alone", func() bool { return len(s.lux.Attachments(luxRun)[alone["directiveId"].(string)]) == 1 })
	if status, body := s.as(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "  "}); status != 400 {
		t.Errorf("neither words nor an image: %d %v", status, body)
	}
}

// Only this session's unsent uploads go with its message, and only from a
// member who may write; a refused message queues nothing.
func TestASessionMessageRefusesImagesItMayNotSend(t *testing.T) {
	s := newSessionWorld(t)
	b := s.withImages()
	id, run, _ := s.liveSession()
	s.join(id, s.joao, "read")
	other := s.session()
	task := s.task()
	s.uploadToSession(b, "att_mine", id, "mine.png", screenshot)
	s.uploadToSession(b, "att_other", other, "other.png", screenshot)
	s.upload(b, "att_task", task, "task.png", screenshot)
	foreign := dbtest.Org(t, s.owner)
	mustExec(t, s.owner, `INSERT INTO sessions (id, organization_id, title) VALUES ('ssn_'||$1, $1, 'F')`, foreign)
	mustExec(t, s.owner, `INSERT INTO attachments (id, organization_id, session_id, name, content_type, width, height, bytes,
		sha256, object_key, original_content_type, original_width, original_height, original_bytes, original_key)
		VALUES ('att_foreign', $1, 'ssn_'||$1, 'f.png', 'image/png', 1, 1, 1, 'x', 'k', 'image/png', 1, 1, 1, 'k.o')`, foreign)

	for _, c := range []struct {
		ids  []string
		want string
	}{
		{[]string{"att_other"}, "image att_other is not one of this session's"},
		{[]string{"att_task"}, "image att_task is not one of this session's"},
		{[]string{"att_foreign"}, "image att_foreign is not one of this session's"},
		{[]string{"att_mine", "att_mine"}, "image att_mine is named twice"},
	} {
		status, body := s.as(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "see", "attachmentIds": c.ids})
		if msg := errorMessage(body); status != 400 || msg != c.want {
			t.Errorf("%v: %d %q, want 400 %q", c.ids, status, msg, c.want)
		}
	}
	if status, _ := s.as(s.joao, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "see", "attachmentIds": []string{"att_mine"}}); status != 403 {
		t.Errorf("a reader sent an image: %d", status)
	}
	if status, _ := s.as(s.outsider, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "see", "attachmentIds": []string{"att_mine"}}); status != 404 {
		t.Errorf("a non-member sent an image: %d", status)
	}
	if n := s.count(`SELECT count(*) FROM directives WHERE run_id = $1`, run); n != 0 {
		t.Fatalf("a refused message queued %d directives", n)
	}
	if n := s.count(`SELECT count(*) FROM attachments WHERE attached_at IS NOT NULL`); n != 0 {
		t.Fatalf("a refused message attached %d images", n)
	}

	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "see", "attachmentIds": []string{"att_mine"}})
	status, body := s.as(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "again", "attachmentIds": []string{"att_mine"}})
	if msg := errorMessage(body); status != 400 || msg != "image att_mine was already sent" {
		t.Errorf("an image sent twice: %d %q", status, msg)
	}
}

// freshAgentGotImage says the session's new agent run was briefed with
// briefed and no image, that att alone went as the one directive queued on
// it, and that lux got att once, on that directive (imageOnce). Returns the
// directive.
func (s *sessionWorld) freshAgentGotImage(run, att, briefed string) string {
	s.t.Helper()
	if p := s.prompt(run); !strings.Contains(p, "## The first message\n\n"+briefed) {
		s.t.Errorf("the briefing does not end with %q:\n%s", briefed, p)
	}
	directive := s.imageDirective(run, att)
	s.imageOnce(run, directive, att)
	return directive
}

// imageDirective is the one directive queued on run, which carries att and
// nothing else.
func (s *sessionWorld) imageDirective(run, att string) string {
	s.t.Helper()
	if n := s.count(`SELECT count(*) FROM directives WHERE run_id = $1`, run); n != 1 {
		s.t.Fatalf("%d directives on %s, want the image's one", n, run)
	}
	var directive string
	var images int
	if err := s.owner.QueryRow(context.Background(), `SELECT d.id, (SELECT count(*) FROM attachments a WHERE a.directive_id = d.id)
		FROM directives d JOIN attachments a ON a.directive_id = d.id WHERE d.run_id = $1 AND a.id = $2`, run, att).
		Scan(&directive, &images); err != nil {
		s.t.Fatalf("no directive on %s carries %s: %v", run, att, err)
	}
	if images != 1 {
		s.t.Errorf("the directive carries %d images, want only %s", images, att)
	}
	return directive
}

// imageOnce waits for run's lux input to have att, then says it is the one
// image any lux Run of the world got, on directive's input, with att's
// name, type and exact bytes; the briefing (prompt) carries none.
func (s *sessionWorld) imageOnce(run, directive, att string) {
	s.t.Helper()
	var luxRun string
	s.until("the fresh agent's lux input to have the image", func() bool {
		_ = s.owner.QueryRow(context.Background(), `SELECT COALESCE(lux_run_id, '') FROM runs WHERE id = $1`, run).Scan(&luxRun)
		return luxRun != "" && len(s.lux.Attachments(luxRun)[directive]) > 0
	})
	// A few more syncer passes, so a copy a later pass sends is counted too.
	for range 3 {
		time.Sleep(untilPoll)
		s.pump()
	}
	time.Sleep(untilPoll)
	var name, contentType string
	if err := s.owner.QueryRow(context.Background(), `SELECT name, content_type FROM attachments WHERE id = $1`, att).
		Scan(&name, &contentType); err != nil {
		s.t.Fatal(err)
	}
	copies := 0
	for _, r := range s.lux.Runs() {
		for input, images := range s.lux.Attachments(r.ID) {
			copies += len(images)
			if r.ID != luxRun || input != directive {
				s.t.Errorf("lux Run %s got %d images on input %q, want them only on %s's %s", r.ID, len(images), input, luxRun, directive)
			}
		}
	}
	if copies != 1 {
		s.t.Fatalf("lux got %d copies of the image, want 1", copies)
	}
	if a := s.lux.Attachments(luxRun)[directive][0]; a.Name != name || a.ContentType != contentType || !bytes.Equal(a.Data, screenshot) {
		s.t.Errorf("lux got %q %s (%d bytes), want %q %s (%d bytes)", a.Name, a.ContentType, len(a.Data), name, contentType, len(screenshot))
	}
}

// oneChatMessage says the session's Chat has one chat.message on run, the
// member's: its words, and att on it under the image directive.
func (s *sessionWorld) oneChatMessage(session, run, text, directive, att string) {
	s.t.Helper()
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND run_id = $2 AND event_type = 'chat.message'`, session, run); n != 1 {
		s.t.Errorf("%d chat.message events on the fresh agent, want 1", n)
	}
	if n := s.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'chat.message' AND payload->>'text' = $2
		AND payload->>'directiveId' = $3 AND payload->'attachments'->0->>'id' = $4`, run, text, directive, att); n != 1 {
		s.t.Errorf("the chat.message does not show %q with %s", text, att)
	}
}

// A first message with words and an image starts the session's agent:
// the words are its briefing, and the image goes to it as its first
// directive, shown on the one message in Chat.
func TestASessionsFirstMessageWithImagesStartsItsAgent(t *testing.T) {
	s := newSessionWorld(t)
	b := s.withImages()
	s.lux.Decide = hang
	id := s.session()
	s.uploadToSession(b, "att_first", id, "first.png", screenshot)
	out := s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "look", "attachmentIds": []string{"att_first"}})
	run, _ := s.brainstorm(id)
	if out["runId"] != run || out["created"] != true {
		t.Fatalf("answered %v, want the new agent %s", out, run)
	}
	directive := s.freshAgentGotImage(run, "att_first", "Márcio Martins: look\n\n[Images come with this message")
	s.oneChatMessage(id, run, "look", directive, "att_first")
}

// An image alone starts the agent too: its briefing says who sent images.
func TestASessionsFirstMessageMayBeAnImageAlone(t *testing.T) {
	s := newSessionWorld(t)
	b := s.withImages()
	s.lux.Decide = hang
	id := s.session()
	s.uploadToSession(b, "att_alone", id, "alone.png", screenshot)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "", "attachmentIds": []string{"att_alone"}})
	run, _ := s.brainstorm(id)
	directive := s.freshAgentGotImage(run, "att_alone", "Márcio Martins: [Images come with this message")
	s.oneChatMessage(id, run, "", directive, "att_alone")
}

// While another member's answer is awaited, a pending model change does not
// swap for a message with images: it is held for the answer on the agent
// that asked.
func TestAHeldSessionMessageWithImagesIgnoresAPendingModelChange(t *testing.T) {
	s := newSessionWorld(t)
	b := s.withImages()
	id, run, question := s.asked(askAna, true)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": nil, "harness": "codex"})
	s.uploadToSession(b, "att_held", id, "held.png", screenshot)
	out := s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "meanwhile", "attachmentIds": []string{"att_held"}})
	s.queuedOn(out, run)
	s.noSwap(id)
	if n := s.count(`SELECT count(*) FROM directives d JOIN attachments a ON a.directive_id = d.id
		WHERE d.id = $1 AND d.held_for = $2 AND a.id = 'att_held'`, out["directiveId"], question); n != 1 {
		t.Errorf("the held message does not hold its image")
	}
}

// While another member's answer is awaited, a message with an image is held
// for it, its image with it.
func TestAHeldSessionMessageKeepsItsImages(t *testing.T) {
	s := newSessionWorld(t)
	b := s.withImages()
	id, run, question := s.asked(askAna, true)
	s.uploadToSession(b, "att_held", id, "held.png", screenshot)
	out := s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "meanwhile", "attachmentIds": []string{"att_held"}})
	directive := out["directiveId"].(string)
	if n := s.count(`SELECT count(*) FROM directives d JOIN attachments a ON a.directive_id = d.id
		WHERE d.id = $1 AND d.run_id = $2 AND d.held_for = $3 AND a.id = 'att_held'`, directive, run, question); n != 1 {
		t.Errorf("the held message does not hold its image")
	}
	if n := s.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'chat.message'
		AND payload->>'heldFor' = $2 AND payload->'attachments'->0->>'id' = 'att_held'`, run, question); n != 1 {
		t.Errorf("chat.message does not show the held image")
	}
}

// A message answering the agent's one question carries its images with the
// answer, as a task's answer does.
func TestASessionAnswerInChatCarriesItsImages(t *testing.T) {
	s := newSessionWorld(t)
	b := s.withImages()
	id, run, question := s.asked(`{"question":"Which layout?"}`, false)
	s.uploadToSession(b, "att_layout", id, "layout.png", screenshot)
	out := s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "this one", "attachmentIds": []string{"att_layout"}})
	if out["questionId"] != question {
		t.Fatalf("the message did not answer the question: %v", out)
	}
	directive := out["directiveId"].(string)
	if n := s.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'question.answered'
		AND payload->>'directiveId' = $2 AND payload->'attachments'->0->>'id' = 'att_layout'`, run, directive); n != 1 {
		t.Errorf("question.answered does not carry the image")
	}
	var luxRun string
	_ = s.owner.QueryRow(context.Background(), `SELECT lux_run_id FROM runs WHERE id = $1`, run).Scan(&luxRun)
	s.until("lux to have the answer's image", func() bool { return len(s.lux.Attachments(luxRun)[directive]) == 1 })
}

// After a model change, a message with images hands the session to a fresh
// agent on the new model as words alone do: its words are the briefing's,
// its image the fresh agent's first directive, and Chat shows one message.
func TestASessionMessageWithImagesAppliesTheModelSwap(t *testing.T) {
	s := newSessionWorld(t)
	b := s.withImages()
	gpt := s.tierOn("Sol", "gpt-6-sol")
	id, run, _ := s.liveSession()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": gpt, "harness": nil})
	s.uploadToSession(b, "att_swap", id, "swap.png", screenshot)
	out := s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "look", "attachmentIds": []string{"att_swap"}})
	next := s.freshAgent(id, run)
	if out["runId"] != next || out["created"] != true {
		t.Errorf("answered %v, want the fresh agent %s", out, next)
	}
	if n := s.count(`SELECT count(*) FROM directives WHERE run_id = $1`, run); n != 0 {
		t.Errorf("the message was queued on the old agent too")
	}
	directive := s.freshAgentGotImage(next, "att_swap", "Márcio Martins: look\n\n[Images come with this message")
	s.oneChatMessage(id, next, "look", directive, "att_swap")
}

// A message with images answering the agent's question after a model
// change answers it through a fresh agent: the answer is its briefing's,
// the image its first directive, and question.answered shows the image.
func TestASessionAnswerWithImagesAppliesTheModelSwap(t *testing.T) {
	s := newSessionWorld(t)
	b := s.withImages()
	id, run, question := s.asked(`{"question":"Which layout?"}`, false)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": nil, "harness": "codex"})
	s.uploadToSession(b, "att_layout", id, "layout.png", screenshot)
	out := s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "this one", "attachmentIds": []string{"att_layout"}})
	next := s.freshAgent(id, run)
	if out["runId"] != next || out["created"] != true || out["questionId"] != question {
		t.Errorf("the answer answered %v, want the fresh agent %s answering %s", out, next, question)
	}
	if n := s.count(`SELECT count(*) FROM questions WHERE id = $1 AND status = 'answered' AND answer = 'this one'`, question); n != 1 {
		t.Errorf("the question is not answered")
	}
	if p := s.prompt(next); !strings.Contains(p, "this one") {
		t.Errorf("the fresh agent's briefing lacks the answer:\n%s", p)
	}
	directive := s.imageDirective(next, "att_layout")
	s.imageOnce(next, directive, "att_layout")
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'question.answered'
		AND payload->>'questionId' = $2 AND payload->'attachments'->0->>'id' = 'att_layout'`, id, question); n != 1 {
		t.Errorf("question.answered does not show the image")
	}
	if n := s.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'chat.message'`, next); n != 0 {
		t.Errorf("the answer was shown again as a chat.message")
	}
}

// refuseEvent makes Postgres refuse every insert of an event of type typ
// for the rest of the test. Each test has its own database (dbtest).
func (s *sessionWorld) refuseEvent(typ string) {
	s.t.Helper()
	mustExec(s.t, s.owner, `CREATE FUNCTION refuse_event() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.event_type = TG_ARGV[0] THEN
				RAISE EXCEPTION 'event refused by the test';
			END IF;
			RETURN NEW;
		END $$`)
	mustExec(s.t, s.owner, `CREATE TRIGGER refuse_event BEFORE INSERT ON events FOR EACH ROW
		EXECUTE FUNCTION refuse_event('`+typ+`')`)
}

// nothingWrittenFor says a refused message left no trace of itself: no
// directive on any of the session's Runs, att unsent, and no event showing
// att.
func (s *sessionWorld) nothingWrittenFor(session, att string) {
	s.t.Helper()
	if n := s.count(`SELECT count(*) FROM directives d JOIN runs r ON r.id = d.run_id WHERE r.session_id = $1`, session); n != 0 {
		s.t.Errorf("%d directives left behind", n)
	}
	if n := s.count(`SELECT count(*) FROM attachments WHERE id = $1 AND directive_id IS NULL AND attached_at IS NULL`, att); n != 1 {
		s.t.Errorf("%s was left attached to a directive", att)
	}
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1
		AND payload->'attachments' @> jsonb_build_array(jsonb_build_object('id', $2::text))`, session, att); n != 0 {
		s.t.Errorf("%d events showing %s left behind", n, att)
	}
}

// A session's first message with an image whose start fails at its last
// write, session.briefed (after the Run, its run.created, the image
// directive, the attachment and the chat.message), leaves none of them.
func TestAFailedFreshAgentStartLeavesNoRunDirectiveOrImage(t *testing.T) {
	s := newSessionWorld(t)
	b := s.withImages()
	id := s.session()
	s.uploadToSession(b, "att_first", id, "first.png", screenshot)
	events := s.count(`SELECT count(*) FROM events WHERE session_id = $1`, id)
	s.refuseEvent(delivery.EvSessionBriefed)
	status, out := s.as(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "look", "attachmentIds": []string{"att_first"}})
	if status != 500 {
		t.Fatalf("answered %d %v, want 500", status, out)
	}
	if n := s.count(`SELECT count(*) FROM runs WHERE session_id = $1`, id); n != 0 {
		t.Errorf("%d Runs left behind", n)
	}
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1`, id); n != events {
		t.Errorf("%d session events, want the %d before the message", n, events)
	}
	s.nothingWrittenFor(id, "att_first")
}

// Try again on a first message with an image whose agent never started,
// after the syncer failed its image directive for want of an agent: the
// fresh agent's briefing still says images follow, and they do, as a copy
// of that directive superseding it. lux gets the image once, and Chat shows
// it once, on the copy.
func TestTryAgainOnAFailedImageStartCarriesTheImage(t *testing.T) {
	s := newSessionWorld(t)
	b := s.withImages()
	id := s.session()
	s.uploadToSession(b, "att_first", id, "first.png", screenshot)
	s.lux.FailStarts("dude.session="+id, 1)
	failed := s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "look", "attachmentIds": []string{"att_first"}})["runId"].(string)
	original := s.imageDirective(failed, "att_first")
	s.until("the image directive failed for want of an agent", func() bool {
		return s.count(`SELECT count(*) FROM directives WHERE id = $1 AND failed_at IS NOT NULL`, original) == 1
	})
	s.lux.Decide = hang
	next := s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/retry", nil)["runId"].(string)
	if next == failed {
		t.Fatalf("the retry is the failed Run %s", failed)
	}
	if p := s.prompt(next); !strings.Contains(p, "## The first message\n\nMárcio Martins: look\n\n[Images come with this message") {
		t.Errorf("the retried briefing does not end with the message and its images note:\n%s", p)
	}
	// The copy carries the image by superseding the original with its words
	// (DirectiveAttachments): the attachment row stays on the original.
	var directive string
	if err := s.owner.QueryRow(context.Background(), `SELECT d.id FROM directives d JOIN directives o ON o.id = d.supersedes
		WHERE d.run_id = $1 AND o.id = $2 AND d.text = o.text`, next, original).Scan(&directive); err != nil {
		t.Fatalf("no directive on the retry supersedes %s with its words: %v", original, err)
	}
	if n := s.count(`SELECT count(*) FROM directives WHERE run_id = $1`, next); n != 1 {
		t.Errorf("%d directives on the retry, want the image's one", n)
	}
	if n := s.count(`SELECT count(*) FROM attachments WHERE directive_id = $1 AND id = 'att_first'`, original); n != 1 {
		t.Errorf("att_first is no longer the original directive's")
	}
	s.imageOnce(next, directive, "att_first")
	// "send it again" is no longer the original's last word: the retry has it.
	if n := s.count(`SELECT count(*) FROM directives WHERE id = $1 AND error = $2`, original,
		"the session's agent stopped before reading it; the next one, "+next+", has it"); n != 1 {
		t.Errorf("the original directive's error does not name the retry")
	}
	var lastError string
	_ = s.owner.QueryRow(context.Background(), `SELECT payload->>'error' FROM events WHERE event_type = 'run.directive.failed'
		AND payload->>'directiveId' = $1 ORDER BY cursor DESC LIMIT 1`, original).Scan(&lastError)
	if !strings.HasSuffix(lastError, next+", has it") {
		t.Errorf("the original's last run.directive.failed says %q, want the retry to have it", lastError)
	}
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'chat.message'
		AND payload->'attachments' @> '[{"id":"att_first"}]'`, id); n != 2 {
		t.Errorf("%d chat.message events show the image, want the original and its copy", n)
	}
	if n := s.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'chat.message' AND payload->>'text' = 'look'
		AND payload->>'directiveId' = $2 AND payload->>'supersedes' = $3`, next, directive, original); n != 1 {
		t.Errorf("Chat does not show the copy as the original message's turn")
	}
}

// A model swap for a message with an image that fails at its last write,
// session.model.applied (after the old agent is aborted and the fresh one
// is started with the image), leaves the old agent live and untouched and
// no fresh agent, directive, attachment or event.
func TestAFailedModelSwapLeavesTheOldAgentAndNoImage(t *testing.T) {
	s := newSessionWorld(t)
	b := s.withImages()
	gpt := s.tierOn("Sol", "gpt-6-sol")
	id, run, _ := s.liveSession()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": gpt, "harness": nil})
	s.uploadToSession(b, "att_swap", id, "swap.png", screenshot)
	events := s.count(`SELECT count(*) FROM events WHERE session_id = $1`, id)
	s.refuseEvent(delivery.EvSessionModelApplied)
	status, out := s.as(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "look", "attachmentIds": []string{"att_swap"}})
	if status != 500 {
		t.Fatalf("answered %d %v, want 500", status, out)
	}
	if n := s.count(`SELECT count(*) FROM runs WHERE session_id = $1`, id); n != 1 {
		t.Errorf("%d Runs, want only the old agent", n)
	}
	if n := s.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND control = 'none' AND ended_at IS NULL`, run); n != 1 {
		t.Errorf("the old agent was not left live")
	}
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1`, id); n != events {
		t.Errorf("%d session events, want the %d before the message", n, events)
	}
	s.nothingWrittenFor(id, "att_swap")
}

// An answer with an image whose model swap fails at its last write,
// session.model.applied (after the question is settled, question.answered
// written and the fresh agent started with the image), leaves the question
// open, unanswered, and no directive, attachment or event.
func TestAFailedModelSwapForAnAnswerLeavesTheQuestionOpenAndNoImage(t *testing.T) {
	s := newSessionWorld(t)
	b := s.withImages()
	id, run, question := s.asked(`{"question":"Which layout?"}`, false)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": nil, "harness": "codex"})
	s.uploadToSession(b, "att_layout", id, "layout.png", screenshot)
	events := s.count(`SELECT count(*) FROM events WHERE session_id = $1`, id)
	s.refuseEvent(delivery.EvSessionModelApplied)
	status, out := s.as(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "this one", "attachmentIds": []string{"att_layout"}})
	if status != 500 {
		t.Fatalf("answered %d %v, want 500", status, out)
	}
	if n := s.count(`SELECT count(*) FROM questions WHERE id = $1 AND status = 'open' AND answer IS NULL`, question); n != 1 {
		t.Errorf("the question is no longer open")
	}
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'question.answered'`, id); n != 0 {
		t.Errorf("%d question.answered events left behind", n)
	}
	if n := s.count(`SELECT count(*) FROM runs WHERE session_id = $1`, id); n != 1 {
		t.Errorf("%d Runs, want only the agent that asked", n)
	}
	if n := s.count(`SELECT count(*) FROM runs WHERE id = $1 AND ended_at IS NULL AND control = 'none'`, run); n != 1 {
		t.Errorf("the agent that asked was not left live")
	}
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1`, id); n != events {
		t.Errorf("%d session events, want the %d before the answer", n, events)
	}
	s.nothingWrittenFor(id, "att_layout")
}

// A message with images an ended agent never read goes to the session's
// next live agent with its images: Chat shows them on the handed-on
// message, and the next agent's lux input has them.
func TestAnUnreadSessionImageGoesToTheNextLiveAgent(t *testing.T) {
	s := newSessionWorld(t)
	b := s.withImages()
	id, run, _ := s.liveSession()
	s.uploadToSession(b, "att_on", id, "on.png", screenshot)
	mustExec(t, s.owner, `UPDATE runs SET status = 'paused', lux_state = 'stopped' WHERE id = $1`, run)
	from := s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "look", "attachmentIds": []string{"att_on"}})["directiveId"].(string)
	mustExec(t, s.owner, `UPDATE runs SET status = 'completed', ended_at = now(), control = 'none' WHERE id = $1`, run)
	// Words alone start the next agent; the syncer then hands it the unread message.
	next := s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "carry on"})["runId"].(string)
	s.until("the image message handed on", func() bool {
		return s.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'chat.message'
			AND payload->>'supersedes' = $2 AND payload->'attachments'->0->>'id' = 'att_on'`, next, from) == 1
	})
	var luxRun, copy string
	_ = s.owner.QueryRow(context.Background(), `SELECT lux_run_id FROM runs WHERE id = $1`, next).Scan(&luxRun)
	_ = s.owner.QueryRow(context.Background(), `SELECT id FROM directives WHERE supersedes = $1`, from).Scan(&copy)
	s.until("the next agent's lux input to have the image", func() bool {
		_ = s.owner.QueryRow(context.Background(), `SELECT COALESCE(lux_run_id, '') FROM runs WHERE id = $1`, next).Scan(&luxRun)
		return luxRun != "" && len(s.lux.Attachments(luxRun)[copy]) == 1
	})
}

// A message with images that an ended agent never read is not a fresh
// agent's briefing: it fails saying to send it again, as before.
func TestAnUnreadSessionImageIsNotHandedToAFreshAgent(t *testing.T) {
	s := newSessionWorld(t)
	b := s.withImages()
	id, run, _ := s.liveSession()
	s.uploadToSession(b, "att_lost", id, "lost.png", screenshot)
	mustExec(t, s.owner, `UPDATE runs SET status = 'paused', lux_state = 'stopped' WHERE id = $1`, run)
	out := s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "look", "attachmentIds": []string{"att_lost"}})
	directive := out["directiveId"].(string)
	mustExec(t, s.owner, `UPDATE runs SET status = 'completed', ended_at = now(), control = 'none' WHERE id = $1`, run)
	if err := s.app.InOrg(context.Background(), s.org, func(tx pgx.Tx) error {
		_, err := delivery.SessionHandOver(context.Background(), tx, delivery.RunRef{Org: s.org, SessionID: id, RunID: run})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if n := s.count(`SELECT count(*) FROM directives WHERE id = $1 AND error = 'the session''s agent stopped before reading it; its images were not passed on; send it again'`, directive); n != 1 {
		t.Errorf("the image message was not failed as before")
	}
	if next, _ := s.brainstorm(id); next != run {
		t.Errorf("a fresh agent %s was briefed with the image message", next)
	}
}
