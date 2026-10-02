package orchestrator_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/objects"
)

// bucket is a photo bucket in memory.
type bucket map[string][]byte

func (b bucket) Get(_ context.Context, key string, _ int64) ([]byte, error) {
	if v, ok := b[key]; ok {
		return v, nil
	}
	return nil, objects.ErrNotFound
}

var screenshot = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{7}, 64)...)

// withImages gives the world a bucket and returns it.
func (w *world) withImages() bucket {
	b := bucket{}
	w.syncer.Objects = b
	return b
}

// upload records an image uploaded to a task, as the backend does.
func (w *world) upload(b bucket, id, task, name string, data []byte) {
	key := "attachments/" + id
	b[key] = data
	mustExec(w.t, w.owner, `INSERT INTO attachments (id, organization_id, task_id, name, content_type, width, height, bytes,
		sha256, object_key, original_content_type, original_width, original_height, original_bytes, original_key)
		VALUES ($1, $2, $3, $4, 'image/png', 10, 10, $5, 'x', $6, 'image/png', 20, 20, $5, $6||'.o')`,
		id, w.org, task, name, len(data), key)
}

// The task's first prompt carries its images as workload.attachments, to
// its first agent only.
func TestATaskStartedWithAnImageGivesItToItsFirstAgent(t *testing.T) {
	w := newWorld(t)
	b := w.withImages()
	wi := w.task()
	w.upload(b, "att_design", wi, "design.png", screenshot)
	if status, body := w.call("/internal/tasks/"+wi+"/deliver", map[string]any{"attachmentIds": []string{"att_design"}}); status != 201 {
		t.Fatalf("deliver: %d %v", status, body)
	}
	w.until("the implementer to be submitted", func() bool { return len(w.lux.Runs()) >= 1 })
	var spec struct {
		Workload struct {
			Attachments []struct {
				Name, ContentType string
				Data              []byte
			} `json:"attachments"`
		} `json:"workload"`
	}
	if err := json.Unmarshal(w.lux.Runs()[0].Spec, &spec); err != nil {
		t.Fatal(err)
	}
	got := spec.Workload.Attachments
	if len(got) != 1 || got[0].Name != "design.png" || got[0].ContentType != "image/png" || !bytes.Equal(got[0].Data, screenshot) {
		t.Fatalf("the first agent's spec carried %+v", got)
	}
	// The transcript's prompt turn names it.
	w.until("the prompt to reach the ledger", func() bool {
		return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'agent.prompt.delivered'
			AND payload->'attachments'->0->>'id' = 'att_design'`, wi) == 1
	})
	// The reviewer after it is asked about the work, and not shown them.
	w.until("a second phase", func() bool { return len(w.lux.Runs()) >= 2 })
	if !strings.Contains(string(w.lux.Runs()[1].Spec), `"dude.phase":"review"`) {
		t.Fatalf("the second Run is not the reviewer: %s", w.lux.Runs()[1].Spec)
	}
	if got := promptImages(t, w.lux.Runs()[1].Spec); len(got) != 0 {
		t.Errorf("the reviewer was given the prompt's images: %v", got)
	}
	// Once given, the prompt takes no more.
	w.upload(b, "att_late", wi, "late.png", screenshot)
	if status, _ := w.call("/internal/tasks/"+wi+"/deliver", map[string]any{"attachmentIds": []string{"att_late"}}); status != 409 {
		t.Errorf("images were added to a prompt already given: %d", status)
	}
}

// A steer names only its own task's unsent images; anything else is
// refused before anything is queued.
func TestASteerRefusesImagesThatAreNotItsTasksToSend(t *testing.T) {
	w := newWorld(t)
	b := w.withImages()
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	wi, other := w.task(), w.task()
	w.deliver(wi)
	w.until("the implementer to run", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'running'`, wi) == 1
	})
	var runID string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1`, wi).Scan(&runID)
	w.upload(b, "att_mine", wi, "mine.png", screenshot)
	w.upload(b, "att_theirs", other, "theirs.png", screenshot)
	// Two that are 5 MiB together and one more byte (the row's size is
	// what counts, so the bucket holds a small stand-in).
	w.uploadSized(b, "att_big1", wi, 3<<20)
	w.uploadSized(b, "att_big2", wi, 2<<20+1)
	// Another organization's, on its own task: not visible here at all.
	foreign := dbtest.Org(t, w.owner)
	mustExec(t, w.owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_'||$1, $1, 'F', 'prj_'||$1, 'F')`, foreign)
	mustExec(t, w.owner, `INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_'||$1, $1, 'prj_'||$1, 1, 'F', 'F')`, foreign)
	mustExec(t, w.owner, `INSERT INTO attachments (id, organization_id, task_id, name, content_type, width, height, bytes,
		sha256, object_key, original_content_type, original_width, original_height, original_bytes, original_key)
		VALUES ('att_foreign', $1, 'wi_'||$1, 'f.png', 'image/png', 1, 1, 1, 'x', 'k', 'image/png', 1, 1, 1, 'k.o')`, foreign)

	for _, c := range []struct {
		ids  []string
		want string
	}{
		{[]string{"att_theirs"}, "image att_theirs is not one of this task's"},
		{[]string{"att_foreign"}, "image att_foreign is not one of this task's"},
		{[]string{"att_nowhere"}, "image att_nowhere is not one of this task's"},
		{[]string{"att_mine", "att_mine"}, "image att_mine is named twice"},
		{[]string{"a", "b", "c", "d", "e", "f", "g"}, "a message carries at most 6 images"},
		{[]string{"att_big1", "att_big2"}, "a message's images are at most 5 MiB together"},
	} {
		status, body := w.call("/internal/runs/"+runID+"/steer", map[string]any{"text": "see", "attachmentIds": c.ids})
		if msg := errorMessage(body); status != 400 || msg != c.want {
			t.Errorf("%v: %d %q, want 400 %q", c.ids, status, msg, c.want)
		}
	}
	if n := w.count(`SELECT count(*) FROM directives WHERE run_id = $1`, runID); n != 0 {
		t.Fatalf("a refused steer queued %d directives", n)
	}

	status, body := w.call("/internal/runs/"+runID+"/steer", map[string]any{"text": "", "attachmentIds": []string{"att_mine"}})
	if status != 201 {
		t.Fatalf("an image alone: %d %v", status, body)
	}
	if atts, _ := body["attachments"].([]any); len(atts) != 1 {
		t.Errorf("the steer answered %v", body["attachments"])
	}
	// Sent once: a second message cannot take it.
	if status, _ := w.call("/internal/runs/"+runID+"/steer", map[string]any{"text": "again", "attachmentIds": []string{"att_mine"}}); status != 400 {
		t.Errorf("an image already sent was sent again: %d", status)
	}
	w.until("lux to have the image", func() bool { return len(w.lux.Attachments(w.lux.Runs()[0].ID)) == 1 })
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.steered'
		AND payload->'attachments'->0->>'name' = 'mine.png'`, runID); n != 1 {
		t.Errorf("run.steered does not carry the image: %d", n)
	}
	if n := w.count(`SELECT count(*) FROM attachments WHERE id = 'att_foreign' AND attached_at IS NULL`); n != 1 {
		t.Error("another organization's image was attached")
	}

	// Six that are exactly 5 MiB together are a message.
	var six []string
	for i := range 6 {
		size := (5 << 20) / 6
		if i == 0 {
			size += (5 << 20) % 6
		}
		id := fmt.Sprintf("att_six%d", i)
		w.uploadSized(b, id, wi, size)
		six = append(six, id)
	}
	if status, body := w.call("/internal/runs/"+runID+"/steer", map[string]any{"text": "all six", "attachmentIds": six}); status != 201 {
		t.Fatalf("six images at 5 MiB together: %d %v", status, body)
	}
}

// uploadSized records an upload whose row says it is size bytes; the bucket
// holds a small PNG for it.
func (w *world) uploadSized(b bucket, id, task string, size int) {
	w.t.Helper()
	key := "attachments/" + id
	b[key] = screenshot
	mustExec(w.t, w.owner, `INSERT INTO attachments (id, organization_id, task_id, name, content_type, width, height, bytes,
		sha256, object_key, original_content_type, original_width, original_height, original_bytes, original_key)
		VALUES ($1, $2, $3, $1||'.png', 'image/png', 10, 10, $4, 'x', $5, 'image/png', 20, 20, $4, $5||'.o')`,
		id, w.org, task, size, key)
}

// An answer may be an image alone: it reaches the agent with the question
// it answers.
func TestAnAnswerMayBeAnImageAlone(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	b := w.withImages()
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] != "implement" {
			return fakelux.Behaviour{Hang: true}
		}
		return fakelux.Behaviour{Ask: `{"question":"Which layout?"}`, Reply: "Done.",
			Commit: map[string]string{"a.md": "x\n"}, Message: "work"}
	}
	wi := w.task()
	w.deliver(wi)
	w.until("the question", func() bool { return w.taskStatus(wi) == "awaiting_input" })
	var qid string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM questions WHERE task_id = $1`, wi).Scan(&qid)
	w.upload(b, "att_layout", wi, "layout.png", screenshot)
	if status, body := w.call("/internal/questions/"+qid+"/answer", map[string]any{"text": " "}); status != 400 || errorMessage(body) != "text or an image is required" {
		t.Errorf("an empty answer: %d %v", status, body)
	}
	if status, body := w.call("/internal/questions/"+qid+"/answer", map[string]any{"text": "", "attachmentIds": []string{"att_layout"}}); status != 200 {
		t.Fatalf("answer: %d %v", status, body)
	}
	w.until("the implementer to finish", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement' AND status = 'completed'`, wi) == 1
	})
	var directive string
	_ = w.owner.QueryRow(context.Background(), `SELECT directive_id FROM attachments WHERE id = 'att_layout'`).Scan(&directive)
	if got := w.lux.Attachments(w.lux.Runs()[0].ID)[directive]; len(got) != 1 || got[0].Name != "layout.png" {
		t.Fatalf("the agent got %+v with the answer", got)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'question.answered'
		AND payload->'attachments'->0->>'id' = 'att_layout'`, wi); n != 1 {
		t.Errorf("question.answered does not carry the image")
	}
}

// An answer is a directive like a steer: its images reach the agent with it.
func TestAnAnswerCarriesItsImagesToTheAgent(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	b := w.withImages()
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] != "implement" {
			return fakelux.Behaviour{Hang: true}
		}
		return fakelux.Behaviour{Ask: `{"question":"Does it overflow on a phone?"}`, Reply: "Fixed it.",
			Commit: map[string]string{"a.md": "x\n"}, Message: "work"}
	}
	wi := w.task()
	w.deliver(wi)
	w.until("the question", func() bool { return w.taskStatus(wi) == "awaiting_input" })
	var qid string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM questions WHERE task_id = $1`, wi).Scan(&qid)
	w.upload(b, "att_phone", wi, "phone.png", screenshot)
	if status, body := w.call("/internal/questions/"+qid+"/answer", map[string]any{"text": "It overflows", "attachmentIds": []string{"att_phone"}}); status != 200 {
		t.Fatalf("answer: %d %v", status, body)
	}
	w.until("the implementer to finish", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement' AND status = 'completed'`, wi) == 1
	})
	var directive string
	_ = w.owner.QueryRow(context.Background(), `SELECT directive_id FROM attachments WHERE id = 'att_phone'`).Scan(&directive)
	got := w.lux.Attachments(w.lux.Runs()[0].ID)[directive]
	if len(got) != 1 || got[0].Name != "phone.png" || !bytes.Equal(got[0].Data, screenshot) {
		t.Fatalf("the agent got %+v with the answer", got)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'question.answered'
		AND payload->'attachments'->0->>'id' = 'att_phone'`, wi); n != 1 {
		t.Errorf("question.answered does not carry the image")
	}
}

// promptImages are the images a lux Run's spec carried with its prompt.
func promptImages(t *testing.T, spec []byte) []string {
	t.Helper()
	var s struct {
		Workload struct {
			Attachments []struct {
				Name string
				Data []byte
			} `json:"attachments"`
		} `json:"workload"`
	}
	if err := json.Unmarshal(spec, &s); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, a := range s.Workload.Attachments {
		names = append(names, a.Name)
	}
	return names
}

// The task's images go with every Run given the task as its prompt: an
// implementer that failed before its agent saw them is retried with them.
func TestARetriedImplementerIsGivenTheTasksImages(t *testing.T) {
	w := newWorld(t)
	b := w.withImages()
	wi := w.task()
	w.upload(b, "att_design", wi, "design.png", screenshot)
	// Missing the first time: the first implementer fails at submit.
	delete(b, "attachments/att_design")
	if status, body := w.call("/internal/tasks/"+wi+"/deliver", map[string]any{"attachmentIds": []string{"att_design"}}); status != 201 {
		t.Fatalf("deliver: %d %v", status, body)
	}
	w.until("delivery to stop for a person", func() bool { return w.taskStatus(wi) == "awaiting_input" })
	var reason string
	_ = w.owner.QueryRow(context.Background(), `SELECT error FROM runs WHERE task_id = $1 AND phase = 'implement'`, wi).Scan(&reason)
	if reason != "the task's image design.png is gone from storage" {
		t.Fatalf("the first implementer failed with %q", reason)
	}
	if n := len(w.lux.Runs()); n != 0 {
		t.Fatalf("lux was given %d Runs", n)
	}

	b["attachments/att_design"] = screenshot
	if status, body := w.call("/internal/tasks/"+wi+"/decide", map[string]any{"action": "retry"}); status != 200 {
		t.Fatalf("retry: %d %v", status, body)
	}
	w.until("the second implementer to be submitted", func() bool { return len(w.lux.Runs()) >= 1 })
	if got := promptImages(t, w.lux.Runs()[0].Spec); !slices.Equal(got, []string{"design.png"}) {
		t.Fatalf("the retried implementer was given %v", got)
	}
	w.until("its prompt turn to name the image", func() bool {
		return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'agent.prompt.delivered'
			AND payload->'attachments'->0->>'id' = 'att_design'`, wi) == 1
	})
}

