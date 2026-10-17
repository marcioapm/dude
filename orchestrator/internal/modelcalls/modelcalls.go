// Package modelcalls keeps every call dude sends a model directly
// (model_calls, migration 139): what asked, on which tier, what was sent
// and what came back. The orchestrator writes; the backend reads.
package modelcalls

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/llm"
	"github.com/marciomartins/dude/orchestrator/internal/redact"
)

// PurposeJudgement is a call made by the judge.
const PurposeJudgement = "judgement"

// Call is one call as it is recorded.
type Call struct {
	Purpose, Asker string
	// "" stores NULL.
	About    string
	TaskID   *string
	TierID   *string
	TierName string
	// The model the call requested.
	Model string
	// What was sent; its headers are never stored.
	Sent       llm.Call
	Completion llm.Completion
	// The call's error; nil for a call that succeeded.
	Err error
	// The caller's reading of the answer, when it has one.
	Result any
}

// Record inserts c in tx's organisation and returns its id. The request,
// the answer, the result and the error go through red first (nil redacts
// nothing).
func Record(ctx context.Context, tx pgx.Tx, red *redact.Redactor, c Call) (string, error) {
	request, err := json.Marshal(c.Sent.Body())
	if err != nil {
		return "", err
	}
	request = red.JSON(request)
	var response, errText, about *string
	if c.Completion.Text != "" {
		s := red.String(c.Completion.Text)
		response = &s
	}
	status := "ok"
	if c.Err != nil {
		status = "error"
		s := red.String(c.Err.Error())
		errText = &s
	}
	if c.About != "" {
		about = &c.About
	}
	var result []byte
	if c.Result != nil {
		if result, err = json.Marshal(c.Result); err != nil {
			return "", err
		}
		result = red.JSON(result)
	}
	id := ids.New(ids.ModelCall)
	_, err = tx.Exec(ctx, `
		INSERT INTO model_calls (id, organization_id, purpose, asker, about, task_id, tier_id, tier_name, model,
		  request, response_text, result, status, error, http_status, latency_ms, input_tokens, output_tokens)
		VALUES ($1, current_organization_id(), $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)`,
		id, c.Purpose, c.Asker, about, c.TaskID, c.TierID, c.TierName, c.Model,
		request, response, result, status, errText, c.Completion.Status, c.Completion.LatencyMs,
		c.Completion.InputTokens, c.Completion.OutputTokens)
	if err != nil {
		return "", err
	}
	return id, nil
}
