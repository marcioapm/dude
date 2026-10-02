package images

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
)

// RunImage is what a Run got from the library (runs.image): kept as it
// was, so its page and its resumes say what it started with.
type RunImage struct {
	ImageID   string `json:"imageId"`
	Name      string `json:"name"`
	VersionID string `json:"versionId"`
	Version   int    `json:"version"`
	// The final image lux pulls, by digest.
	Ref   string `json:"ref"`
	Layer string `json:"layer"`
}

// Site is where a Run's image may be named. Pick says which wins.
type Site struct {
	// The image the Run's role names (RoleImage); "" for none or a preview.
	Role string
	// A preview's: its project's preview settings, by id and typed.
	PreviewID, PreviewTyped string
	// The project's runtime image, by id and typed.
	RuntimeID, RuntimeTyped string
	// The organization's default base.
	DefaultID string
	// DUDE_AGENT_IMAGE: what runs when nothing names an image.
	Fallback string
}

// Pick is the image a site names: a library image's id, or a ref typed by
// hand (or the fallback). Exactly one is set. Every library id comes
// before any typed image: an organization that sets a default base moves
// its projects onto the library, and one that never uses it runs as
// before.
func (s Site) Pick() (imageID, ref string) {
	for _, id := range []string{s.Role, s.PreviewID, s.RuntimeID, s.DefaultID} {
		if id != "" {
			return id, ""
		}
	}
	for _, typed := range []string{s.PreviewTyped, s.RuntimeTyped} {
		if typed != "" {
			return "", typed
		}
	}
	return "", s.Fallback
}

// RoleImage is the image a role's settings name over a project's
// agent_models and its organization's, the fixer then following the
// implementer, as the backend's resolveRoleImage; "" for none. An id
// known() rejects (an image since gone) is skipped.
func RoleImage(role string, project, org json.RawMessage, known func(string) bool) string {
	chain := []string{role}
	if role == "fixer" {
		chain = append(chain, "implementer")
	}
	layers := make([]map[string]struct {
		Image string `json:"image"`
	}, 2)
	for i, raw := range []json.RawMessage{project, org} {
		if json.Unmarshal(raw, &layers[i]) != nil {
			layers[i] = nil
		}
	}
	for _, r := range chain {
		for _, l := range layers {
			if id := l[r].Image; id != "" && known(id) {
				return id
			}
		}
	}
	return ""
}

// Known reads the organization's image ids, in its transaction, for
// RoleImage.
func Known(ctx context.Context, tx pgx.Tx) (func(string) bool, error) {
	rows, err := tx.Query(ctx, `SELECT id FROM images`)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return func(id string) bool { return set[id] }, nil
}

// Outcome is what a Run whose image is a library image does now: start
// on Ref, wait for WaitBuild (its dude layer being added, or its first
// version building), or fail with Fail, before lux and at no model cost.
type Outcome struct {
	Ref       string
	Image     *RunImage
	WaitBuild string
	Fail      string
}

// ErrNotConfigured is a library image with no dude layer to finish it.
const ErrNotConfigured = "image library not configured: DUDE_LAYER_IMAGE is unset, so the library's images cannot run"

