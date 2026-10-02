package phases

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/objects"
)

// memStore is a bucket in memory; gets counts its reads, and fail, when
// set, is what every read answers.
type memStore struct {
	mu   sync.Mutex
	objs map[string][]byte
	gets int
	fail error
}

func (m *memStore) Get(_ context.Context, key string, max int64) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gets++
	if m.fail != nil {
		return nil, m.fail
	}
	b, ok := m.objs[key]
	if !ok {
		return nil, objects.ErrNotFound
	}
	return b, nil
}

var (
	pngBytes  = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{1}, 40)...)
	jpegBytes = append([]byte{0xff, 0xd8, 0xff, 0xe0}, bytes.Repeat([]byte{2}, 40)...)
)

// imageWorld is one running agent on a fake lux, a task with images
// uploaded to it, and a syncer reading them from memory.
type imageWorld struct {
	t     *testing.T
	s     *Syncer
	fake  *fakelux.Server
	store *memStore
	owner *pgx.Conn
	org   string
	run   phaseRun
}

func newImageWorld(t *testing.T, adapter string) *imageWorld {
	ctx := context.Background()
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	for _, q := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_'||$1, $1, 'P', 'prj_'||$1, 'P')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_'||$1, $1, 'prj_'||$1, 1, 'T', 'G')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi2_'||$1, $1, 'prj_'||$1, 2, 'T2', 'G')`,
		`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status) VALUES ('run_'||$1, $1, 'prj_'||$1, 'wi_'||$1, 1, 'running')`,
	} {
		if _, err := owner.Exec(ctx, q, org); err != nil {
			t.Fatal(err)
		}
	}
	fake := fakelux.New("", "k", func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true, WakeOnInput: true} })
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(func() { srv.Close(); fake.Close() })
	client := lux.New(srv.URL, "k")
	spec := lux.Spec{Name: "t", Image: lux.Image{Ref: "img"}, Workload: lux.Workload{Adapter: adapter, Prompt: "go"}}
	if adapter == "generic" {
		spec.Workload.Command = []string{"sleep", "infinity"}
	}
	lr, err := client.Submit(ctx, spec, "run_"+org)
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		got, _ := client.Get(ctx, lr.ID)
		if got.State == "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the fake lux Run did not start: %s", got.State)
		}
		time.Sleep(10 * time.Millisecond)
	}
	store := &memStore{objs: map[string][]byte{"k/a.png": pngBytes, "k/b.jpg": jpegBytes, "k/other.png": pngBytes}}
	s := &Syncer{DB: app, Lux: client, Objects: store, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	run := phaseRun{ID: "run_" + org, Org: org, ProjectID: "prj_" + org, TaskID: "wi_" + org, Status: statusRunning,
		LuxRunID: lr.ID, LuxState: "running"}
	w := &imageWorld{t: t, s: s, fake: fake, store: store, owner: owner, org: org, run: run}
	w.upload("att_a", "wi_", "checkout.png", "image/png", "k/a.png", len(pngBytes))
	w.upload("att_b", "wi_", "Summary v3.jpg", "image/jpeg", "k/b.jpg", len(jpegBytes))
	w.upload("att_other", "wi2_", "other.png", "image/png", "k/other.png", len(pngBytes))
	return w
}

func (w *imageWorld) exec(q string, args ...any) {
	w.t.Helper()
	if _, err := w.owner.Exec(context.Background(), q, args...); err != nil {
		w.t.Fatal(err)
	}
}

func (w *imageWorld) upload(id, taskPrefix, name, typ, key string, size int) {
	w.exec(`INSERT INTO attachments (id, organization_id, task_id, name, content_type, width, height, bytes, sha256, object_key,
		original_content_type, original_width, original_height, original_bytes, original_key)
		VALUES ($1, $2, $3||$2, $4, $5, 10, 10, $6, 'x', $7, $5, 20, 20, $6, $7||'.orig')`, id, w.org, taskPrefix, name, typ, size, key)
}

// steer queues a directive with images, as the API does.
func (w *imageWorld) steer(id, text string, images ...string) {
	w.exec(`INSERT INTO directives (id, organization_id, task_id, run_id, text) VALUES ($1, $2, 'wi_'||$2, 'run_'||$2, $3)`, id, w.org, text)
	for i, a := range images {
		w.exec(`UPDATE attachments SET directive_id = $1, position = $2, attached_at = now() WHERE id = $3`, id, i, a)
	}
}

