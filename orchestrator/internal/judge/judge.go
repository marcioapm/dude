// Package judge asks a model one question and reads back a decision: yes,
// no or unsure, how sure it is, and why. The organisation picks the tier
// that answers (organizations.judgement_tier_id); with none, judgements are
// off. Every call sent is recorded in model_calls, answered or not.
//
// It fails closed: any error is a zero Judgement, and the caller does what
// it does without one.
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
	"github.com/marciomartins/dude/orchestrator/internal/modelcalls"
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

// Ask is one question, and who asks it.
type Ask struct {
	// The part of dude that asks, as code names it ("delivery.comment").
	Asker string
	// A short label for what it is about: "ACME-28 · acme#5477".
	About  string
	TaskID *string
	// One question a yes or no answers.
	Question string
	// What the model needs to answer it; may be empty.
	Context string
}

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

// systemPrompt is what the model is told before every question.
const systemPrompt = `You decide one question for dude, a software-delivery system that runs coding agents on tasks and pull requests.

Read the question and its context, then answer with only a JSON object, nothing before or after it:
{"decision":"yes"|"no"|"unsure","confidence":<a number from 0 to 1>,"reasoning":"<one or two sentences>"}

confidence is how sure you are of your decision. Answer "unsure" when the context does not settle the question.`

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

// call is the request a question on t is sent as. No temperature: the
// proxy refuses one for the Claude models ("Bad request", seen live on
// every tier). A non-reasoning model then samples at its provider's
// default, so a repeated question can differ; the answer is parsed
// tolerantly and a failure falls back, so nothing relies on it.
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
// it in an error), the tier's header values, which may be credentials,
// and, for a question about a task, its project's secret values.
func redactor(ctx context.Context, tx pgx.Tx, proxyKey string, taskID *string, headers map[string]string) (*redact.Redactor, error) {
	var list []redact.Secret
	if proxyKey != "" {
		list = append(list, redact.Secret{Name: "DUDE_LLM_KEY", Value: proxyKey})
	}
	for name, value := range headers {
		list = append(list, redact.Secret{Name: name, Value: value})
	}
	if taskID != nil {
		rows, err := tx.Query(ctx, `SELECT s.name, s.value FROM project_secrets s JOIN tasks t ON t.project_id = s.project_id WHERE t.id = $1`, *taskID)
		if err != nil {
			return nil, err
		}
		secrets, err := pgx.CollectRows(rows, pgx.RowToStructByPos[redact.Secret])
		if err != nil {
			return nil, err
		}
		list = append(list, secrets...)
	}
	if len(list) == 0 {
		return nil, nil
	}
	return redact.FromList(list), nil
}

// Judge asks the organisation's judgement tier ask's question, once, and
// records the call. The tier is read in one transaction and the call
// recorded in another: no transaction is held while the model thinks. The
// record is written under its own 5s bound, so a call sent is recorded
// even when ctx ends first.
func (s Service) Judge(ctx context.Context, org string, ask Ask) (Judgement, error) {
	var (
		tier Tier
		red  *redact.Redactor
	)
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) (err error) {
		if tier, err = orgTier(ctx, tx); err != nil {
			return err
		}
		red, err = redactor(ctx, tx, s.LLM.Key, ask.TaskID, tier.Headers)
		return err
	})
	if err != nil {
		return Judgement{}, err
	}
	sent := call(tier, ask.Question, ask.Context)
	sent.Timeout = s.Timeout
	j, done, askErr := decide(ctx, s.LLM, sent)
	rec := modelcalls.Call{Purpose: modelcalls.PurposeJudgement, Asker: ask.Asker, About: ask.About, TaskID: ask.TaskID,
		TierID: &tier.ID, TierName: tier.Name, Sent: sent, Completion: done, Err: askErr}
	if askErr == nil {
		rec.Result = j
	}
	recCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
	defer cancel()
	var id string
	if err := s.DB.InOrg(recCtx, org, func(tx pgx.Tx) (err error) {
		id, err = modelcalls.Record(recCtx, tx, red, rec)
		return err
	}); err != nil {
		return Judgement{}, fmt.Errorf("record the judgement: %w", err)
	}
	if askErr != nil {
		return Judgement{}, askErr
	}
	j.CallID = id
	return j, nil
}

// TestQuestion is the question an admin's test asks.
const TestQuestion = "Is the sky blue on a clear day?"

// Test asks t the test question as Judge would ask it, and records nothing.
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
