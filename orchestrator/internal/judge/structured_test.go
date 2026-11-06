package judge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

type route struct {
	Route string `json:"route"`
}

func parseRoute(obj []byte) (any, Judgement, error) {
	var r route
	if err := json.Unmarshal(obj, &r); err != nil {
		return nil, Judgement{}, err
	}
	if r.Route == "" {
		return nil, Judgement{}, errors.New("no route")
	}
	return r, Judgement{Decision: r.Route, Confidence: 0.9, Reasoning: "routed"}, nil
}

var routing = StructuredAsk{Asker: "chat.route", About: "slack · #support", Instructions: "Route it.",
	Input: "@dude acme-header-value is the header", Parse: parseRoute}

func TestStructuredIsRecordedAsAJudgementWithItsOwnInstructions(t *testing.T) {
	w := setup(t, answering("Sure:\n```json\n{\"route\":\"task\"}\n```"), true)
	got, err := w.svc.Structured(context.Background(), w.org, routing)
	if err != nil {
		t.Fatal(err)
	}
	if got.Answer.(route).Route != "task" || got.Summary.Decision != "task" || got.CallID == "" {
		t.Fatalf("got %+v", got)
	}
	body := w.proxy.bodies[0]
	if _, has := body["temperature"]; has {
		t.Errorf("temperature sent: %v", body)
	}
	msgs := body["messages"].([]any)
	system, _ := msgs[0].(map[string]any)["content"].(string)
	// The caller's own instructions first, then the format rule every judgement is told.
	if body["model"] != "gpt-6-sol-mini" || msgs[0].(map[string]any)["role"] != "system" || !strings.HasPrefix(system, "Route it.\n\n") ||
		!strings.Contains(system, "Its first character is { and its last character is }.") ||
		!strings.Contains(system, "before or after it") {
		t.Fatalf("sent %v", body)
	}
	calls := w.calls(t)
	if len(calls) != 1 || calls[0].Asker != "chat.route" || calls[0].Status != "ok" || calls[0].TierID == nil || *calls[0].TierID != w.tierID ||
		!strings.Contains(string(calls[0].Result), `"decision": "task"`) {
		t.Fatalf("recorded %+v", calls)
	}
	// The tier's header value is redacted from what is stored, as for Judge.
	if strings.Contains(string(calls[0].Request), "acme-header-value") {
		t.Fatalf("the header value was stored: %s", calls[0].Request)
	}
}

func TestStructuredFailsClosedAndStillRecords(t *testing.T) {
	w := setup(t, answering(`{"other":1}`), true)
	got, err := w.svc.Structured(context.Background(), w.org, routing)
	if !errors.Is(err, ErrUnparseable) || got.Answer != nil || got.CallID == "" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if c := w.calls(t); len(c) != 1 || c[0].Status != "error" || c[0].Result != nil {
		t.Fatalf("recorded %+v", c)
	}
	off := setup(t, answering(`{"route":"task"}`), false)
	if _, err := off.svc.Structured(context.Background(), off.org, routing); !errors.Is(err, ErrOff) || off.proxy.count() != 0 {
		t.Fatalf("off: %v, %d sent", err, off.proxy.count())
	}
	// A tier with no model: ErrNoModel, nothing sent, nothing recorded.
	noModel := setup(t, answering(`{"route":"task"}`), true)
	if _, err := noModel.owner.Exec(context.Background(), `UPDATE model_tiers SET model = NULL WHERE id = $1`, noModel.tierID); err != nil {
		t.Fatal(err)
	}
	if _, err := noModel.svc.Structured(context.Background(), noModel.org, routing); !errors.Is(err, ErrNoModel) || noModel.proxy.count() != 0 {
		t.Fatalf("no model: %v, %d sent", err, noModel.proxy.count())
	}
	if c := noModel.calls(t); len(c) != 0 {
		t.Fatalf("no model: recorded %+v", c)
	}
	// A proxy error echoing the proxy key and the tier's header value: the
	// stored row has neither, in its error or its response.
	const key = "sk-down-key-0123456789"
	down := setup(t, func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("bad key " + key + " with X-Team acme-header-value"))
	}, true)
	down.svc.LLM.Key = key
	_, err = down.svc.Structured(context.Background(), down.org, routing)
	c := down.calls(t)
	if err == nil || len(c) != 1 || c[0].Status != "error" || c[0].Error == nil {
		t.Fatalf("a proxy error: %v, recorded %+v", err, c)
	}
	stored := *c[0].Error
	if c[0].Response != nil {
		stored += "\n" + *c[0].Response
	}
	if !strings.Contains(stored, "bad key") {
		t.Fatalf("the proxy's error is not stored: %q", stored)
	}
	if strings.Contains(stored, key) || strings.Contains(stored, "acme-header-value") || strings.Contains(err.Error(), key) {
		t.Fatalf("stored %q, returned %v: the key or header value is not redacted", stored, err)
	}
}

// An unparseable answer echoing the proxy key: the error returned has the
// key redacted from its text and is still ErrUnparseable.
func TestStructuredRedactsItsErrorAndStillUnwraps(t *testing.T) {
	const key = "sk-structured-key-0123456789"
	w := setup(t, answering(`{"route":"`+key+`"}`), true)
	w.svc.LLM.Key = key
	ask := routing
	ask.Parse = func(obj []byte) (any, Judgement, error) {
		return nil, Judgement{}, fmt.Errorf("no such route in %s", obj)
	}
	_, err := w.svc.Structured(context.Background(), w.org, ask)
	if !errors.Is(err, ErrUnparseable) || strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "[REDACTED:DUDE_LLM_KEY]") {
		t.Fatalf("err %v", err)
	}
}

// A call sent is recorded even when the caller's ctx ends before the
// model answers (the record has its own bound, recordTimeout).
func TestStructuredRecordsACallPastTheCallersDeadline(t *testing.T) {
	release := make(chan struct{})
	w := setup(t, func(http.ResponseWriter) { <-release }, true)
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := w.svc.Structured(ctx, w.org, routing); err == nil || err.Error() != "no answer from the LLM proxy before the caller's deadline" {
		t.Errorf("err = %v", err)
	}
	if c := w.calls(t); len(c) != 1 || c[0].Status != "error" || c[0].Error == nil || c[0].HTTPStatus != nil {
		t.Errorf("recorded %+v", c)
	}
}
