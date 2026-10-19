package phases

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/judge"
	"github.com/marciomartins/dude/orchestrator/internal/llm"
)

// proxyAnswering is an LLM proxy whose model answers text, or never
// answers within the call's bound (hang).
func proxyAnswering(t *testing.T, text string, hang bool) llm.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hang {
			// Longer than the call's bound; then gone, so Close returns.
			select {
			case <-r.Context().Done():
			case <-time.After(2 * time.Second):
			}
			return
		}
		body, _ := json.Marshal(map[string]any{"model": "judge-model",
			"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": text}}},
			"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5}})
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return llm.Client{URL: srv.URL + "/v1", Key: "sk-test"}
}

// judgedThroughTheService judges the harness Run's passing report with the
// real judge.Service over proxy, the organisation's tier on when on.
func judgedThroughTheService(t *testing.T, client llm.Client, on bool) (checksDecision, *harnessWorld) {
	t.Helper()
	w := newHarnessWorld(t)
	checksReply(w, passingChecks)
	ctx := context.Background()
	if on {
		if _, err := w.owner.Exec(ctx, `UPDATE model_tiers SET model = 'judge-model' WHERE organization_id = $1 AND name = 'Fast'`, w.run.Org); err != nil {
			t.Fatal(err)
		}
		if _, err := w.owner.Exec(ctx, `UPDATE organizations SET judgement_tier_id =
			(SELECT id FROM model_tiers WHERE organization_id = $1 AND name = 'Fast') WHERE id = $1`, w.run.Org); err != nil {
			t.Fatal(err)
		}
	}
	w.s.Judge = &judge.Service{DB: w.s.DB, LLM: client, Timeout: 300 * time.Millisecond}
	d, err := w.s.judgeChecks(ctx, w.run)
	if err != nil {
		t.Fatal(err)
	}
	return d, w
}

func (w *harnessWorld) modelCalls() int {
	w.t.Helper()
	var n int
	if err := w.owner.QueryRow(context.Background(), `SELECT count(*) FROM model_calls WHERE organization_id = $1`, w.run.Org).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

// Through the real judge.Service: no tier skips and sends nothing; a model
// that answers yes and no as asked verifies, each call recorded with its
// CallID; an unparseable answer and a timeout are not verified.
func TestChecksThroughTheJudgeService(t *testing.T) {
	t.Run("no tier", func(t *testing.T) {
		d, w := judgedThroughTheService(t, proxyAnswering(t, `{"decision":"yes","confidence":1}`, false), false)
		if !d.Verified || !d.Skipped || w.modelCalls() != 0 {
			t.Fatalf("decision %+v, %d calls", d, w.modelCalls())
		}
	})
	t.Run("unparseable", func(t *testing.T) {
		d, w := judgedThroughTheService(t, proxyAnswering(t, "It looks fine to me.", false), true)
		if d.Verified || d.Skipped || w.modelCalls() == 0 {
			t.Fatalf("decision %+v, %d calls", d, w.modelCalls())
		}
	})
	t.Run("timeout", func(t *testing.T) {
		d, w := judgedThroughTheService(t, proxyAnswering(t, "", true), true)
		if d.Verified || d.Skipped || w.modelCalls() == 0 {
			t.Fatalf("decision %+v, %d calls", d, w.modelCalls())
		}
	})
	t.Run("unsure", func(t *testing.T) {
		d, _ := judgedThroughTheService(t, proxyAnswering(t, `{"decision":"unsure","confidence":0.5,"reasoning":"no output"}`, false), true)
		if d.Verified {
			t.Fatalf("decision %+v", d)
		}
	})
	t.Run("recorded calls", func(t *testing.T) {
		d, w := judgedThroughTheService(t, proxyAnswering(t, `{"decision":"no","confidence":0.9,"reasoning":"x"}`, false), true)
		// No to Backed fails the check; each question's call is recorded and named.
		if d.Verified || len(d.Verdicts) != 2 || w.modelCalls() != 2 {
			t.Fatalf("decision %+v, %d calls", d, w.modelCalls())
		}
		for _, v := range d.Verdicts {
			if v.CallID == "" {
				t.Errorf("verdict %+v has no CallID", v)
			}
		}
	})
}