// A delivery whose workflow could not start is asked again with the same
// images: they are already the prompt's, and it goes.
func TestADeliveryAskedAgainKeepsItsImages(t *testing.T) {
	w := newWorld(t)
	b := w.withImages()
	wi := w.task()
	w.upload(b, "att_design", wi, "design.png", screenshot)
	// The workflow cannot start, for this organization only.
	mustExec(t, w.owner, fmt.Sprintf(`CREATE FUNCTION refuse_%[1]s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'workflow store down'; END $$;
		CREATE TRIGGER refuse_%[1]s BEFORE INSERT ON workflow_runs FOR EACH ROW
		WHEN (NEW.organization_id = '%[1]s') EXECUTE FUNCTION refuse_%[1]s()`, w.org))
	if status, body := w.call("/internal/tasks/"+wi+"/deliver", map[string]any{"attachmentIds": []string{"att_design"}}); status != 500 {
		t.Fatalf("deliver with the workflow store down: %d %v", status, body)
	}
	mustExec(t, w.owner, fmt.Sprintf(`DROP TRIGGER refuse_%[1]s ON workflow_runs; DROP FUNCTION refuse_%[1]s()`, w.org))

	if status, body := w.call("/internal/tasks/"+wi+"/deliver", map[string]any{"attachmentIds": []string{"att_design"}}); status != 201 {
		t.Fatalf("deliver again: %d %v", status, body)
	}
	w.until("the implementer to be submitted", func() bool { return len(w.lux.Runs()) >= 1 })
	if got := promptImages(t, w.lux.Runs()[0].Spec); !slices.Equal(got, []string{"design.png"}) {
		t.Fatalf("the implementer was given %v", got)
	}
	// It was sent, with the prompt: a steer cannot take it.
	if n := w.count(`SELECT count(*) FROM attachments WHERE id = 'att_design' AND for_prompt AND attached_at IS NOT NULL`); n != 1 {
		t.Errorf("the image is not the prompt's")
	}
}

