package fakelux

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

var png = append([]byte("\x89PNG\r\n\x1a\n"), 1, 2, 3)

// The fake refuses attachments as the contract says lux does, naming the
// one and why.
func TestTheFakeValidatesAttachmentsAsLuxDoes(t *testing.T) {
	fake := New("", "k", func(map[string]any) Behaviour { return Behaviour{Hang: true} })
	c, run := submitRun(t, fake)
	awaitRun(t, fake, run.ID, "never ran", func(r *Run) bool { return r.State == "running" })
	img := func(name, typ string, data []byte) lux.Attachment {
		return lux.Attachment{Name: name, ContentType: typ, Data: data}
	}
	many := make([]lux.Attachment, 11)
	for i := range many {
		many[i] = img("a.png", "image/png", png)
	}
	for _, c2 := range []struct {
		in   []lux.Attachment
		want string
	}{
		{[]lux.Attachment{img("a.svg", "image/svg+xml", png)}, `attachments[0]: unknown content type "image/svg+xml"`},
		{[]lux.Attachment{img("a.png", "image/png", png), img("b.jpg", "image/jpeg", png)}, "attachments[1]: the data is not image/jpeg"},
		{[]lux.Attachment{img("../a.png", "image/png", png)}, "attachments[0]: bad name"},
		{[]lux.Attachment{img("a\nb.png", "image/png", png)}, "attachments[0]: bad name"},
		{[]lux.Attachment{img("", "image/png", png)}, "attachments[0]: bad name"},
		{[]lux.Attachment{img("big.png", "image/png", append(png, make([]byte, lux.MaxAttachmentBytes)...))}, "attachments[0]: "},
		{many, "attachments: at most 10 per input, got 11"},
	} {
		err := c.Input(context.Background(), run.ID, lux.InputRequest{Text: "x", RequestID: "r", Attachments: c2.in})
		le, ok := lux.AsError(err)
		if !ok || le.Status != 400 || le.Code != lux.CodeInvalidAttachment || !strings.HasPrefix(le.Message, c2.want) {
			t.Errorf("want 400 invalid_attachment %q, got %v", c2.want, err)
		}
	}
}

// inputRecord is the run's record of type typ for request id, or nil.
func inputRecord(fake *Server, runID, typ, requestID string) map[string]any {
	for _, rec := range fake.Records(runID) {
		data, _ := rec["data"].(map[string]any)
		if rec["type"] == typ && data["requestId"] == requestID {
			return data
		}
	}
	return nil
}

// pngMeta is what lux records of the png attachment named name.
func pngMeta(name string) []any {
	sum := sha256.Sum256(png)
	return []any{map[string]any{"name": name, "contentType": "image/png", "size": len(png), "sha256": hex.EncodeToString(sum[:])}}
}

