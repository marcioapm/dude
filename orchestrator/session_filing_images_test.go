package orchestrator_test

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// sharedImage is an image a member sent the session's live agent: an
// upload of the session, attached to the message's directive.
func (s *sessionWorld) sharedImage(b bucket, session, id, name string) {
	s.t.Helper()
	s.uploadToSession(b, id, session, name, screenshot)
	s.ok(s.marcio, "POST", "/internal/sessions/"+session+"/chat", map[string]any{"text": "this is the design", "attachmentIds": []string{id}})
}

// linkedLiveSession is liveSession with the world's project linked, as
// propose needs for a task item.
func (s *sessionWorld) linkedLiveSession() (session, run, luxRun string) {
	s.t.Helper()
	session, run, luxRun = s.liveSession()
	mustExec(s.t, s.owner, `INSERT INTO session_projects (session_id, project_id, organization_id) VALUES ($1, $2, $3)`,
		session, s.project, s.org)
	return session, run, luxRun
}

// sentIn marks a session's upload sent with a message of that session's
// agent, as though a member had sent it there.
func (s *sessionWorld) sentIn(session, att string) {
	s.t.Helper()
	mustExec(s.t, s.owner, `INSERT INTO runs (id, organization_id, session_id, attempt, status, kind, role)
		VALUES ('run_sent_'||$1, $2, $1, 1, 'completed', 'agent', 'brainstorm') ON CONFLICT DO NOTHING`, session, s.org)
	mustExec(s.t, s.owner, `INSERT INTO directives (id, organization_id, run_id, text) VALUES ('dir_'||$1, $2, 'run_sent_'||$3, 'see')`,
		att, s.org, session)
	mustExec(s.t, s.owner, `UPDATE attachments SET directive_id = 'dir_'||id, attached_at = now() WHERE id = $1`, att)
}