// hangingRun delivers a task whose implementer runs until stopped, and
// returns the task and its Run.
func (w *world) hangingRun() (task, run string) {
	w.t.Helper()
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	task = w.task()
	w.deliver(task)
	w.until("the implementer to run", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'running' AND lux_state = 'running'`, task) == 1
	})
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1`, task).Scan(&run)
	return task, run
}

// errorMessage is the message of an API refusal.
func errorMessage(body map[string]any) string {
	e, _ := body["error"].(map[string]any)
	msg, _ := e["message"].(string)
	return msg
}

// Retry on a steer that was an image and no words sends the image again:
// the retry has no words of its own, and needs none.
func TestRetryingAnImageOnlySteerSendsItsImage(t *testing.T) {
	w := newWorld(t)
	b := w.withImages()
	wi, runID := w.hangingRun()
	w.upload(b, "att_only", wi, "only.png", screenshot)
	// Gone from storage the first time: the steer fails.
	delete(b, "attachments/att_only")
	status, body := w.call("/internal/runs/"+runID+"/steer", map[string]any{"text": "", "attachmentIds": []string{"att_only"}})
	if status != 201 {
		t.Fatalf("steer: %d %v", status, body)
	}
	first, _ := body["id"].(string)
	w.until("the steer to fail", func() bool {
		return w.count(`SELECT count(*) FROM directives WHERE id = $1 AND failed_at IS NOT NULL`, first) == 1
	})
	b["attachments/att_only"] = screenshot

	status, body = w.call("/internal/runs/"+runID+"/steer", map[string]any{"text": "", "supersedes": first})
	if status != 201 {
		t.Fatalf("retry: %d %v", status, body)
	}
	retry, _ := body["id"].(string)
	w.until("lux to have the image", func() bool { return len(w.lux.Attachments(w.lux.Runs()[0].ID)[retry]) == 1 })
	if got := w.lux.Attachments(w.lux.Runs()[0].ID)[retry]; got[0].Name != "only.png" || !bytes.Equal(got[0].Data, screenshot) {
		t.Errorf("the retry carried %+v", got)
	}

	// Nothing to repeat and nothing new: refused.
	for _, c := range []map[string]any{{"text": ""}, {"text": " ", "supersedes": "dir_nowhere"}} {
		if status, body := w.call("/internal/runs/"+runID+"/steer", c); status != 400 || errorMessage(body) != "text or an image is required" {
			t.Errorf("%v: %d %v", c, status, body)
		}
	}
}