// What lux records of an input's images is their metadata, never the bytes:
// on the accepted answer, on a failure, and on the prompt.
func TestLuxRecordsTheMetadataOfImagesNeverTheBytes(t *testing.T) {
	fake := New("", "k", func(map[string]any) Behaviour { return Behaviour{Hang: true} })
	c := c0(t, fake)
	run, err := c.Submit(context.Background(), lux.Spec{Workload: lux.Workload{Adapter: "opencode", Prompt: "go",
		Attachments: []lux.Attachment{{Name: "design.png", ContentType: "image/png", Data: png}}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	awaitRun(t, fake, run.ID, "never ran", func(r *Run) bool { return r.busy })
	for _, id := range []string{"dir_1", "dir_2"} {
		if err := c.Input(context.Background(), run.ID, lux.InputRequest{Text: "see", RequestID: id,
			Attachments: []lux.Attachment{{Name: id + ".png", ContentType: "image/png", Data: png}}}); err != nil {
			t.Fatal(err)
		}
	}
	fake.FailInput(run.ID, "dir_2", "the agent errored")

	for _, c := range []struct {
		typ, id string
		want    map[string]any
	}{
		{lux.RecordInput, "prompt", map[string]any{"requestId": "prompt", "phase": lux.InputAccepted, "receipt": true,
			"lands": "next_step", "text": "go", "attachments": pngMeta("design.png")}},
		{lux.RecordInput, "dir_1", map[string]any{"requestId": "dir_1", "phase": lux.InputAccepted, "receipt": true,
			"lands": "next_step", "text": "see", "attachments": pngMeta("dir_1.png")}},
		{lux.RecordInputFailed, "dir_2", map[string]any{"requestId": "dir_2", "error": "the agent errored",
			"attachments": pngMeta("dir_2.png")}},
	} {
		if got := inputRecord(fake, run.ID, c.typ, c.id); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s %s:\n got %#v\nwant %#v", c.typ, c.id, got, c.want)
		}
	}
}

// luxd refuses a body over 8 MiB, an input's or a submit's, before reading
// it; dude's 5 MiB a message keeps six images under it.
func TestTheFakeRefusesABodyOverLuxsLimit(t *testing.T) {
	fake := New("", "k", func(map[string]any) Behaviour { return Behaviour{Hang: true} })
	c, run := submitRun(t, fake)
	awaitRun(t, fake, run.ID, "never ran", func(r *Run) bool { return r.State == "running" })
	sixAtTheLimit := make([]lux.Attachment, 6)
	for i := range sixAtTheLimit {
		sixAtTheLimit[i] = lux.Attachment{Name: fmt.Sprintf("%d.png", i), ContentType: "image/png",
			Data: append(append([]byte{}, png...), make([]byte, (5<<20)/6-len(png))...)}
	}
	if err := c.Input(context.Background(), run.ID, lux.InputRequest{Text: "six", RequestID: "six", Attachments: sixAtTheLimit}); err != nil {
		t.Fatalf("six images at 5 MiB together: %v", err)
	}
	big := lux.Attachment{Name: "big.png", ContentType: "image/png", Data: append(append([]byte{}, png...), make([]byte, 4<<20)...)}
	err := c.Input(context.Background(), run.ID, lux.InputRequest{Text: "two", RequestID: "two", Attachments: []lux.Attachment{big, big}})
	if le, ok := lux.AsError(err); !ok || le.Status != 400 || le.Code != "bad_request" {
		t.Errorf("an input of 8 MiB of images: %v, want 400", err)
	}
	_, err = c.Submit(context.Background(), lux.Spec{Workload: lux.Workload{Adapter: "opencode", Prompt: "go",
		Attachments: []lux.Attachment{big, big}}}, "")
	if le, ok := lux.AsError(err); !ok || le.Status != 400 || le.Code != "bad_request" {
		t.Errorf("a submit of 8 MiB of images: %v, want 400", err)
	}
}

// A generic workload has nowhere to put an image, at submit or on input.
func TestAGenericRunRefusesImages(t *testing.T) {
	fake := New("", "k", nil)
	srv := c0(t, fake)
	spec := lux.Spec{Workload: lux.Workload{Adapter: "generic", Command: []string{"sleep"},
		Attachments: []lux.Attachment{{Name: "a.png", ContentType: "image/png", Data: png}}}}
	_, err := srv.Submit(context.Background(), spec, "")
	if le, ok := lux.AsError(err); !ok || le.Code != lux.CodeAttachmentsUnsupported {
		t.Fatalf("submit: %v", err)
	}
	spec.Workload.Attachments = nil
	run, err := srv.Submit(context.Background(), spec, "")
	if err != nil {
		t.Fatal(err)
	}
	awaitRun(t, fake, run.ID, "never ran", func(r *Run) bool { return r.State == "running" })
	err = srv.Input(context.Background(), run.ID, lux.InputRequest{RequestID: "x",
		Attachments: []lux.Attachment{{Name: "a.png", ContentType: "image/png", Data: png}}})
	if le, ok := lux.AsError(err); !ok || le.Status != 400 || le.Code != lux.CodeAttachmentsUnsupported {
		t.Fatalf("input: %v", err)
	}
}

// The prompt's images are checked at submit and given with the prompt.
func TestThePromptsImagesAreGivenWithIt(t *testing.T) {
	fake := New("", "k", func(map[string]any) Behaviour { return Behaviour{Hang: true} })
	c := c0(t, fake)
	spec := lux.Spec{Workload: lux.Workload{Adapter: "opencode", Prompt: "go",
		Attachments: []lux.Attachment{{Name: "d.png", ContentType: "image/jpeg", Data: png}}}}
	if _, err := c.Submit(context.Background(), spec, ""); err == nil {
		t.Fatal("a mismatched prompt image was taken")
	}
	spec.Workload.Attachments[0].ContentType = "image/png"
	run, err := c.Submit(context.Background(), spec, "")
	if err != nil {
		t.Fatal(err)
	}
	got := fake.Attachments(run.ID)["prompt"]
	if len(got) != 1 || !bytes.Equal(got[0].Data, png) {
		t.Fatalf("prompt images %v", got)
	}
}

func c0(t *testing.T, fake *Server) *lux.HTTPClient {
	t.Helper()
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	return lux.New(srv.URL, "k")
}
