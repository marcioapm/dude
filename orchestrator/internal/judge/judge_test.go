package judge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/llm"
)

// proxy is a stand-in LLM proxy: it answers every request with handle, and
// keeps each request's body.
type proxy struct {
	mu     sync.Mutex
	bodies []map[string]any
}

func (p *proxy) serve(t *testing.T, handle func(w http.ResponseWriter)) llm.Client {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		p.mu.Lock()
		p.bodies = append(p.bodies, body)
		p.mu.Unlock()
		handle(w)
	}))
	t.Cleanup(srv.Close)
	return llm.Client{URL: srv.URL + "/v1", Key: "sk-test"}
}

func (p *proxy) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.bodies)
}

// answering is a proxy handler answering content as the model's text.
func answering(content string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		body, _ := json.Marshal(map[string]any{
			"model":   "gpt-6-sol-mini-2026",
			"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": content}}},
			"usage":   map[string]any{"prompt_tokens": 120, "completion_tokens": 30},
		})
		_, _ = w.Write(body)
	}
}

type world struct {
	svc    Service
	owner  *pgx.Conn
	org    string
	tierID string
	proxy  *proxy
}

// setup is an organisation whose Fast tier requests gpt-6-sol-mini at low
// effort with a header, judging when on.
func setup(t *testing.T, handle func(w http.ResponseWriter), on bool) world {
	t.Helper()
	app, owner := dbtest.Open(t)
	ctx := context.Background()
	org := dbtest.Org(t, owner)
	w := world{owner: owner, org: org, proxy: &proxy{}}
	if err := owner.QueryRow(ctx, `UPDATE model_tiers SET model = 'gpt-6-sol-mini', effort = 'low', headers = '{"X-Team": "acme-header-value"}'
		WHERE organization_id = $1 AND name = 'Fast' RETURNING id`, org).Scan(&w.tierID); err != nil {
		t.Fatal(err)
	}
	if on {
		if _, err := owner.Exec(ctx, `UPDATE organizations SET judgement_tier_id = $1 WHERE id = $2`, w.tierID, org); err != nil {
			t.Fatal(err)
		}
	}
	w.svc = Service{DB: app, LLM: w.proxy.serve(t, handle)}
	return w
}

type recorded struct {
	Status, TierName, Model, Asker string
	About, Error, Response         *string
	Result                         []byte
	Request                        []byte
	HTTPStatus                     *int
	TierID                         *string
	In, Out                        int
}

