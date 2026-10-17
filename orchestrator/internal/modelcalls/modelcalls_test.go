package modelcalls

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/llm"
	"github.com/marciomartins/dude/orchestrator/internal/redact"
)

func tierOf(t *testing.T, owner *pgx.Conn, org string) (id, name string) {
	t.Helper()
	if err := owner.QueryRow(context.Background(), `SELECT id, name FROM model_tiers WHERE organization_id = $1 ORDER BY position LIMIT 1`, org).
		Scan(&id, &name); err != nil {
		t.Fatal(err)
	}
	return id, name
}

// A call is stored with what was sent and what came back, redacted, and
// never the tier's headers.
func TestACallIsRecordedRedactedWithoutItsHeaders(t *testing.T) {
	app, owner := dbtest.Open(t)
	ctx := context.Background()
	org := dbtest.Org(t, owner)
	tierID, tierName := tierOf(t, owner, org)
	status := 200
	red := redact.New(map[string]string{"API_TOKEN": "tok-0123456789abcdef"})
	c := Call{
		Purpose: PurposeJudgement, Asker: "delivery.escalation", About: "ACME-1 · acme#7", TierID: &tierID, TierName: tierName, Model: "gpt-6",
		Sent: llm.Call{Model: "gpt-6", Headers: map[string]string{"X-Secret-Header": "hdr-value-never-stored"},
			Messages: []llm.Message{{Role: "user", Content: "Is tok-0123456789abcdef a token?"}}, MaxTokens: 1024},
		Completion: llm.Completion{Text: "it was tok-0123456789abcdef", Status: &status, LatencyMs: 42, InputTokens: 9, OutputTokens: 4},
		Result:     map[string]any{"decision": "yes"},
	}
	var id string
	err := app.InOrg(ctx, org, func(tx pgx.Tx) (err error) {
		id, err = Record(ctx, tx, red, c)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, "mcl_") {
		t.Errorf("id = %s", id)
	}
	var (
		gotOrg, purpose, asker, about, tid, tname, model, st string
		request, result                                      []byte
		response                                             *string
		errText                                              *string
		httpStatus, latency, in, out                         int
	)
	if err := owner.QueryRow(ctx, `SELECT organization_id, purpose, asker, about, tier_id, tier_name, model, request, response_text, result,
		status, error, http_status, latency_ms, input_tokens, output_tokens FROM model_calls WHERE id = $1`, id).
		Scan(&gotOrg, &purpose, &asker, &about, &tid, &tname, &model, &request, &response, &result, &st, &errText, &httpStatus, &latency, &in, &out); err != nil {
		t.Fatal(err)
	}
	if gotOrg != org || purpose != "judgement" || asker != "delivery.escalation" || about != "ACME-1 · acme#7" || tid != tierID || tname != tierName ||
		model != "gpt-6" || st != "ok" || errText != nil || httpStatus != 200 || latency != 42 || in != 9 || out != 4 {
		t.Errorf("row = %s %s %s %s %s %s %s %s %v %d %d %d %d", gotOrg, purpose, asker, about, tid, tname, model, st, errText, httpStatus, latency, in, out)
	}
	if strings.Contains(string(request), "tok-0123456789abcdef") || !strings.Contains(string(request), "[REDACTED:API_TOKEN]") {
		t.Errorf("request not redacted: %s", request)
	}
	if strings.Contains(string(request), "hdr-value-never-stored") || strings.Contains(string(request), "X-Secret-Header") {
		t.Errorf("headers stored: %s", request)
	}
	var req map[string]any
	_ = json.Unmarshal(request, &req)
	if req["model"] != "gpt-6" || req["max_tokens"] != float64(1024) {
		t.Errorf("request = %s", request)
	}
	if response == nil || *response != "it was [REDACTED:API_TOKEN]" {
		t.Errorf("response = %v", response)
	}
	if string(result) != `{"decision": "yes"}` {
		t.Errorf("result = %s", result)
	}
}

// A failed call is status error with its message, and no answer or status
// when the proxy gave none.
func TestAFailedCallIsRecordedAsAnError(t *testing.T) {
	app, owner := dbtest.Open(t)
	ctx := context.Background()
	org := dbtest.Org(t, owner)
	_, tierName := tierOf(t, owner, org)
	err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
		_, err := Record(ctx, tx, nil, Call{Purpose: PurposeJudgement, Asker: "test", TierName: tierName, Model: "m",
			Sent: llm.Call{Model: "m"}, Completion: llm.Completion{LatencyMs: 7}, Err: errors.New("no answer from the LLM proxy in 1m0s")})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var st string
	var errText, response, about *string
	var httpStatus *int
	var result []byte
	if err := owner.QueryRow(ctx, `SELECT status, error, response_text, about, http_status, result FROM model_calls WHERE organization_id = $1`, org).
		Scan(&st, &errText, &response, &about, &httpStatus, &result); err != nil {
		t.Fatal(err)
	}
	if st != "error" || errText == nil || *errText != "no answer from the LLM proxy in 1m0s" || response != nil || about != nil || httpStatus != nil || result != nil {
		t.Errorf("row = %s %v %v %v %v %s", st, errText, response, about, httpStatus, result)
	}
}

// A call cannot name another organisation's tier or task, and another
// organisation does not see it.
func TestACallIsItsOrganisations(t *testing.T) {
	app, owner := dbtest.Open(t)
	ctx := context.Background()
	org, other := dbtest.Org(t, owner), dbtest.Org(t, owner)
	otherTier, _ := tierOf(t, owner, other)
	err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
		_, err := Record(ctx, tx, nil, Call{Purpose: PurposeJudgement, Asker: "test", TierID: &otherTier, TierName: "x", Model: "m", Sent: llm.Call{Model: "m"}})
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "foreign key") {
		t.Errorf("another organisation's tier: err = %v", err)
	}
	if err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
		_, err := Record(ctx, tx, nil, Call{Purpose: PurposeJudgement, Asker: "test", TierName: "x", Model: "m", Sent: llm.Call{Model: "m"}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := app.InOrg(ctx, other, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM model_calls`).Scan(&n)
	}); err != nil || n != 0 {
		t.Errorf("another organisation sees %d calls (%v)", n, err)
	}
}

// Removing the tier keeps the call, with the tier's name then.
func TestACallOutlivesItsTier(t *testing.T) {
	app, owner := dbtest.Open(t)
	ctx := context.Background()
	org := dbtest.Org(t, owner)
	tierID, tierName := tierOf(t, owner, org)
	if err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
		_, err := Record(ctx, tx, nil, Call{Purpose: PurposeJudgement, Asker: "test", TierID: &tierID, TierName: tierName, Model: "m", Sent: llm.Call{Model: "m"}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `UPDATE organizations SET default_agent_models = '{}' WHERE id = $1`, org); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `DELETE FROM model_tiers WHERE id = $1`, tierID); err != nil {
		t.Fatal(err)
	}
	var tid *string
	var name, gotOrg string
	if err := owner.QueryRow(ctx, `SELECT tier_id, tier_name, organization_id FROM model_calls`).Scan(&tid, &name, &gotOrg); err != nil {
		t.Fatal(err)
	}
	if tid != nil || name != tierName || gotOrg != org {
		t.Errorf("after removal: %v %s %s", tid, name, gotOrg)
	}
}
