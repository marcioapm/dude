// Package judge asks the organisation's judgement tier
// (organizations.judgement_tier_id) a structured question and reads back
// its answer: chat's routing (Structured). With no tier, judgements are
// off. Every call sent is recorded in model_calls, answered or not. An
// admin's test (Test) asks one yes-or-no question and records nothing.
//
// It fails closed: any error is a zero answer, and the caller does what it
// does without one.
package judge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/llm"
	"github.com/marciomartins/dude/orchestrator/internal/redact"
)

var (
	// ErrOff: the organisation has no judgement tier. Nothing was sent.
	ErrOff = errors.New("judgements are off: the organisation has no judgement tier")
	// ErrNoModel: the judgement tier names no model. Nothing was sent.
	ErrNoModel = errors.New("the judgement tier names no model")
	// ErrUnparseable: the model answered, but not with a valid decision.
	ErrUnparseable = errors.New("the model's answer is not a decision")
)

// Decisions a model may answer.
const (
	Yes    = "yes"
	No     = "no"
	Unsure = "unsure"
)

// Judgement is a model's answer. CallID is its row in model_calls.
type Judgement struct {
	Decision   string  `json:"decision"`
	Confidence float64 `json:"confidence"`
	Reasoning  string  `json:"reasoning"`
	CallID     string  `json:"-"`
}

// Service asks the organisation's judgement tier.
type Service struct {
	DB  *db.DB
	LLM llm.Client
	// Bounds each call; llm.CompleteTimeout when zero.
	Timeout time.Duration
}

// Tier is the tier a judgement is asked of.
type Tier struct {
	ID, Name, Model, Effort string
	Headers                 map[string]string
}

// systemPrompt is what the model is told before every question. The format
// rules are stated first and bluntly: a reply with prose before the object,
// or reasoning long enough to hit maxTokens, does not parse.
const systemPrompt = `You decide one question for dude, a software-delivery system that runs coding agents on tasks and pull requests.

` + onlyTheObject + ` A reply that does not parse as that object is thrown away and counted against the work you are judging.

{"decision":"yes"|"no"|"unsure","confidence":<a number from 0 to 1>,"reasoning":"<at most two sentences, under 60 words>"}

Do your thinking before you answer, not in the reply. Keep reasoning short: under 60 words, quoting at most a few words of the context, never a whole command or log, and no { or } characters in it.
confidence is how sure you are of your decision. Answer "unsure" when the context does not settle the question.`

// onlyTheObject is the format rule every judgement is told; a structured
// one's comes after its caller's instructions.
const onlyTheObject = `Your whole reply is one JSON object and nothing else. Its first character is { and its last character is }.
No prose, heading, code fence or thinking out loud before or after it.`

// recordTimeout bounds the write of a call's record, apart from the caller's ctx.
const recordTimeout = 5 * time.Second

// maxTokens bounds one judgement's answer, with room for a reasoning model's thinking.
const maxTokens = 1024

// messages are the messages a question is sent as: the fixed system
// message, then the question with its context under a heading.
func messages(question, context string) []llm.Message {
	user := strings.TrimSpace(question)
	if c := strings.TrimSpace(context); c != "" {
		user += "\n\n## Context\n\n" + c
	}
	return []llm.Message{{Role: "system", Content: systemPrompt}, {Role: "user", Content: user}}
}

// call omits temperature: the proxy answers 400 to one (seen for Claude and GPT tiers).
func call(t Tier, question, context string) llm.Call {
	return llm.Call{Model: t.Model, Effort: t.Effort, Headers: t.Headers, Messages: messages(question, context),
		MaxTokens: maxTokens}
}

// LoadTier reads a tier in tx's organisation: ErrNoModel when it names
// none; pgx.ErrNoRows when it is not the organisation's.
func LoadTier(ctx context.Context, tx pgx.Tx, id string) (Tier, error) {
	t := Tier{ID: id}
	var model *string
	if err := tx.QueryRow(ctx, `SELECT name, model, COALESCE(effort, ''), headers FROM model_tiers WHERE id = $1`, id).
		Scan(&t.Name, &model, &t.Effort, &t.Headers); err != nil {
		return t, err
	}
	if model == nil {
		return t, ErrNoModel
	}
	t.Model = *model
	return t, nil
}