func (w *imageWorld) deliver() {
	w.t.Helper()
	if _, err := w.s.deliverDirectives(context.Background(), w.run); err != nil {
		w.t.Fatal(err)
	}
}

func (w *imageWorld) directive(id string) (sent, failed bool, reason string) {
	var e *string
	_ = w.owner.QueryRow(context.Background(), `SELECT sent_at IS NOT NULL, failed_at IS NOT NULL, error FROM directives WHERE id = $1`, id).
		Scan(&sent, &failed, &e)
	if e != nil {
		reason = *e
	}
	return
}

// A steer's images reach lux with its words, each with its name, its type
// and the bytes storage holds, in the order they were attached.
func TestASteersImagesAreDeliveredWithItsWords(t *testing.T) {
	w := newImageWorld(t, "acp")
	w.steer("dir_img", "VAT stays at 0, see these", "att_b", "att_a")
	// Attached in that order: b first.
	w.exec(`UPDATE attachments SET position = 0 WHERE id = 'att_b'`)
	w.exec(`UPDATE attachments SET position = 1 WHERE id = 'att_a'`)
	w.deliver()

	got := w.fake.Attachments(w.run.LuxRunID)["dir_img"]
	if len(got) != 2 {
		t.Fatalf("lux got %d images with the steer, want 2", len(got))
	}
	want := []lux.Attachment{{Name: "Summary v3.jpg", ContentType: "image/jpeg", Data: jpegBytes}, {Name: "checkout.png", ContentType: "image/png", Data: pngBytes}}
	for i := range want {
		if got[i].Name != want[i].Name || got[i].ContentType != want[i].ContentType || !bytes.Equal(got[i].Data, want[i].Data) {
			t.Errorf("image %d: got %s %s (%d bytes), want %s %s", i, got[i].Name, got[i].ContentType, len(got[i].Data), want[i].Name, want[i].ContentType)
		}
	}
	if sent, failed, _ := w.directive("dir_img"); !sent || failed {
		t.Fatalf("sent=%v failed=%v", sent, failed)
	}
	// What lux recorded of it: metadata, never the bytes.
	var body map[string]any
	_ = json.Unmarshal([]byte(w.fake.Runs()[0].InputBodies["dir_img"][0]), &body)
	if body["text"] != "VAT stays at 0, see these" {
		t.Errorf("the words were not sent with the images: %v", body["text"])
	}
}

