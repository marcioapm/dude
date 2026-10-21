package phases

import (
	"errors"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/images"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// continuedReviewer makes w's Run a reviewer's first resume of its
// predecessor's lux Run, on a project whose runtime image is a library
// image with no published version and no image layer configured.
func continuedReviewer(t *testing.T) *resumeWorld {
	w := newResumeWorld(t)
	w.exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, status, ended_at)
		VALUES ('run_prev_'||$1, $1, 'prj_'||$1, 'wi_'||$1, 1, 'review', 'completed', now())`, w.run.Org)
	w.exec(`UPDATE runs SET phase = 'review', role = 'reviewer', continues_run_id = 'run_prev_'||organization_id, continues_how = 'resume'
		WHERE id = $1`, w.run.ID)
	w.exec(`INSERT INTO images (id, organization_id, name) VALUES ('img_'||$1, $1, 'unbuilt')`, w.run.Org)
	w.exec(`UPDATE projects SET runtime_image_id = 'img_'||organization_id WHERE id = $1`, w.run.ProjectID)
	w.run.Phase, w.run.Role = "review", "reviewer"
	return w
}

// An image that cannot be had now replaces the continuation (it cannot
// resume); a failure to read what the image is, from the database, is the
// resume's to retry: the Run is neither failed nor replaced.
func TestARoundsImageLookupFailureIsRetriedNotReplayed(t *testing.T) {
	w := continuedReviewer(t)
	var cannot errCannotResume
	err := w.s.roundImage(w.ctx, w.run, lux.StoredSpec{})
	if !errors.As(err, &cannot) || !errors.As(err, new(images.Refused)) {
		t.Fatalf("an image with no version: %v, want errCannotResume of images.Refused", err)
	}

	w.exec(`REVOKE SELECT ON images FROM dude_app`)
	err = w.s.roundImage(w.ctx, w.run, lux.StoredSpec{})
	if err == nil || errors.As(err, &cannot) {
		t.Errorf("the image unreadable: %v, want the database error, not errCannotResume", err)
	}

	w.lux.set(lux.Run{ID: "lrun_1", State: "stopped", Epoch: 1}, nil)
	w.resumable()
	w.whilePaused()
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND retry_failures = 1 AND next_attempt_at > now()`); n != 1 {
		t.Errorf("the Run was not left paused and backed off for a retry")
	}
}