// orgTier is the organisation's judgement tier: ErrOff without one.
func orgTier(ctx context.Context, tx pgx.Tx) (Tier, error) {
	var id *string
	if err := tx.QueryRow(ctx, `SELECT judgement_tier_id FROM organizations WHERE id = current_organization_id()`).Scan(&id); err != nil {
		return Tier{}, fmt.Errorf("read the judgement tier: %w", err)
	}
	if id == nil {
		return Tier{}, ErrOff
	}
	return LoadTier(ctx, tx, *id)
}

// redactor redacts what a call stores: the proxy's key (a proxy may echo
// it in an error) and the tier's header values, which may be credentials.
func redactor(proxyKey string, headers map[string]string) *redact.Redactor {
	var list []redact.Secret
	if proxyKey != "" {
		list = append(list, redact.Secret{Name: "DUDE_LLM_KEY", Value: proxyKey})
	}
	for name, value := range headers {
		list = append(list, redact.Secret{Name: name, Value: value})
	}
	if len(list) == 0 {
		return nil
	}
	return redact.FromList(list)
}

// TestQuestion is the question an admin's test asks.
const TestQuestion = "Is the sky blue on a clear day?"

// Test asks t the test question, as a yes-or-no judgement, and records
// nothing.
// It is bounded as the Models page's test message is (c.Timeout, else
// llm.TestTimeout), inside the backend's wait for a test.
func Test(ctx context.Context, c llm.Client, t Tier) (Judgement, llm.Completion, error) {
	return decide(ctx, c, testCall(c, t))
}

func testCall(c llm.Client, t Tier) llm.Call {
	sent := call(t, TestQuestion, "")
	sent.Timeout = c.Timeout
	if sent.Timeout == 0 {
		sent.Timeout = llm.TestTimeout
	}
	return sent
}

// decide sends one call and reads its answer as a decision. On
// ErrUnparseable the Completion keeps the model's text as it came.
func decide(ctx context.Context, c llm.Client, sent llm.Call) (Judgement, llm.Completion, error) {
	done, err := c.Complete(ctx, sent)
	if err != nil {
		return Judgement{}, done, err
	}
	j, err := parse(done.Text)
	if err != nil {
		return Judgement{}, done, err
	}
	return j, done, nil
}

// parse reads a decision from a model's text, tolerantly: a Markdown code
// fence around it and prose before or after it are skipped, and the first
// balanced JSON object is read. It must name a decision of yes, no or
// unsure and a confidence from 0 to 1.
func parse(text string) (Judgement, error) {
	obj := firstObject(text)
	if obj == "" {
		return Judgement{}, fmt.Errorf("%w: no JSON object in it", ErrUnparseable)
	}
	var raw struct {
		Decision   *string  `json:"decision"`
		Confidence *float64 `json:"confidence"`
		Reasoning  string   `json:"reasoning"`
	}
	if err := json.Unmarshal([]byte(obj), &raw); err != nil {
		return Judgement{}, fmt.Errorf("%w: %v", ErrUnparseable, err)
	}
	if raw.Decision == nil {
		return Judgement{}, fmt.Errorf("%w: it names no decision", ErrUnparseable)
	}
	d := strings.ToLower(strings.TrimSpace(*raw.Decision))
	if d != Yes && d != No && d != Unsure {
		return Judgement{}, fmt.Errorf("%w: decision %q is not yes, no or unsure", ErrUnparseable, *raw.Decision)
	}
	if raw.Confidence == nil {
		return Judgement{}, fmt.Errorf("%w: it names no confidence", ErrUnparseable)
	}
	if *raw.Confidence < 0 || *raw.Confidence > 1 {
		return Judgement{}, fmt.Errorf("%w: confidence %v is not between 0 and 1", ErrUnparseable, *raw.Confidence)
	}
	return Judgement{Decision: d, Confidence: *raw.Confidence, Reasoning: strings.TrimSpace(raw.Reasoning)}, nil
}

// firstObject is the first balanced {…} in text, braces inside JSON
// strings ignored; "" when there is none. A code fence needs no special
// case: its backticks are outside the object.
func firstObject(text string) string {
	start := strings.IndexByte(text, '{')
	for start >= 0 {
		depth, inString, escaped := 0, false, false
		for i := start; i < len(text); i++ {
			ch := text[i]
			switch {
			case escaped:
				escaped = false
			case inString && ch == '\\':
				escaped = true
			case ch == '"':
				inString = !inString
			case inString:
			case ch == '{':
				depth++
			case ch == '}':
				depth--
				if depth == 0 {
					return text[start : i+1]
				}
			}
		}
		next := strings.IndexByte(text[start+1:], '{')
		if next < 0 {
			return ""
		}
		start += 1 + next
	}
	return ""
}
