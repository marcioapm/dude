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
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
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

// describe sets a task's goal and criteria and attaches the images they
// reference, as the backend's save does (syncTaskAttachments).
func (w *world) describe(task, goal string, criteria ...string) {
	w.t.Helper()
	raw, _ := json.Marshal(criteria)
	mustExec(w.t, w.owner, `UPDATE tasks SET goal = $2, acceptance_criteria = $3::jsonb WHERE id = $1`, task, goal, string(raw))
	mustExec(w.t, w.owner, `UPDATE attachments SET for_prompt = false, attached_at = NULL WHERE task_id = $1 AND for_prompt`, task)
	mustExec(w.t, w.owner, `UPDATE attachments SET for_prompt = true, attached_at = now() WHERE task_id = $1 AND id = ANY($2)`,
		task, delivery.TaskImageIDs(goal, criteria))
}

// specImages are the images a lux Run's spec carried with its prompt, with
// their bytes.
func specImages(t *testing.T, spec []byte) []struct {
	Name, ContentType string
	Data              []byte
} {
	t.Helper()
	var s struct {
		Workload struct {
			Attachments []struct {
				Name, ContentType string
				Data              []byte
			} `json:"attachments"`
		} `json:"workload"`
	}
	if err := json.Unmarshal(spec, &s); err != nil {
		t.Fatal(err)
	}
	return s.Workload.Attachments
}