func (w world) calls(t *testing.T) []recorded {
	t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT status, tier_name, model, asker, about, error, response_text, result, request,
		http_status, tier_id, input_tokens, output_tokens FROM model_calls WHERE organization_id = $1 ORDER BY created_at`, w.org)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (c recorded, err error) {
		err = r.Scan(&c.Status, &c.TierName, &c.Model, &c.Asker, &c.About, &c.Error, &c.Response, &c.Result, &c.Request, &c.HTTPStatus, &c.TierID, &c.In, &c.Out)
		return c, err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

var ask = Ask{Asker: "delivery.comment", About: "ACME-28 · acme#5477", Question: "Should the fixer act on this comment?", Context: "> Looks good to me."}

func TestOffSendsAndRecordsNothing(t *testing.T) {
	w := setup(t, answering(`{"decision":"yes","confidence":1,"reasoning":"x"}`), false)
	j, err := w.svc.Judge(context.Background(), w.org, ask)
	if !errors.Is(err, ErrOff) || j != (Judgement{}) {
		t.Errorf("judgement = %+v, err = %v", j, err)
	}
	if n := w.proxy.count(); n != 0 {
		t.Errorf("%d requests sent", n)
	}
	if got := w.calls(t); len(got) != 0 {
		t.Errorf("recorded %v", got)
	}
}

func TestATierWithNoModelSendsAndRecordsNothing(t *testing.T) {
	w := setup(t, answering(`{"decision":"yes","confidence":1,"reasoning":"x"}`), true)
	if _, err := w.owner.Exec(context.Background(), `UPDATE model_tiers SET model = NULL WHERE id = $1`, w.tierID); err != nil {
		t.Fatal(err)
	}
	j, err := w.svc.Judge(context.Background(), w.org, ask)
	if !errors.Is(err, ErrNoModel) || j != (Judgement{}) {
		t.Errorf("judgement = %+v, err = %v", j, err)
	}
	if n := w.proxy.count(); n != 0 {
		t.Errorf("%d requests sent", n)
	}
	if got := w.calls(t); len(got) != 0 {
		t.Errorf("recorded %v", got)
	}
}

// A judgement is one call on the tier's model, effort and headers, with
// the fixed system message and the question then its context; the answer
// and the call are recorded, the header's value never.
func TestAJudgementIsOneCallRecorded(t *testing.T) {
	w := setup(t, answering(`{"decision":"no","confidence":0.93,"reasoning":"It asks for nothing to change."}`), true)
	j, err := w.svc.Judge(context.Background(), w.org, ask)
	if err != nil {
		t.Fatal(err)
	}
	if j.Decision != No || j.Confidence != 0.93 || j.Reasoning != "It asks for nothing to change." || !strings.HasPrefix(j.CallID, "mcl_") {
		t.Errorf("judgement = %+v", j)
	}
	if w.proxy.count() != 1 {
		t.Fatalf("%d requests", w.proxy.count())
	}
	body := w.proxy.bodies[0]
	if body["model"] != "gpt-6-sol-mini" || body["reasoning_effort"] != "low" || body["max_tokens"] != float64(1024) || body["temperature"] != float64(0) {
		t.Errorf("body = %v", body)
	}
	if _, has := body["response_format"]; has {
		t.Errorf("response_format sent: %v", body)
	}
	msgs := body["messages"].([]any)
	system := msgs[0].(map[string]any)
	user := msgs[1].(map[string]any)
	if system["role"] != "system" || system["content"] != systemPrompt {
		t.Errorf("system = %v", system)
	}
	if user["role"] != "user" || user["content"] != "Should the fixer act on this comment?\n\n## Context\n\n> Looks good to me." {
		t.Errorf("user = %v", user)
	}
	got := w.calls(t)
	if len(got) != 1 {
		t.Fatalf("recorded %d", len(got))
	}
	c := got[0]
	if c.Status != "ok" || c.TierName != "Fast" || c.Model != "gpt-6-sol-mini" || c.Asker != "delivery.comment" || *c.About != "ACME-28 · acme#5477" ||
		c.Error != nil || *c.HTTPStatus != 200 || *c.TierID != w.tierID || c.In != 120 || c.Out != 30 {
		t.Errorf("recorded %+v", c)
	}
	var result map[string]any
	_ = json.Unmarshal(c.Result, &result)
	if result["decision"] != "no" || result["confidence"] != 0.93 || result["reasoning"] != "It asks for nothing to change." {
		t.Errorf("result = %s", c.Result)
	}
	if strings.Contains(string(c.Request), "acme-header-value") || strings.Contains(string(c.Request), "X-Team") {
		t.Errorf("header stored: %s", c.Request)
	}
}

// What the model says is redacted with the tier's header values before it is kept.
func TestAHeaderValueEchoedIsRedacted(t *testing.T) {
	w := setup(t, answering(`{"decision":"yes","confidence":0.5,"reasoning":"saw acme-header-value"}`), true)
	if _, err := w.svc.Judge(context.Background(), w.org, ask); err != nil {
		t.Fatal(err)
	}
	c := w.calls(t)[0]
	if strings.Contains(*c.Response, "acme-header-value") || strings.Contains(string(c.Result), "acme-header-value") {
		t.Errorf("header value kept: %s / %s", *c.Response, c.Result)
	}
}

// A question about a task is kept redacted with its project's secret
// values, and names the task.
func TestAQuestionAboutATaskIsRedactedWithItsProjectsSecrets(t *testing.T) {
	w := setup(t, answering(`{"decision":"yes","confidence":0.7,"reasoning":"The key s3cr3t-value-42 is in the diff."}`), true)
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_judge', $1, 'Web', 'web', 'WEB')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_judge', $1, 'prj_judge', 1, 'T', 'G')`,
		`INSERT INTO project_secrets (project_id, organization_id, name, value, hint) VALUES ('prj_judge', $1, 'API_KEY', 's3cr3t-value-42', '-42')`,
	} {
		if _, err := w.owner.Exec(ctx, q, w.org); err != nil {
			t.Fatal(err)
		}
	}
	task := "wi_judge"
	a := ask
	a.TaskID = &task
	a.Context = "diff: API_KEY=s3cr3t-value-42"
	if _, err := w.svc.Judge(ctx, w.org, a); err != nil {
		t.Fatal(err)
	}
	var request, result []byte
	var response, taskID string
	if err := w.owner.QueryRow(ctx, `SELECT request, response_text, result, task_id FROM model_calls WHERE organization_id = $1`, w.org).
		Scan(&request, &response, &result, &taskID); err != nil {
		t.Fatal(err)
	}
	for what, text := range map[string]string{"request": string(request), "response": response, "result": string(result)} {
		if strings.Contains(text, "s3cr3t-value-42") || !strings.Contains(text, "[REDACTED:API_KEY]") {
			t.Errorf("%s not redacted: %s", what, text)
		}
	}
	if taskID != task {
		t.Errorf("task_id = %s", taskID)
	}
	// The model was sent the value: redaction is of what dude keeps.
	if !strings.Contains(w.proxy.bodies[0]["messages"].([]any)[1].(map[string]any)["content"].(string), "s3cr3t-value-42") {
		t.Errorf("sent = %v", w.proxy.bodies[0])
	}
}