// A retry of the same directive (lux unavailable the first time) is the
// same request: the agent has the images once.
func TestARetriedImageSteerLandsOnce(t *testing.T) {
	w := newImageWorld(t, "acp")
	first := true
	w.fake.BeforeInput = func(_, _ string) bool {
		if first {
			first = false
			return false
		}
		return true
	}
	w.steer("dir_retry", "look", "att_a")
	w.deliver()
	if sent, _, _ := w.directive("dir_retry"); sent {
		t.Fatal("a refused send was marked sent")
	}
	w.exec(`UPDATE runs SET next_attempt_at = NULL WHERE id = $1`, w.run.ID)
	w.deliver()
	// A third send of the same request id, as after a crash before sent_at
	// was written.
	if err := w.s.Lux.InputWith(context.Background(), w.run.LuxRunID, lux.InputRequest{Text: "look", RequestID: "dir_retry",
		Attachments: []lux.Attachment{{Name: "checkout.png", ContentType: "image/png", Data: pngBytes}}}); err != nil {
		t.Fatal(err)
	}
	if bodies := w.fake.Runs()[0].InputBodies["dir_retry"]; len(bodies) != 3 {
		t.Fatalf("%d requests, want 3", len(bodies))
	}
	// The agent's turn ends: it reads what it was given.
	w.fake.EndTurn(w.run.LuxRunID)
	var seen int
	for _, in := range w.fake.Runs()[0].Inputs {
		if in == "look" {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("the agent read the steer %d times, want once", seen)
	}
	if got := w.fake.Attachments(w.run.LuxRunID)["dir_retry"]; len(got) != 1 || !bytes.Equal(got[0].Data, pngBytes) {
		t.Errorf("the agent has %d images with the steer, want the one", len(got))
	}
	if inputs := acceptedInputs(w.fake, w.run.LuxRunID, "dir_retry"); inputs != 1 {
		t.Errorf("lux took the steer %d times, want once", inputs)
	}
}

// acceptedInputs counts lux's accepted lux.input records for request id.
func acceptedInputs(fake *fakelux.Server, runID, requestID string) int {
	n := 0
	for _, rec := range fake.Records(runID) {
		data, _ := rec["data"].(map[string]any)
		if rec["type"] == lux.RecordInput && data["requestId"] == requestID && data["phase"] == lux.InputAccepted {
			n++
		}
	}
	return n
}

// lux refusing the images fails the directive with a reason a person can
// read, rather than retrying it for ever.
func TestLuxRefusingImagesFailsTheSteerReadably(t *testing.T) {
	w := newImageWorld(t, "generic")
	w.steer("dir_gen", "see", "att_a")
	w.deliver()
	sent, failed, reason := w.directive("dir_gen")
	if sent || !failed || reason != "this agent cannot take images" {
		t.Fatalf("sent=%v failed=%v reason=%q", sent, failed, reason)
	}
	var n int
	_ = w.owner.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE event_type = 'run.directive.failed'
		AND payload->>'directiveId' = 'dir_gen' AND payload->>'error' = 'this agent cannot take images'`).Scan(&n)
	if n != 1 {
		t.Errorf("%d run.directive.failed events, want 1", n)
	}
}

func TestAnInvalidImageIsRefusedByLuxAndFailsTheSteer(t *testing.T) {
	w := newImageWorld(t, "acp")
	// Stored bytes that are not what the row says.
	w.store.objs["k/a.png"] = []byte("not a png at all")
	w.steer("dir_bad", "see", "att_a")
	w.deliver()
	_, failed, reason := w.directive("dir_bad")
	if !failed || !strings.HasPrefix(reason, "lux refused its images: attachments[0]: ") {
		t.Fatalf("failed=%v reason=%q", failed, reason)
	}
}

// Storage refusing the read (a role without access to the bucket) answers
// the same next time: the steer fails with why, rather than waiting for
// ever. Storage failing (5xx, unreachable) may pass: the send waits, as
// for lux unreachable, and goes once storage answers.
func TestStorageRefusingAnImageFailsTheSteerAndStorageDownWaits(t *testing.T) {
	w := newImageWorld(t, "acp")
	w.store.fail = &objects.RefusedError{Status: 403, Err: errors.New("AccessDenied: Access Denied")}
	w.steer("dir_denied", "see", "att_a")
	w.deliver()
	want := "its image checkout.png could not be read: storage refused it (403)"
	if sent, failed, reason := w.directive("dir_denied"); sent || !failed || reason != want {
		t.Fatalf("sent=%v failed=%v reason=%q, want %q", sent, failed, reason, want)
	}

	w.store.fail = errors.New("reading an attachment from storage: 503 SlowDown")
	w.steer("dir_slow", "again", "att_b")
	w.deliver()
	if sent, failed, _ := w.directive("dir_slow"); sent || failed {
		t.Fatalf("storage down: sent=%v failed=%v, want it waiting", sent, failed)
	}
	w.store.fail = nil
	w.exec(`UPDATE runs SET next_attempt_at = NULL WHERE id = $1`, w.run.ID)
	w.deliver()
	if sent, failed, _ := w.directive("dir_slow"); !sent || failed {
		t.Fatalf("storage back: sent=%v failed=%v", sent, failed)
	}
}

func TestAMissingImageFailsTheSteer(t *testing.T) {
	w := newImageWorld(t, "acp")
	delete(w.store.objs, "k/b.jpg")
	w.steer("dir_gone", "see", "att_b")
	w.deliver()
	if _, failed, reason := w.directive("dir_gone"); !failed || reason != "its image Summary v3.jpg is gone from storage" {
		t.Fatalf("failed=%v reason=%q", failed, reason)
	}
	if len(w.fake.Runs()[0].InputBodies["dir_gone"]) != 0 {
		t.Error("a steer missing an image was sent anyway")
	}
}

// Retry after a failure is a new directive superseding it with the same
// words: it carries the failed one's images.
func TestARetryCarriesTheFailedSteersImages(t *testing.T) {
	w := newImageWorld(t, "acp")
	w.steer("dir_first", "see", "att_a")
	w.exec(`UPDATE directives SET failed_at = now(), error = 'the run stopped' WHERE id = 'dir_first'`)
	w.exec(`INSERT INTO directives (id, organization_id, task_id, run_id, text, supersedes) VALUES ('dir_again', $1, 'wi_'||$1, 'run_'||$1, 'see', 'dir_first')`, w.org)
	w.deliver()
	if got := w.fake.Attachments(w.run.LuxRunID)["dir_again"]; len(got) != 1 || got[0].Name != "checkout.png" {
		t.Fatalf("the retry carried %v", got)
	}
}

// A steer superseding a failed one with other words is a new message: it
// does not carry the failed one's images.
func TestASupersedingSteerWithNewWordsCarriesNoImages(t *testing.T) {
	w := newImageWorld(t, "acp")
	w.steer("dir_first", "see", "att_a")
	w.exec(`UPDATE directives SET failed_at = now(), error = 'the run stopped' WHERE id = 'dir_first'`)
	w.exec(`INSERT INTO directives (id, organization_id, task_id, run_id, text, supersedes) VALUES ('dir_other', $1, 'wi_'||$1, 'run_'||$1, 'use the blue one instead', 'dir_first')`, w.org)
	gets := w.store.gets
	w.deliver()
	if sent, _, _ := w.directive("dir_other"); !sent {
		t.Fatal("the new words were not sent")
	}
	if got := w.fake.Attachments(w.run.LuxRunID)["dir_other"]; len(got) != 0 {
		t.Errorf("new words carried the failed steer's images: %v", got)
	}
	if w.store.gets != gets {
		t.Error("new words read the failed steer's images")
	}
}

// A steer with an image queued while the Run is paused waits — /resume
// carries no images — and goes through /input once the Run runs again.
func TestAnImageSteerQueuedWhilePausedIsDeliveredAfterResume(t *testing.T) {
	w := newImageWorld(t, "acp")
	w.steer("dir_paused", "", "att_b")
	paused := w.run
	paused.Status, paused.LuxState = statusPaused, "paused"
	if _, err := w.s.deliverDirectives(context.Background(), paused); err != nil {
		t.Fatal(err)
	}
	resuming := w.run
	resuming.LuxState = "resuming"
	if _, err := w.s.deliverDirectives(context.Background(), resuming); err != nil {
		t.Fatal(err)
	}
	if sent, _, _ := w.directive("dir_paused"); sent || len(w.fake.Runs()[0].InputBodies["dir_paused"]) != 0 {
		t.Fatal("an image steer was sent to a Run not running")
	}
	w.deliver()
	if sent, failed, _ := w.directive("dir_paused"); !sent || failed {
		t.Fatalf("after resume: sent=%v failed=%v", sent, failed)
	}
	if got := w.fake.Attachments(w.run.LuxRunID)["dir_paused"]; len(got) != 1 || got[0].Name != "Summary v3.jpg" {
		t.Errorf("lux got %v", got)
	}
}

// An interrupt alone carries neither the words nor their images.
func TestAnInterruptAloneCarriesNoImages(t *testing.T) {
	w := newImageWorld(t, "acp")
	w.steer("dir_root", "see", "att_a")
	w.deliver()
	w.exec(`INSERT INTO directives (id, organization_id, task_id, run_id, text, supersedes, interrupt, resends)
		VALUES ('dir_int', $1, 'wi_'||$1, 'run_'||$1, 'see', 'dir_root', true, 'dir_root')`, w.org)
	w.exec(`UPDATE directives SET interrupt_only = NULL WHERE id = 'dir_int'`)
	gets := w.store.gets
	w.deliver()
	if w.store.gets != gets {
		t.Error("an interrupt alone read the images again")
	}
	if got := w.fake.Attachments(w.run.LuxRunID)["dir_int"]; len(got) != 0 {
		t.Errorf("an interrupt alone carried %d images", len(got))
	}
}
