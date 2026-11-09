package delivery

import (
	"strings"
	"testing"
)

// openAskKinds is each kind of ask OpenAsk counts, as SQL that opens one
// for run_held of newHeldTask ($1 its organization), with raisedAt for a
// checks escalation.
var openAskKinds = []struct{ name, open string }{
	{"a question", `INSERT INTO questions (id, organization_id, task_id, run_id, prompt, status) VALUES ('q_k', $1, 'wi_h', 'run_held', 'Sorted?', 'open')`},
	{"a blocking repository request", `INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES ('repo_k', $1, 'prj_h', 'r', 'git://x/r');
		INSERT INTO repository_requests (id, organization_id, task_id, run_id, repository_id, reason, blocking) VALUES ('rr_k', $1, 'wi_h', 'run_held', 'repo_k', 'needs it', true)`},
	{"a checks escalation", `UPDATE workflow_runs SET state = state || jsonb_build_object('escalation', jsonb_build_object('reason', '` + ReasonChecksUnverified + `',
		'raisedAt', now(), 'detail', jsonb_build_object('runId', 'run_held'))) WHERE id = 'wf_h' AND organization_id = $1`},
	{"a peer wait", `INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_p', $1, 'prj_h', 2, 'P', 'P');
		INSERT INTO peer_messages (id, organization_id, from_task_id, from_run_id, to_task_id, text) VALUES ('pm_k', $1, 'wi_h', 'run_held', 'wi_p', 'hi');
		INSERT INTO peer_waits (id, organization_id, run_id, message_id, how) VALUES ('pw_k', $1, 'run_held', 'pm_k', 'wait')`},
	{"the gate's checks asked", `INSERT INTO events (id, organization_id, run_id, event_type, actor_type, actor_id, source, payload)
		VALUES ('ev_k', $1, 'run_held', '` + EvRunChecksAsked + `', 'system', 'dude', 'orchestrator', jsonb_build_object('turn', 0, 'deadline', now() + interval '1 hour'))`},
}

// OpenAskSince says when every ask OpenAsk counts was made: each kind
// alone is open (OpenAsk) and has a time (OpenAskSince), so an ask added to
// one and not the other fails here. And OpenAsk has no ask this list lacks.
func TestEveryOpenAskSaysWhenItWasMade(t *testing.T) {
	if n := strings.Count(OpenAsk, "EXISTS (SELECT 1 FROM ") - strings.Count(OpenAsk, "NOT EXISTS (SELECT 1 FROM "); n != len(openAskKinds) {
		t.Fatalf("OpenAsk reads %d kinds of ask, the test %d: add the new one to openAskKinds (and OpenAskSince)", n, len(openAskKinds))
	}
	for _, k := range openAskKinds {
		t.Run(k.name, func(t *testing.T) {
			h := newHeldTask(t)
			for _, q := range strings.Split(k.open, ";") {
				h.exec(q, h.org)
			}
			var open, since bool
			if err := h.owner.QueryRow(h.ctx, `SELECT `+OpenAsk+`, (`+OpenAskSince+`) IS NOT NULL FROM runs r WHERE r.id = 'run_held'`).Scan(&open, &since); err != nil {
				t.Fatal(err)
			}
			if !open || !since {
				t.Errorf("OpenAsk %v, OpenAskSince set %v; want both", open, since)
			}
		})
	}
}