func TestAFencedAnswerIsRead(t *testing.T) {
	w := setup(t, answering("```json\n{\"decision\":\"yes\",\"confidence\":0.8,\"reasoning\":\"Blue.\"}\n```"), true)
	j, err := w.svc.Judge(context.Background(), w.org, ask)
	if err != nil || j.Decision != Yes || j.Confidence != 0.8 {
		t.Errorf("judgement = %+v, err = %v", j, err)
	}
	if c := w.calls(t); len(c) != 1 || c[0].Status != "ok" {
		t.Errorf("recorded %+v", c)
	}
}

func TestProseAroundTheAnswerIsSkipped(t *testing.T) {
	w := setup(t, answering("Here is my answer: {\"decision\":\"Unsure\",\"confidence\":0.4,\"reasoning\":\"Braces {like these} in text.\"} Hope it helps {}."), true)
	j, err := w.svc.Judge(context.Background(), w.org, ask)
	if err != nil || j.Decision != Unsure || j.Confidence != 0.4 || j.Reasoning != "Braces {like these} in text." {
		t.Errorf("judgement = %+v, err = %v", j, err)
	}
	if c := w.calls(t); len(c) != 1 || c[0].Status != "ok" {
		t.Errorf("recorded %+v", c)
	}
}

// An answer that is not a decision is an error, recorded with the text as it came.
func TestAnAnswerThatIsNotADecisionIsRecordedAsAnError(t *testing.T) {
	for name, text := range map[string]string{
		"bad decision":         `{"decision":"maybe","confidence":0.5,"reasoning":"x"}`,
		"confidence over 1":    `{"decision":"yes","confidence":1.5,"reasoning":"x"}`,
		"confidence below 0":   `{"decision":"yes","confidence":-0.1,"reasoning":"x"}`,
		"no confidence":        `{"decision":"yes","reasoning":"x"}`,
		"no object":            `Yes, it is.`,
		"confidence as string": `{"decision":"yes","confidence":"high","reasoning":"x"}`,
	} {
		t.Run(name, func(t *testing.T) {
			w := setup(t, answering(text), true)
			j, err := w.svc.Judge(context.Background(), w.org, ask)
			if !errors.Is(err, ErrUnparseable) || j != (Judgement{}) {
				t.Errorf("judgement = %+v, err = %v", j, err)
			}
			got := w.calls(t)
			if len(got) != 1 {
				t.Fatalf("recorded %d", len(got))
			}
			c := got[0]
			if c.Status != "error" || c.Error == nil || !strings.HasPrefix(*c.Error, ErrUnparseable.Error()) || c.Response == nil || *c.Response != text ||
				c.Result != nil || *c.HTTPStatus != 200 {
				t.Errorf("recorded %+v (error %v)", c, c.Error)
			}
		})
	}
}

func TestAProxyErrorIsRecorded(t *testing.T) {
	w := setup(t, func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream is down"}}`))
	}, true)
	j, err := w.svc.Judge(context.Background(), w.org, ask)
	if err == nil || err.Error() != "the LLM proxy answered 502: upstream is down" || j != (Judgement{}) {
		t.Errorf("judgement = %+v, err = %v", j, err)
	}
	got := w.calls(t)
	if len(got) != 1 {
		t.Fatalf("recorded %d", len(got))
	}
	if c := got[0]; c.Status != "error" || *c.Error != "the LLM proxy answered 502: upstream is down" || *c.HTTPStatus != 502 || c.Response != nil || c.Result != nil {
		t.Errorf("recorded %+v", c)
	}
}

