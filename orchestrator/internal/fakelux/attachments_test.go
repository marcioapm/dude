package fakelux

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
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
		err := c.InputWith(context.Background(), run.ID, lux.InputRequest{Text: "x", RequestID: "r", Attachments: c2.in})
		le, ok := lux.AsError(err)
		if !ok || le.Status != 400 || le.Code != lux.CodeInvalidAttachment || !strings.HasPrefix(le.Message, c2.want) {
			t.Errorf("want 400 invalid_attachment %q, got %v", c2.want, err)
		}
	}
}

// What lux records of an input's images is their metadata, never the bytes.
func TestAcceptedInputRecordsItsImagesMetadata(t *testing.T) {
	fake := New("", "k", func(map[string]any) Behaviour { return Behaviour{Hang: true} })
	c, run := submitRun(t, fake)
	awaitRun(t, fake, run.ID, "never ran", func(r *Run) bool { return r.busy })
	if err := c.InputWith(context.Background(), run.ID, lux.InputRequest{RequestID: "dir_1",
		Attachments: []lux.Attachment{{Name: "a.png", ContentType: "image/png", Data: png}}}); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	sum := sha256.Sum256(png)
	for _, rec := range fake.runs[run.ID].records {
		data, _ := rec.Event["data"].(map[string]any)
		if rec.Event["type"] != lux.RecordInput || data["requestId"] != "dir_1" {
			continue
		}
		atts, _ := data["attachments"].([]any)
		if len(atts) != 1 {
			t.Fatalf("lux.input carried %v", data["attachments"])
		}
		a := atts[0].(map[string]any)
		if a["name"] != "a.png" || a["contentType"] != "image/png" || a["size"] != len(png) || a["sha256"] != hex.EncodeToString(sum[:]) {
			t.Errorf("metadata %v", a)
		}
		if _, ok := a["data"]; ok {
			t.Error("lux.input recorded the bytes")
		}
		return
	}
	t.Fatal("no accepted lux.input for dir_1")
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
	err = srv.InputWith(context.Background(), run.ID, lux.InputRequest{RequestID: "x",
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