// A task item names images a member shared in its session; filed, each is
// an upload of the new task on the same stored object, its goal shows them
// as a person's image in the task's text, and the task's agents are given
// them with the task.
func TestAFiledTaskCarriesTheImagesSharedInItsSession(t *testing.T) {
	s := newSessionWorld(t)
	s.withTools()
	b := s.withImages()
	id, run, luxRun := s.linkedLiveSession()
	s.sharedImage(b, id, "att_design", "design.png")
	directive := ""
	_ = s.owner.QueryRow(context.Background(), `SELECT directive_id FROM attachments WHERE id = 'att_design'`).Scan(&directive)
	// The agent is told the image's id with the message, to name it by.
	s.until("the agent's input names the image's id", func() bool {
		return slices.ContainsFunc(s.lux.Sent(luxRun), func(in string) bool { return strings.Contains(in, "design.png (att_design)") })
	})

	status, out := s.tool(run, "propose", `{"items":[{"kind":"task","project":"BL","title":"Dropdown checklist",
		"goal":"A dropdown with a checklist of the metrics, as in the design","images":["att_design"]}]}`)
	if status != 200 {
		t.Fatalf("propose with images: %d %v", status, out)
	}
	proposal := out["proposalId"].(string)
	// The card carries the image for the web to show.
	card := s.ok(s.marcio, "GET", "/internal/sessions/"+id, nil)
	raw, _ := json.Marshal(card["proposals"])
	var cards []struct {
		Items       []delivery.ProposalItem
		Attachments map[string]struct{ Name string }
	}
	_ = json.Unmarshal(raw, &cards)
	if len(cards) != 1 || !slices.Equal(cards[0].Items[0].Images, []string{"att_design"}) || cards[0].Attachments["att_design"].Name != "design.png" {
		t.Fatalf("the card: %s", raw)
	}

	filed := s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/file", map[string]any{"proposalId": proposal, "items": []int{0}})
	if r := filed["results"].([]any)[0].(map[string]any); r["status"] != "filed" {
		t.Fatalf("filing: %v", filed)
	}
	var task, goal, criteria string
	if err := s.owner.QueryRow(context.Background(), `SELECT id, goal, acceptance_criteria::text FROM tasks
		WHERE organization_id = $1 AND title = 'Dropdown checklist'`, s.org).Scan(&task, &goal, &criteria); err != nil {
		t.Fatal(err)
	}
	// The task's own upload: another row, the same objects, attached to its text.
	var copyID, objectKey, originalKey string
	var forPrompt bool
	if err := s.owner.QueryRow(context.Background(), `SELECT id, object_key, original_key, for_prompt AND attached_at IS NOT NULL
		FROM attachments WHERE task_id = $1`, task).Scan(&copyID, &objectKey, &originalKey, &forPrompt); err != nil {
		t.Fatalf("the task has no upload of its own: %v", err)
	}
	if copyID == "att_design" || objectKey != "attachments/att_design" || originalKey != "attachments/att_design.o" || !forPrompt {
		t.Errorf("the task's upload %s: %s %s, attached to its text %v", copyID, objectKey, originalKey, forPrompt)
	}
	if n := s.count(`SELECT count(*) FROM attachments WHERE id = 'att_design' AND session_id = $1 AND directive_id = $2`, id, directive); n != 1 {
		t.Errorf("the session's image is no longer its message's")
	}
	if refs := delivery.ImageRefs(goal); len(refs) != 1 || refs[0].ID != copyID || refs[0].Alt != "design.png" {
		t.Errorf("the goal's images %+v, want the task's %s:\n%s", refs, copyID, goal)
	}
	if !strings.HasPrefix(goal, "A dropdown with a checklist of the metrics, as in the design\n\n") {
		t.Errorf("the goal lost its words:\n%s", goal)
	}
	var images []delivery.SentAttachment
	if err := s.app.InOrg(context.Background(), s.org, func(tx pgx.Tx) error {
		var err error
		images, err = delivery.TaskImages(context.Background(), tx, task, goal, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 || images[0].ID != copyID || images[0].Name != "design.png" {
		t.Fatalf("the task's images: %+v", images)
	}

	// The implementer is given the image's bytes with its prompt (a real
	// model's implementer is given the composed prompt).
	s.onModel("implementer", "llm-impl")
	s.deliver(task)
	s.until("the implementer submitted", func() bool {
		for _, r := range s.lux.Runs() {
			if strings.Contains(string(r.Spec), `"dude.task":"`+task+`"`) {
				return true
			}
		}
		return false
	})
	for _, r := range s.lux.Runs() {
		if !strings.Contains(string(r.Spec), `"dude.task":"`+task+`"`) {
			continue
		}
		var spec struct {
			Workload struct {
				Attachments []struct {
					Name string
					Data []byte
				}
			}
		}
		_ = json.Unmarshal(r.Spec, &spec)
		if a := spec.Workload.Attachments; len(a) != 1 || a[0].Name != "design.png" || !bytes.Equal(a[0].Data, screenshot) {
			t.Errorf("the implementer was given %d images", len(a))
		}
		if p := r.Prompt(); !strings.Contains(p, "[Image 1: design.png]") {
			t.Errorf("the implementer's prompt does not show the image:\n%s", p)
		}
	}
}

// propose refuses an image the session's members did not share: another
// session's, one uploaded and never sent, one named twice, more than a
// task's text shows; and images on an item that is not a task.
func TestProposeRefusesImagesNotSharedInTheSession(t *testing.T) {
	s := newSessionWorld(t)
	s.withTools()
	b := s.withImages()
	id, run, _ := s.linkedLiveSession()
	other := s.session()
	s.sharedImage(b, id, "att_mine", "mine.png")
	s.uploadToSession(b, "att_other", other, "other.png", screenshot)
	s.sentIn(other, "att_other")
	s.uploadToSession(b, "att_unsent", id, "unsent.png", screenshot)
	task := s.taskIn(s.project, "Meter", s.marcio)
	s.upload(b, "att_task", task, "task.png", screenshot)
	seven := []string{"att_mine"}
	for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
		s.sharedImage(b, id, "att_many"+n, n+".png")
		seven = append(seven, "att_many"+n)
	}
	item := func(images ...string) string {
		raw, _ := json.Marshal(map[string]any{"items": []map[string]any{{"kind": "task", "project": "BL", "title": "T",
			"goal": "A goal long enough to be filed", "images": images}}})
		return string(raw)
	}
	for _, c := range []struct{ body, want string }{
		{item("att_other"), "image att_other is not one a member shared in this session"},
		{item("att_unsent"), "image att_unsent is not one a member shared in this session"},
		{item("att_task"), "image att_task is not one a member shared in this session"},
		{item("att_mine", "att_mine"), "image att_mine is named twice"},
		{item(seven...), "a task's text shows at most 6 images"},
		{`{"items":[{"kind":"epic","project":"BL","title":"E","images":["att_mine"]}]}`, "item 1: only a task item has images"},
	} {
		if status, out := s.tool(run, "propose", c.body); status != 422 || !strings.Contains(out["error"].(string), c.want) {
			t.Errorf("%s: %d %v, want %q", c.body, status, out, c.want)
		}
	}
	if n := s.count(`SELECT count(*) FROM session_proposals WHERE session_id = $1`, id); n != 0 {
		t.Errorf("%d proposals recorded from refused calls", n)
	}
	if status, out := s.tool(run, "propose", item(seven[:6]...)); status != 200 {
		t.Errorf("six shared images: %d %v", status, out)
	}
}

// An image the session no longer has when the item is filed (its session
// row removed it, or the card names one another session's) refuses the
// item, writing no task; the rest of the card is filed.
func TestProposalImageMetadataStaysInItsSession(t *testing.T) {
	s := newSessionWorld(t)
	b := s.withImages()
	id, other := s.session(), s.session()
	for _, image := range []struct{ session, id string }{{id, "att_own"}, {other, "att_foreign"}} {
		s.uploadToSession(b, image.id, image.session, "design.png", screenshot)
		s.sentIn(image.session, image.id)
	}
	s.proposal(id, []delivery.ProposalItem{{Kind: "task", Project: "BL", Title: "Design", Goal: "Build the proposed design", Images: []string{"att_own", "att_foreign"}}})
	card := s.ok(s.marcio, "GET", "/internal/sessions/"+id, nil)
	raw, _ := json.Marshal(card["proposals"])
	var cards []struct{ Attachments map[string]json.RawMessage }
	if err := json.Unmarshal(raw, &cards); err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 || len(cards[0].Attachments) != 1 || cards[0].Attachments["att_own"] == nil {
		t.Fatalf("proposal metadata: %s", raw)
	}
	if status, out := s.as(s.outsider, "GET", "/internal/sessions/"+id, nil); status != 404 {
		t.Fatalf("unauthorized detail: %d %v", status, out)
	}
}

func TestProposalImageAggregateByteBoundary(t *testing.T) {
	s := newSessionWorld(t)
	s.withTools()
	b := s.withImages()
	id, run, _ := s.linkedLiveSession()
	for _, image := range []string{"att_large_a", "att_large_b"} {
		s.sharedImage(b, id, image, "design.png")
	}
	mustExec(t, s.owner, `UPDATE attachments SET bytes = $1 WHERE id = ANY($2)`, delivery.MaxMessageAttachmentBytes/2, []string{"att_large_a", "att_large_b"})
	item := delivery.ProposalItem{Kind: "task", Project: "BL", Title: "Boundary", Goal: "Build the boundary design", Images: []string{"att_large_a", "att_large_b"}}
	body, _ := json.Marshal(map[string]any{"items": []delivery.ProposalItem{item}})
	status, out := s.tool(run, "propose", string(body))
	if status != 200 {
		t.Fatalf("exact 5MiB: %d %v", status, out)
	}
	mustExec(t, s.owner, `UPDATE attachments SET bytes = bytes + 1 WHERE id = 'att_large_b'`)
	if status, out := s.tool(run, "propose", string(body)); status != 422 {
		t.Fatalf("5MiB+1: %d %v", status, out)
	}
	if n := s.count(`SELECT count(*) FROM session_proposals WHERE session_id = $1`, id); n != 1 {
		t.Fatalf("overage recorded a proposal (%d)", n)
	}
	filed := s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/file", map[string]any{"proposalId": out["proposalId"], "items": []int{0}})
	if filed["results"].([]any)[0].(map[string]any)["status"] != "refused" {
		t.Fatalf("file must revalidate: %v", filed)
	}
	if n := s.count(`SELECT count(*) FROM tasks WHERE organization_id = $1`, s.org); n != 0 {
		t.Fatalf("%d tasks written on overage", n)
	}
	if n := s.count(`SELECT count(*) FROM attachments WHERE organization_id = $1 AND task_id IS NOT NULL`, s.org); n != 0 {
		t.Fatalf("%d copies written on overage", n)
	}
}

func TestProposalImageExpandedGoalBoundary(t *testing.T) {
	s := newSessionWorld(t)
	s.withTools()
	b := s.withImages()
	id, run, _ := s.linkedLiveSession()
	imageID := "att_" + strings.Repeat("0", 25)
	s.sharedImage(b, id, imageID, "design.png")
	// Propose reserves a production-length task-owned ID for the generated section.
	section := delivery.WithSessionImages("", []delivery.SessionImage{{ID: "att_" + strings.Repeat("0", 25), Name: "design.png"}})
	goal := strings.Repeat("x", agenttools.GoalMax-len(utf16.Encode([]rune(section)))-2) + "\U0001f680"
	propose := func(goal string) (int, map[string]any) {
		body, _ := json.Marshal(map[string]any{"items": []delivery.ProposalItem{{Kind: "task", Project: "BL", Title: "Boundary", Goal: goal, Images: []string{imageID}}}})
		return s.tool(run, "propose", string(body))
	}
	if status, out := propose(goal); status != 200 {
		t.Fatalf("exact expanded GoalMax: %d %v", status, out)
	}
	if status, out := propose(goal + "x"); status != 422 {
		t.Fatalf("expanded GoalMax+1: %d %v", status, out)
	}
	if n := s.count(`SELECT count(*) FROM session_proposals WHERE session_id = $1`, id); n != 1 {
		t.Fatalf("overlong proposal recorded (%d)", n)
	}
}

func TestFilingImageRefusalRollsBackEachItem(t *testing.T) {
	s := newSessionWorld(t)
	s.withTools()
	b := s.withImages()
	id, run, _ := s.linkedLiveSession()
	s.sharedImage(b, id, "att_session", "design.png")
	goal := "Build this ![design](attachment:att_session)"
	body, _ := json.Marshal(map[string]any{"items": []delivery.ProposalItem{
		{Kind: "task", Project: "BL", Title: "Refused design", Goal: goal, Images: []string{"att_session"}},
		{Kind: "task", Project: "BL", Title: "Valid design", Goal: "A valid task without inline image references"},
	}})
	status, out := s.tool(run, "propose", string(body))
	if status != 200 {
		t.Fatalf("propose: %d %v", status, out)
	}
	before := s.count(`SELECT next_task_number FROM projects WHERE id = $1`, s.project)
	for attempt := 0; attempt < 2; attempt++ {
		filed := s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/file", map[string]any{"proposalId": out["proposalId"], "items": []int{0, 1}})
		results := filed["results"].([]any)
		if results[0].(map[string]any)["status"] != "refused" {
			t.Fatalf("inline source reference must refuse atomically: %v", results)
		}
		if attempt == 0 && results[1].(map[string]any)["status"] != "filed" {
			t.Fatalf("valid item: %v", results)
		}
		if n := s.count(`SELECT count(*) FROM tasks WHERE organization_id = $1 AND title = 'Refused design'`, s.org); n != 0 {
			t.Fatalf("refused item left %d tasks committed on attempt %d", n, attempt)
		}
		if n := s.count(`SELECT next_task_number FROM projects WHERE id = $1`, s.project); n != before+1 {
			t.Fatalf("counter %d, want %d", n, before+1)
		}
		if n := s.count(`SELECT count(*) FROM session_filings WHERE proposal_id = $1`, out["proposalId"]); n != 1 {
			t.Fatalf("%d filings, want only valid item", n)
		}
	}

	task := s.taskIn(s.project, "Editable", s.marcio)
	proposal := s.proposal(id, []delivery.ProposalItem{{Kind: "edit", Task: s.keyOf(task), After: &delivery.TaskText{
		Goal: &goal, AcceptanceCriteria: &[]string{"Changed criteria"},
	}}})
	filed := s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/file", map[string]any{"proposalId": proposal, "items": []int{0}})
	if filed["results"].([]any)[0].(map[string]any)["status"] != "refused" {
		t.Fatalf("edit: %v", filed)
	}
	var storedGoal string
	var criteria []string
	if err := s.owner.QueryRow(context.Background(), `SELECT goal, acceptance_criteria FROM tasks WHERE id = $1`, task).Scan(&storedGoal, &criteria); err != nil {
		t.Fatal(err)
	}
	if storedGoal != "A goal long enough to keep" || !slices.Equal(criteria, []string{"works"}) {
		t.Fatalf("refused edit persisted text: %q %v", storedGoal, criteria)
	}
}

func TestFilingRefusesAnItemWhoseImageIsNotTheSessions(t *testing.T) {
	s := newSessionWorld(t)
	b := s.withImages()
	id := s.session()
	other := s.session()
	s.uploadToSession(b, "att_elsewhere", other, "elsewhere.png", screenshot)
	s.sentIn(other, "att_elsewhere")
	proposal := s.proposal(id, []delivery.ProposalItem{
		{Kind: "task", Project: "BL", Title: "With another's image", Goal: "A goal long enough to be filed", Images: []string{"att_elsewhere"}},
		{Kind: "task", Project: "BL", Title: "Plain", Goal: "A goal long enough to be filed"},
	})
	out := s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/file", map[string]any{"proposalId": proposal, "items": []int{0, 1}})
	results := out["results"].([]any)
	if r := results[0].(map[string]any); r["status"] != "refused" || !strings.Contains(r["why"].(string), "not one a member shared in this session") {
		t.Errorf("the item with another session's image: %v", r)
	}
	if r := results[1].(map[string]any); r["status"] != "filed" {
		t.Errorf("the plain item: %v", r)
	}
	if n := s.count(`SELECT count(*) FROM tasks WHERE organization_id = $1 AND title = 'With another''s image'`, s.org); n != 0 {
		t.Errorf("the refused item's task was written")
	}
	if n := s.count(`SELECT count(*) FROM attachments WHERE task_id IS NOT NULL AND organization_id = $1`, s.org); n != 0 {
		t.Errorf("%d task uploads written for a refused image", n)
	}
}
