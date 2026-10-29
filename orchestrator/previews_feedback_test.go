package orchestrator_test

import (
	"context"
	"testing"
	"time"
)

func (w *world) seedServerFeedback(id string) {
	w.t.Helper()
	mustExec(w.t, w.owner, `INSERT INTO people (id, organization_id, name) VALUES ($1, $2, 'Ana Example')`, "per_feedback_"+id, w.org)
	for _, status := range []string{"open", "resolved"} {
		mustExec(w.t, w.owner, `INSERT INTO preview_feedback
			(id, organization_id, project_id, preview_run_id, preview_server_name, author_person_id, submission_key, body, anchor,
			 status, resolved_at, resolved_by) VALUES ($1, $2, $3, $4, 'web', $5, $1, 'button overlaps',
			 '{"kind":"preview","url":"https://preview.example.test/cart"}', $6,
			 CASE WHEN $6 = 'resolved' THEN now() END, CASE WHEN $6 = 'resolved' THEN $5 END)`,
			"fbi_"+status+id, w.org, w.project, id, "per_feedback_"+id, status)
	}
	mustExec(w.t, w.owner, `INSERT INTO attachments (id, organization_id, preview_run_id, preview_server_name, uploaded_by,
		name, content_type, width, height, bytes, sha256, object_key, original_content_type, original_width, original_height, original_bytes, original_key)
		VALUES ($1, $2, $3, 'web', $4, 'shot.png', 'image/png', 1, 1, 1, 'sha', $5, 'image/png', 1, 1, 1, $6)`,
		"att_"+id, w.org, id, "per_feedback_"+id, "delivered-"+id, "original-"+id)
}

func (w *world) assertServerFeedback(id string, want int) {
	w.t.Helper()
	if got := w.count(`SELECT count(*) FROM preview_feedback WHERE preview_run_id = $1`, id); got != want {
		w.t.Fatalf("feedback count %d, want %d", got, want)
	}
	if want == 0 {
		if got := w.count(`SELECT count(*) FROM attachments WHERE preview_run_id = $1`, id); got != 0 {
			w.t.Fatalf("ended server kept %d screenshot rows", got)
		}
		if got := w.count(`SELECT count(*) FROM attachment_object_deletions WHERE object_key = ANY($1)`, []string{"delivered-" + id, "original-" + id}); got != 2 {
			w.t.Fatalf("queued %d screenshot blobs, want 2", got)
		}
	}
}

func TestServerFeedbackSurvivesSleepCrashReplacementAndPush(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	w.gh.Branch("feature/feedback", "main", "feedback")
	id := w.startBranch("feature/feedback")
	w.seedServerFeedback(id)
	web := w.serverID(id, "web")
	w.open(web)
	w.assertServerFeedback(id, 2)
	w.lux.Idle(web)
	w.until("sleep", func() bool { return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, id) == 1 })
	w.assertServerFeedback(id, 2)
	w.open(web)
	w.lux.Crash(w.luxRuns()[0].ID)
	w.until("crashed preview asleep", func() bool { return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, id) == 1 })
	w.assertServerFeedback(id, 2)
	w.open(web)
	if err := w.previews.Lux.Cancel(context.Background(), w.luxRuns()[0].ID); err != nil {
		t.Fatal(err)
	}
	w.until("terminated transport asleep", func() bool { return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, id) == 1 })
	w.open(web)
	w.until("new transport", func() bool { return len(w.luxRuns()) == 2 })
	w.assertServerFeedback(id, 2)
	if err := w.previews.BranchEvent(context.Background(), w.org, "acme/target", "feature/feedback", false, time.Now()); err != nil {
		t.Fatal(err)
	}
	w.pump()
	w.assertServerFeedback(id, 2)
}

func TestServerFeedbackPurgedByLogicalPreviewLifecycle(t *testing.T) {
	for _, reason := range []string{"stop", "branch deletion", "idle expiry", "server deletion"} {
		t.Run(reason, func(t *testing.T) {
			w := newWorld(t)
			w.wakeable()
			w.recipe("web", 3000, "npm run dev", "", nil, true)
			w.gh.Branch("feature/feedback", "main", "feedback")
			id := w.startBranch("feature/feedback")
			w.seedServerFeedback(id)
			switch reason {
			case "stop":
				if _, err := w.previews.StopBranchPreview(context.Background(), w.org, id, ""); err != nil {
					t.Fatal(err)
				}
			case "branch deletion":
				if err := w.gh.DeleteBranch("feature/feedback"); err != nil {
					t.Fatal(err)
				}
				mustExec(t, w.owner, `UPDATE runs SET preview_branch_checked_at = now() - interval '25 hours' WHERE id = $1`, id)
				if _, err := w.previews.CheckBranches(context.Background()); err != nil {
					t.Fatal(err)
				}
			case "idle expiry":
				mustExec(t, w.owner, `UPDATE runs SET created_at = now() - interval '31 days' WHERE id = $1`, id)
			case "server deletion":
				if err := w.previews.Lux.DeleteServer(context.Background(), w.serverID(id, "web")); err != nil {
					t.Fatal(err)
				}
			}
			w.until("logical end", func() bool {
				return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, id) == 1
			})
			w.assertServerFeedback(id, 0)
			if reason == "branch deletion" {
				w.gh.Branch("feature/feedback", "main", "recreated")
			}
			recreated := w.startBranch("feature/feedback")
			if recreated == id || w.count(`SELECT count(*) FROM preview_feedback WHERE preview_run_id = $1`, recreated) != 0 {
				t.Fatal("new incarnation inherited feedback")
			}
		})
	}
}