func TestATimeoutIsRecorded(t *testing.T) {
	release := make(chan struct{})
	w := setup(t, func(http.ResponseWriter) { <-release }, true)
	t.Cleanup(func() { close(release) })
	w.svc.Timeout = 200 * time.Millisecond
	j, err := w.svc.Judge(context.Background(), w.org, ask)
	if err == nil || err.Error() != "no answer from the LLM proxy in 200ms" || j != (Judgement{}) {
		t.Errorf("judgement = %+v, err = %v", j, err)
	}
	got := w.calls(t)
	if len(got) != 1 {
		t.Fatalf("recorded %d", len(got))
	}
	if c := got[0]; c.Status != "error" || *c.Error != "no answer from the LLM proxy in 200ms" || c.HTTPStatus != nil {
		t.Errorf("recorded %+v", c)
	}
}

// A call sent is recorded even when the caller's ctx ends before the model answers.
func TestACallerDeadlineStillRecordsTheCall(t *testing.T) {
	release := make(chan struct{})
	w := setup(t, func(http.ResponseWriter) { <-release }, true)
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	j, err := w.svc.Judge(ctx, w.org, ask)
	if err == nil || err.Error() != "no answer from the LLM proxy before the caller's deadline" || j != (Judgement{}) {
		t.Errorf("judgement = %+v, err = %v", j, err)
	}
	if n := w.proxy.count(); n != 1 {
		t.Errorf("%d requests sent", n)
	}
	got := w.calls(t)
	if len(got) != 1 {
		t.Fatalf("recorded %d", len(got))
	}
	if c := got[0]; c.Status != "error" || c.Error == nil || *c.Error != "no answer from the LLM proxy before the caller's deadline" || c.HTTPStatus != nil {
		t.Errorf("recorded %+v", c)
	}
}

// A proxy error that echoes the proxy's key or a project secret is stored redacted.
func TestAnEchoedKeyInAProxyErrorIsRedacted(t *testing.T) {
	w := setup(t, func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid key sk-test for a body naming s3cr3t-value-42"}}`))
	}, true)
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_judge', $1, 'Web', 'web', 'WEB')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_judge', $1, 'prj_judge', 1, 'T', 'G')`,
		`INSERT INTO project_secrets (project_id, organization_id, name, value, hint) VALUES ('prj_judge', $1, 'API_KEY', 's3cr3t-value-42', '-42')`,
	} {
		if _, err := w.owner.Exec(ctx, q, w.org); err != nil {
			t.Fatal(err)
		}
	}
	task := "wi_judge"
	a := ask
	a.TaskID = &task
	if _, err := w.svc.Judge(ctx, w.org, a); err == nil {
		t.Fatal("no error")
	}
	got := w.calls(t)
	if len(got) != 1 || got[0].Error == nil {
		t.Fatalf("recorded %+v", got)
	}
	want := "the LLM proxy answered 401: invalid key [REDACTED:DUDE_LLM_KEY] for a body naming [REDACTED:API_KEY]"
	if *got[0].Error != want {
		t.Errorf("error = %q, want %q", *got[0].Error, want)
	}
}

// The test question is bounded by the client's Timeout when it names one.
func TestTheTestQuestionIsBoundedByTheClientsTimeout(t *testing.T) {
	release := make(chan struct{})
	w := setup(t, func(http.ResponseWriter) { <-release }, false)
	t.Cleanup(func() { close(release) })
	c := w.svc.LLM
	c.Timeout = 200 * time.Millisecond
	start := time.Now()
	_, _, err := Test(context.Background(), c, Tier{ID: w.tierID, Name: "Fast", Model: "gpt-6-sol-mini"})
	if err == nil || err.Error() != "no answer from the LLM proxy in 200ms" {
		t.Errorf("err = %v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("took %s", took)
	}
}

// The test question goes the same way and is not recorded.
func TestTheTestQuestionIsNotRecorded(t *testing.T) {
	w := setup(t, answering(`{"decision":"yes","confidence":0.99,"reasoning":"Rayleigh scattering."}`), false)
	var tier Tier
	if err := w.svc.DB.InOrg(context.Background(), w.org, func(tx pgx.Tx) (err error) {
		tier, err = LoadTier(context.Background(), tx, w.tierID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	j, done, err := Test(context.Background(), w.svc.LLM, tier)
	if err != nil || j.Decision != Yes || done.InputTokens != 120 {
		t.Errorf("judgement = %+v, %+v, err = %v", j, done, err)
	}
	user := w.proxy.bodies[0]["messages"].([]any)[1].(map[string]any)
	if user["content"] != TestQuestion {
		t.Errorf("user = %v", user)
	}
	if got := w.calls(t); len(got) != 0 {
		t.Errorf("recorded %v", got)
	}
}
