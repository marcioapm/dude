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

// The system message the test question is sent with states the rules a
// parseable answer needs: the reply is the object alone, first character {
// and last }, and its reasoning is bounded in words with no braces, so it
// ends before maxTokens.
func TestTheSystemMessageStatesTheFormatRules(t *testing.T) {
	system := messages("Is it done?", "")[0].Content
	for _, rule := range []string{
		"Your whole reply is one JSON object and nothing else.",
		"Its first character is { and its last character is }.",
		"before or after it",
		"at most two sentences",
		"counted against the work you are judging",
		"under 60 words",
		"no { or } characters in it",
	} {
		if !strings.Contains(system, rule) {
			t.Errorf("the system message does not say %q:\n%s", rule, system)
		}
	}
}

// A decision is read tolerantly: a fence or prose around the object is
// skipped, braces inside its strings are kept.
func TestAFencedOrWrappedAnswerIsRead(t *testing.T) {
	for text, want := range map[string]Judgement{
		"```json\n{\"decision\":\"yes\",\"confidence\":0.8,\"reasoning\":\"Blue.\"}\n```": {Decision: Yes, Confidence: 0.8, Reasoning: "Blue."},
		"Here is my answer: {\"decision\":\"Unsure\",\"confidence\":0.4,\"reasoning\":\"Braces {like these} in text.\"} Hope it helps {}.": {
			Decision: Unsure, Confidence: 0.4, Reasoning: "Braces {like these} in text."},
	} {
		if got, err := parse(text); err != nil || got != want {
			t.Errorf("parse(%q) = %+v, %v; want %+v", text, got, err, want)
		}
	}
}

// An answer that is not a decision is ErrUnparseable.
func TestAnAnswerThatIsNotADecisionIsUnparseable(t *testing.T) {
	for name, text := range map[string]string{
		"bad decision":         `{"decision":"maybe","confidence":0.5,"reasoning":"x"}`,
		"confidence over 1":    `{"decision":"yes","confidence":1.5,"reasoning":"x"}`,
		"confidence below 0":   `{"decision":"yes","confidence":-0.1,"reasoning":"x"}`,
		"no confidence":        `{"decision":"yes","reasoning":"x"}`,
		"no object":            `Yes, it is.`,
		"confidence as string": `{"decision":"yes","confidence":"high","reasoning":"x"}`,
	} {
		if j, err := parse(text); !errors.Is(err, ErrUnparseable) || j != (Judgement{}) {
			t.Errorf("%s: judgement = %+v, err = %v", name, j, err)
		}
	}
}

func TestTheTestQuestionHasTheDefaultBound(t *testing.T) {
	sent := testCall(llm.Client{}, Tier{Model: "m"})
	if sent.Timeout != llm.TestTimeout {
		t.Fatalf("timeout = %s, want %s", sent.Timeout, llm.TestTimeout)
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