// The images a task's text shows go with its first prompt as
// workload.attachments, the goal's then the criteria's, each once, in
// order of first appearance; the prompt names each by its place there.
func TestATaskStartedWithImagesInItsTextGivesThemToItsFirstAgent(t *testing.T) {
	w := newWorld(t)
	b := w.withImages()
	// A model of its own: the scripted agent's prompt is its script.
	w.onModel("implementer", "llm-impl")
	wi := w.task()
	w.upload(b, "att_header", wi, "header.png", screenshot)
	w.upload(b, "att_mock", wi, "mock.png", append([]byte("\x89PNG\r\n\x1a\n"), 1, 2, 3))
	w.upload(b, "att_after", wi, "after.png", screenshot)
	w.describe(wi, "The header ![header.png](attachment:att_header) overlaps the menu.\n\n"+
		"It should look like ![mock.png](attachment:att_mock), and ![header.png](attachment:att_header) again.",
		"Matches ![mock.png](attachment:att_mock)", "No overlap, as ![after.png](attachment:att_after) shows")
	if status, body := w.call("/internal/tasks/"+wi+"/deliver", map[string]any{}); status != 201 {
		t.Fatalf("deliver: %d %v", status, body)
	}
	w.until("the implementer to be submitted", func() bool { return len(w.lux.Runs()) >= 1 })
	got := specImages(t, w.lux.Runs()[0].Spec)
	var names []string
	for _, a := range got {
		names = append(names, a.Name)
	}
	if want := []string{"header.png", "mock.png", "after.png"}; !slices.Equal(names, want) {
		t.Fatalf("the first agent's spec carried %v, want %v", names, want)
	}
	if got[0].ContentType != "image/png" || !bytes.Equal(got[0].Data, screenshot) {
		t.Errorf("the first image is %+v", got[0])
	}
	prompt := w.lux.Runs()[0].Prompt()
	for _, want := range []string{
		"The header [Image 1: header.png] overlaps the menu.\n\nIt should look like [Image 2: mock.png], and [Image 1: header.png] again.",
		"- Matches [Image 2: mock.png]\n- No overlap, as [Image 3: after.png] shows",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt lacks %q:\n%s", want, prompt)
		}
	}
	// The transcript's prompt turn names them, in the same order.
	w.until("the prompt to reach the ledger", func() bool {
		return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'agent.prompt.delivered'
			AND payload->'attachments'->0->>'id' = 'att_header' AND payload->'attachments'->2->>'id' = 'att_after'`, wi) == 1
	})
}

// A reviewer checks the work against the goal and criteria: its Run is
// given the images they show, numbered as the implementer's were.
func TestAReviewerIsGivenTheTasksImages(t *testing.T) {
	w := newWorld(t)
	b := w.withImages()
	w.onModel("reviewer", "llm-review")
	wi := w.task()
	w.upload(b, "att_goal", wi, "goal.png", screenshot)
	w.upload(b, "att_crit", wi, "evidence.png", screenshot)
	w.describe(wi, "Make the page look right: ![goal.png](attachment:att_goal)", "Matches ![evidence.png](attachment:att_crit)")
	w.deliver(wi)
	w.until("a reviewer", func() bool {
		for _, r := range w.lux.Runs() {
			if strings.Contains(string(r.Spec), `"dude.phase":"review"`) {
				return true
			}
		}
		return false
	})
	for _, r := range w.lux.Runs() {
		if !strings.Contains(string(r.Spec), `"dude.phase":"review"`) {
			continue
		}
		if got := promptImages(t, r.Spec); !slices.Equal(got, []string{"goal.png", "evidence.png"}) {
			t.Errorf("the reviewer was given %v", got)
		}
		if p := r.Prompt(); !strings.Contains(p, "- Matches [Image 2: evidence.png]") || !strings.Contains(p, "[Image 1: goal.png]") {
			t.Errorf("the reviewer's prompt:\n%s", p)
		}
	}
}

// A reference to an image that is not the task's to show — another
// task's, or one never attached — is no error: the agent reads that it is
// unavailable, and is given none.
func TestAReferenceToAnImageNotTheTasksReadsAsUnavailable(t *testing.T) {
	w := newWorld(t)
	b := w.withImages()
	w.onModel("implementer", "llm-impl")
	wi, other := w.task(), w.task()
	w.upload(b, "att_theirs", other, "theirs.png", screenshot)
	w.describe(other, "Theirs: ![theirs.png](attachment:att_theirs)")
	w.describe(wi, "See ![theirs.png](attachment:att_theirs) and ![gone.png](attachment:att_gone).")
	w.deliver(wi)
	w.until("the implementer to be submitted", func() bool { return len(w.lux.Runs()) >= 1 })
	if got := promptImages(t, w.lux.Runs()[0].Spec); len(got) != 0 {
		t.Errorf("the agent was given %v", got)
	}
	if p := w.lux.Runs()[0].Prompt(); !strings.Contains(p, "See [Image unavailable: theirs.png] and [Image unavailable: gone.png].") {
		t.Errorf("the prompt:\n%s", p)
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
	w.upload(b, id, task, id+".png", screenshot)
	mustExec(w.t, w.owner, `UPDATE attachments SET bytes = $2, original_bytes = $2 WHERE id = $1`, id, size)
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

// The task's images go with every Run given the task: an implementer that
// failed before its agent saw them is retried with them.
func TestARetriedImplementerIsGivenTheTasksImages(t *testing.T) {
	w := newWorld(t)
	b := w.withImages()
	wi := w.task()
	w.upload(b, "att_design", wi, "design.png", screenshot)
	w.describe(wi, "Build ![design.png](attachment:att_design)")
	// Missing the first time: the first implementer fails at submit.
	delete(b, "attachments/att_design")
	if status, body := w.call("/internal/tasks/"+wi+"/deliver", map[string]any{}); status != 201 {
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

// An image the task's text shows is the prompt's: a steer cannot take it.
func TestASteerCannotTakeTheTasksImage(t *testing.T) {
	w := newWorld(t)
	b := w.withImages()
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	wi := w.task()
	w.upload(b, "att_design", wi, "design.png", screenshot)
	w.describe(wi, "Build ![design.png](attachment:att_design)")
	w.deliver(wi)
	var runID string
	w.until("the implementer to run", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND status = 'running'`, wi).Scan(&runID)
		return runID != ""
	})
	status, body := w.call("/internal/runs/"+runID+"/steer", map[string]any{"text": "x", "attachmentIds": []string{"att_design"}})
	if want := "image att_design was already sent"; status != 400 || errorMessage(body) != want {
		t.Errorf("a steer naming the prompt's image: %d %q, want 400 %q", status, errorMessage(body), want)
	}
	if n := w.count(`SELECT count(*) FROM attachments WHERE id = 'att_design' AND for_prompt AND directive_id IS NULL`); n != 1 {
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

// An image steer sent to a paused Run waits for it: the resume carries
// neither its words nor a nudge (a resume has no images), and once the
// Run runs again the steer goes through /input with its image.
func TestAnImageSteerToAPausedRunGoesAfterTheResume(t *testing.T) {
	w := newWorld(t)
	b := w.withImages()
	wi, runID := w.hangingRun()
	if status, body := w.call("/internal/runs/"+runID+"/pause", map[string]any{}); status != 200 {
		t.Fatalf("pause: %d %v", status, body)
	}
	w.until("the pause", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, runID) == 1
	})
	w.upload(b, "att_paused", wi, "paused.png", screenshot)
	status, body := w.call("/internal/runs/"+runID+"/steer", map[string]any{"text": "look at this", "attachmentIds": []string{"att_paused"}})
	if status != 201 {
		t.Fatalf("steer: %d %v", status, body)
	}
	dir, _ := body["id"].(string)
	for range 5 {
		w.pump()
	}
	lr := w.lux.Runs()[0]
	if n := w.count(`SELECT count(*) FROM directives WHERE id = $1 AND sent_at IS NULL`, dir); n != 1 || len(w.lux.Attachments(lr.ID)[dir]) != 0 {
		t.Fatal("a steer was sent to a paused Run")
	}

	if status, body := w.call("/internal/runs/"+runID+"/resume", map[string]any{}); status != 200 {
		t.Fatalf("resume: %d %v", status, body)
	}
	w.until("the image to reach lux", func() bool { return len(w.lux.Attachments(lr.ID)[dir]) == 1 })
	if !slices.Contains(w.lux.CallsOf(lr.ID), "resume") {
		t.Errorf("lux was asked %v, no resume", w.lux.CallsOf(lr.ID))
	}
	if got := w.lux.ResumeInputs(lr.ID); !slices.Equal(got, []string{""}) {
		t.Errorf("the resume carried input %q", got)
	}
	if got := w.lux.Attachments(lr.ID)[dir]; got[0].Name != "paused.png" || !bytes.Equal(got[0].Data, screenshot) {
		t.Errorf("the steer carried %+v", got)
	}
	if bodies := w.lux.InputBodies(lr.ID, dir); len(bodies) != 1 || !strings.Contains(bodies[0], "look at this") {
		t.Errorf("/input for the steer got %q", bodies)
	}
}