// Resolve is what imageID gives a Run now, under layer (DUDE_LAYER_IMAGE),
// in the Run's organization's transaction. waitingOn is the job the Run
// already waited on (runs.image_build_id), "" for none: its failure is the
// Run's. A build cancelled under it (a newer version queued instead) is
// not: the image is looked at again. A finish that is missing is queued,
// or joined if one is: two Runs needing the same one share it, and the
// builder takes it before any build.
func Resolve(ctx context.Context, tx pgx.Tx, imageID, layer, waitingOn string) (Outcome, error) {
	if waitingOn != "" {
		var state, kind string
		var why *string
		err := tx.QueryRow(ctx, `SELECT state, kind, error FROM image_builds WHERE id = $1`, waitingOn).Scan(&state, &kind, &why)
		if err != nil && !db.IsNotFound(err) {
			return Outcome{}, err
		}
		if state == "failed" || (state == "cancelled" && kind == "finish") {
			name, version := versionName(ctx, tx, waitingOn)
			sentence := "it was cancelled"
			if why != nil {
				sentence = *why
			}
			if kind == "finish" {
				return Outcome{Fail: fmt.Sprintf("its image %s v%d could not get the dude layer: %s", name, version, sentence)}, nil
			}
			return Outcome{Fail: fmt.Sprintf("its image %s has no published version: v%d failed to build: %s", name, version, sentence)}, nil
		}
	}
	var name string
	var versionID *string
	var number *int
	var userRef *string
	err := tx.QueryRow(ctx, `SELECT i.name, v.id, v.number, v.user_ref FROM images i
		LEFT JOIN image_versions v ON v.id = i.published_version_id WHERE i.id = $1`, imageID).Scan(&name, &versionID, &number, &userRef)
	if db.IsNotFound(err) {
		return Outcome{Fail: fmt.Sprintf("its image %s is not one of the organization's", imageID)}, nil
	}
	if err != nil {
		return Outcome{}, err
	}
	if layer == "" {
		return Outcome{Fail: ErrNotConfigured}, nil
	}
	if versionID == nil {
		// Its first version may be on its way: the Run waits for it.
		var build string
		err := tx.QueryRow(ctx, `SELECT b.id FROM image_builds b JOIN image_versions v ON v.id = b.image_version_id
			WHERE v.image_id = $1 AND b.kind = 'build' AND b.state IN ('queued', 'running')
			ORDER BY b.requested_at DESC LIMIT 1`, imageID).Scan(&build)
		if db.IsNotFound(err) {
			return Outcome{Fail: fmt.Sprintf("its image %s has no published version: build and publish one first", name)}, nil
		}
		if err != nil {
			return Outcome{}, err
		}
		return Outcome{WaitBuild: build}, nil
	}
	img := &RunImage{ImageID: imageID, Name: name, VersionID: *versionID, Version: *number, Layer: layer}
	var final string
	err = tx.QueryRow(ctx, `SELECT final_ref FROM image_finals WHERE image_version_id = $1 AND layer_ref = $2`, *versionID, layer).Scan(&final)
	if err == nil {
		img.Ref = final
		return Outcome{Ref: final, Image: img}, nil
	}
	if !db.IsNotFound(err) {
		return Outcome{}, err
	}
	// Its version's own build may be finishing it with this layer now.
	var build string
	err = tx.QueryRow(ctx, `SELECT id FROM image_builds WHERE image_version_id = $1 AND state IN ('queued', 'running')
		AND (kind = 'build' OR layer_ref = $2) ORDER BY kind = 'build' DESC LIMIT 1`, *versionID, layer).Scan(&build)
	if err == nil {
		return Outcome{WaitBuild: build}, nil
	}
	if !db.IsNotFound(err) {
		return Outcome{}, err
	}
	org := ""
	if err := tx.QueryRow(ctx, `SELECT current_organization_id()`).Scan(&org); err != nil {
		return Outcome{}, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO image_builds (id, organization_id, image_version_id, kind, layer_ref)
		VALUES (new_id('imb'), $1, $2, 'finish', $3)
		ON CONFLICT (image_version_id, layer_ref) WHERE kind = 'finish' AND state IN ('queued', 'running') DO NOTHING
		RETURNING id`, org, *versionID, layer).Scan(&build)
	if errors.Is(err, pgx.ErrNoRows) {
		// Another Run queued it between the read and the insert.
		err = tx.QueryRow(ctx, `SELECT id FROM image_builds WHERE image_version_id = $1 AND layer_ref = $2 AND kind = 'finish'
			AND state IN ('queued', 'running')`, *versionID, layer).Scan(&build)
	}
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{WaitBuild: build}, nil
}

func versionName(ctx context.Context, tx pgx.Tx, build string) (string, int) {
	var name string
	var n *int
	_ = tx.QueryRow(ctx, `SELECT i.name, v.number FROM image_builds b JOIN image_versions v ON v.id = b.image_version_id
		JOIN images i ON i.id = v.image_id WHERE b.id = $1`, build).Scan(&name, &n)
	if n == nil {
		return name, 0
	}
	return name, *n
}

// Waiting is a Run's image not ready yet: Build is the job it waits on;
// New when the Run was not waiting on it before.
type Waiting struct {
	Build string
	New   bool
}

// OfflineSentence is what a Run waiting for its image says when the
// builder has not been heard from for Offline: since its last heartbeat.
func OfflineSentence(seen *time.Time) string {
	if seen == nil {
		return "image builder offline: it has never reported in"
	}
	return "image builder offline since " + seen.UTC().Format("2006-01-02 15:04 UTC")
}

func (w Waiting) Error() string { return "waiting for image build " + w.Build }

// Refused is a Run that cannot have its image: it fails before lux, with
// Reason as its error.
type Refused struct{ Reason string }

func (r Refused) Error() string { return r.Reason }

// Choose is the image a Run at site starts on now, in its organization's
// transaction: a ref lux pulls, and for a library image what it got. A
// library image not ready is Waiting (the job recorded on the Run, so its
// failure is the Run's: waitingOn); one that cannot be had is Refused.
// Both are the caller's to act on after committing: returned from the
// transaction they would roll back the finish job it queued (Settle).
func Choose(ctx context.Context, tx pgx.Tx, site Site, layer, runID, waitingOn string) (string, *RunImage, error) {
	id, ref := site.Pick()
	if id == "" {
		return ref, nil, nil
	}
	out, err := Resolve(ctx, tx, id, layer, waitingOn)
	switch {
	case err != nil:
		return "", nil, err
	case out.Fail != "":
		return "", nil, Refused{out.Fail}
	case out.WaitBuild != "":
		fresh, err := Wait(ctx, tx, runID, out.WaitBuild)
		if err != nil {
			return "", nil, err
		}
		if gone, why, err := givenUp(ctx, tx, runID); err != nil || gone {
			if err != nil {
				return "", nil, err
			}
			return "", nil, Refused{why}
		}
		return "", nil, Waiting{Build: out.WaitBuild, New: fresh}
	}
	return out.Ref, out.Image, nil
}

// Wait records on a Run the job it waits on, once, and since when it has
// waited for an image: true when the job is new, for the caller's event.
func Wait(ctx context.Context, tx pgx.Tx, runID, build string) (bool, error) {
	tag, err := tx.Exec(ctx, `UPDATE runs SET image_build_id = $2, image_waiting_since = COALESCE(image_waiting_since, now())
		WHERE id = $1 AND image_build_id IS DISTINCT FROM $2`, runID, build)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// givenUp: the Run has waited for its image GiveUp while the builder was
// offline (the overlap of its wait and the builder's silence), and fails
// with why.
func givenUp(ctx context.Context, tx pgx.Tx, runID string) (bool, string, error) {
	var seen *time.Time
	var gone bool
	err := tx.QueryRow(ctx, `SELECT b.seen_at,
			(b.seen_at IS NULL OR b.seen_at < now() - make_interval(secs => $2))
			AND now() - GREATEST(r.image_waiting_since, b.seen_at) > make_interval(secs => $3)
		FROM runs r LEFT JOIN image_builder b ON true WHERE r.id = $1`, runID, Offline.Seconds(), GiveUp.Seconds()).Scan(&seen, &gone)
	if err != nil || !gone {
		return false, "", err
	}
	return true, OfflineSentence(seen), nil
}

// Settle splits Choose's error: one to return from the transaction (a
// failure to read or write), and the Waiting or Refused to act on once it
// has committed.
func Settle(err error) (txErr, outcome error) {
	if errors.As(err, new(Waiting)) || errors.As(err, new(Refused)) {
		return nil, err
	}
	return err, nil
}
