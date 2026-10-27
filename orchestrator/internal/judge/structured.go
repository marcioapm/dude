package judge

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/llm"
	"github.com/marciomartins/dude/orchestrator/internal/modelcalls"
	"github.com/marciomartins/dude/orchestrator/internal/redact"
)

// A structured judgement is a judgement whose answer is a JSON object the
// caller defines (chat routing's route, project, tasks…) rather than yes,
// no or unsure: the same tier, the same proxy call, the same redaction and
// the same model_calls record (purpose judgement), with the caller's own
// instructions as the system message and its parse of the answer.

// StructuredAsk is one structured question.
type StructuredAsk struct {
	Asker, About string
	// The system message: what the model is and what JSON it answers with.
	Instructions string
	// The user message: the case to decide.
	Input string
	// Bounds the answer; 0 is 2048.
	MaxTokens int
	// Parse reads the model's JSON object (the first balanced one of its
	// text) into the caller's answer; an error makes the call
	// ErrUnparseable. Summary is what Recent judgements shows of it.
	Parse func(object []byte) (answer any, summary Judgement, err error)
}

// Structured is the answer: the caller's, the judgement summary recorded,
// and the call's row.
type Structured struct {
	Answer  any
	Summary Judgement
	CallID  string
}

// Structured asks the organisation's judgement tier a structured question,
// once, and records it as Judge records a question. ErrOff and ErrNoModel
// send nothing and record nothing; any other failure is recorded.
func (s Service) Structured(ctx context.Context, org string, ask StructuredAsk) (Structured, error) {
	var (
		tier Tier
		red  *redact.Redactor
	)
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) (err error) {
		if tier, err = orgTier(ctx, tx); err != nil {
			return err
		}
		red, err = redactor(ctx, tx, s.LLM.Key, nil, tier.Headers)
		return err
	})
	if err != nil {
		return Structured{}, err
	}
	maxTok := ask.MaxTokens
	if maxTok == 0 {
		maxTok = 2048
	}
	// The caller's instructions, then the format rule every judgement is told.
	system := strings.TrimSpace(ask.Instructions) + "\n\n" + onlyTheObject + " A reply that does not parse as that object is thrown away."
	sent := llm.Call{Model: tier.Model, Effort: tier.Effort, Headers: tier.Headers, MaxTokens: maxTok,
		Timeout: s.Timeout, Messages: []llm.Message{{Role: "system", Content: system}, {Role: "user", Content: strings.TrimSpace(ask.Input)}}}
	done, askErr := s.LLM.Complete(ctx, sent)
	var out Structured
	if askErr == nil {
		obj := firstObject(done.Text)
		if obj == "" {
			askErr = fmt.Errorf("%w: no JSON object in it", ErrUnparseable)
		} else if out.Answer, out.Summary, askErr = ask.Parse([]byte(obj)); askErr != nil {
			askErr = fmt.Errorf("%w: %v", ErrUnparseable, askErr)
		}
	}
	rec := modelcalls.Call{Purpose: modelcalls.PurposeJudgement, Asker: ask.Asker, About: ask.About, TierID: &tier.ID,
		TierName: tier.Name, Sent: sent, Completion: done, Err: askErr}
	if askErr == nil {
		rec.Result = out.Summary
	}
	recCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
	defer cancel()
	if err := s.DB.InOrg(recCtx, org, func(tx pgx.Tx) (err error) {
		out.CallID, err = modelcalls.Record(recCtx, tx, red, rec)
		return err
	}); err != nil {
		return Structured{}, fmt.Errorf("record the judgement: %w", err)
	}
	if askErr != nil {
		return Structured{CallID: out.CallID}, askErr
	}
	out.Summary.CallID = out.CallID
	return out, nil
}
