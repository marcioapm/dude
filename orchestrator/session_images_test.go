package orchestrator_test

import (
	"bytes"
	"context"
	"testing"

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

// With no live agent the message would be a fresh agent's briefing, which
// carries no images: it is refused, nothing starts, and the image stays
// unsent for the next message.
func TestASessionMessageWithImagesStartsNoAgent(t *testing.T) {
	s := newSessionWorld(t)
	b := s.withImages()
	id := s.session()
	s.uploadToSession(b, "att_first", id, "first.png", screenshot)
	status, body := s.as(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "look", "attachmentIds": []string{"att_first"}})
	if e, _ := body["error"].(map[string]any); status != 409 || e["code"] != "images_need_agent" {
		t.Fatalf("a first message with an image: %d %v", status, body)
	}
	if run, _ := s.brainstorm(id); run != "" {
		t.Errorf("an agent was started: %s", run)
	}
	if n := s.count(`SELECT count(*) FROM attachments WHERE id = 'att_first' AND attached_at IS NULL`); n != 1 {
		t.Errorf("the refused image was attached")
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

// After a model change a message with images stays with the agent that is
// running, since a fresh one would read it in its briefing; the next
// message without images swaps.
func TestASessionMessageWithImagesDefersTheModelSwap(t *testing.T) {
	s := newSessionWorld(t)
	b := s.withImages()
	gpt := s.tierOn("Sol", "gpt-6-sol")
	id, run, luxRun := s.liveSession()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/model", map[string]any{"tier": gpt, "harness": nil})
	s.uploadToSession(b, "att_swap", id, "swap.png", screenshot)
	out := s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "look", "attachmentIds": []string{"att_swap"}})
	s.queuedOn(out, run)
	s.noSwap(id)
	s.until("lux to have the image", func() bool { return len(s.lux.Attachments(luxRun)[out["directiveId"].(string)]) == 1 })
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
	// Paused by a person: the directive waits unsent.
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
