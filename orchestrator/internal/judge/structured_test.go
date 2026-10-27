package judge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
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
	if got.Answer.(route).Route != "task" || got.Summary.Decision != "task" || got.CallID == "" || got.Summary.CallID != got.CallID {
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
	down := setup(t, func(w http.ResponseWriter) { w.WriteHeader(http.StatusBadGateway) }, true)
	if _, err := down.svc.Structured(context.Background(), down.org, routing); err == nil || len(down.calls(t)) != 1 {
		t.Fatalf("a proxy error: %v", err)
	}
}