// A message sent again (the same words, superseding it) carries the
// images it had; new images with it would be attached and never sent, so
// the request is refused before anything is queued.
func TestASteerSentAgainCannotBringNewImages(t *testing.T) {
	w := newWorld(t)
	b := w.withImages()
	wi, runID := w.hangingRun()
	w.upload(b, "att_new", wi, "new.png", screenshot)
	status, body := w.call("/internal/runs/"+runID+"/steer", map[string]any{"text": "look at the header"})
	if status != 201 {
		t.Fatalf("steer: %d %v", status, body)
	}
	first, _ := body["id"].(string)
	w.until("the steer to be sent", func() bool {
		return w.count(`SELECT count(*) FROM directives WHERE id = $1 AND sent_at IS NOT NULL`, first) == 1
	})
	status, body = w.call("/internal/runs/"+runID+"/steer", map[string]any{"text": "look at the header", "supersedes": first,
		"interrupt": true, "attachmentIds": []string{"att_new"}})
	if want := "a message sent again carries the images it had: send new images in a new message"; status != 400 || errorMessage(body) != want {
		t.Fatalf("%d %v, want 400 %q", status, body, want)
	}
	if n := w.count(`SELECT count(*) FROM directives WHERE run_id = $1`, runID); n != 1 {
		t.Errorf("the refused request queued a directive: %d", n)
	}
	if n := w.count(`SELECT count(*) FROM attachments WHERE id = 'att_new' AND attached_at IS NULL`); n != 1 {
		t.Error("the refused request attached the image")
	}
	// New words with new images are a new message, and go.
	if status, body := w.call("/internal/runs/"+runID+"/steer", map[string]any{"text": "and this", "supersedes": first,
		"attachmentIds": []string{"att_new"}}); status != 201 {
		t.Fatalf("new words: %d %v", status, body)
	}
}
