package delivery

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// A conductor's request_limit: the task's people are asked, through the
// conductor's question, to raise one of the limits its delivery holds it
// to, for this delivery only. The request waits in the delivery's state
// (LimitRequests) under its question's id; an approval by someone who may
// change the setting appends a LimitGrant, which the checks read through
// RoundFixLimit, PullRequestFixLimit and RerunsAllowed.

// Ledger events of limit requests. limit.requested: {questionId, limit,
// from, to, repo?, number?, head?, reason}, by the conductor's Run.
// limit.granted: the same and {extra, by, byName}, by the approver.
const (
	EvLimitRequested = "limit.requested"
	EvLimitGranted   = "limit.granted"
)

// LimitRequest is a request_limit waiting on its question's answer.
type LimitRequest struct {
	QuestionID string `json:"questionId"`
	Limit      string `json:"limit"`
	From       int    `json:"from"`
	To         int    `json:"to"`
	Repo       string `json:"repo,omitempty"`
	Number     int    `json:"number,omitempty"`
	Head       string `json:"head,omitempty"`
	Reason     string `json:"reason"`
}

// Extra is what the request adds to the limit.
func (r LimitRequest) Extra() int { return r.To - r.From }

// LimitAsk is what request_limit asks: the limit, the value the fix-round
// limits should become, the pull request a rerun is for, and why.
type LimitAsk struct {
	Limit  string
	To     int
	PR     string
	Reason string
}

// Bounds of one request: how far it may raise a fix-round limit, and its
// reason's length.
const (
	LimitRaiseMax  = 10
	LimitReasonMax = 2000
)

// LimitApprovalError is an approval by someone who may not raise the limit:
// the API answers it 403, and the answer is not recorded.
type LimitApprovalError struct{ Msg string }

func (e LimitApprovalError) Error() string { return e.Msg }

var (
	limitChoicesTmpl  = mustTemplate("results/limit-request/choice")
	limitQuestionTmpl = mustTemplate("results/limit-request/question")
	limitNo           = render(mustTemplate("results/limit-request/no"), nil)
	limitRefusedTmpl  = mustTemplate("results/limit-request/not-allowed")
)

// limitChoice is the approving choice's label for r.
func limitChoice(r LimitRequest) string { return render(limitChoicesTmpl, r) }

// RequestLimitTx records the conductor's request to raise limit and asks
// the task's people, as its question; returns the question's id. Refused
// unless the caller is the task's live conductor, the delivery is live,
// the limit is one it may raise, and no request for it is open.
func RequestLimitTx(ctx context.Context, tx pgx.Tx, ref RunRef, in LimitAsk) (string, error) {
	in.Reason = strings.TrimSpace(in.Reason)
	switch {
	case !slices.Contains(LimitKeys, in.Limit):
		return "", refusef(rLimitRequestUnknownLimit, in.Limit, strings.Join(LimitKeys, ", "))
	case in.Reason == "":
		return "", refuseText(rLimitRequestReasonRequired)
	case len([]rune(in.Reason)) > LimitReasonMax:
		return "", refusef(rLimitRequestReasonTooLong, LimitReasonMax)
	}
	if err := liveConductor(ctx, tx, ref); err != nil {
		return "", err
	}
	d, err := LoadDelivery(ctx, tx, ref.TaskID)
	if err != nil {
		return "", err
	}
	switch {
	case d == nil || !d.Live():
		return "", refuseText(rLimitRequestNoDelivery)
	case Ended(d.TaskStatus):
		return "", refusef(rLimitRequestTaskEnded, d.TaskStatus)
	}
	st := &d.State
	for _, r := range st.LimitRequests {
		if r.Limit != in.Limit {
			continue
		}
		var open bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM questions WHERE id = $1 AND status = 'open')`, r.QuestionID).Scan(&open); err != nil {
			return "", err
		}
		if open {
			return "", refusef(rLimitRequestAlreadyOpen, in.Limit, r.QuestionID)
		}
	}
	// Like ask_person, it is the Run's one open question.
	if open, err := HasOpenQuestion(ctx, tx, ref.RunID); err != nil {
		return "", err
	} else if open {
		return "", refuseText(rLimitRequestQuestionOpen)
	}
	req := LimitRequest{Limit: in.Limit, Reason: in.Reason}
	switch in.Limit {
	case LimitRoundFixes, LimitPullRequestFixes:
		req.From = st.RoundFixLimit()
		if in.Limit == LimitPullRequestFixes {
			settings, err := GitHubSettingsTx(ctx, tx)
			if err != nil {
				return "", err
			}
			if settings.FixRoundsPerPR <= 0 {
				return "", refuseText(rLimitRequestNoBound)
			}
			req.From = st.PullRequestFixLimit(settings.FixRoundsPerPR)
		}
		if in.To <= req.From || in.To > req.From+LimitRaiseMax {
			return "", refusef(rLimitRequestToOutOfRange, req.From, req.From+1, req.From+LimitRaiseMax)
		}
		req.To = in.To
	case LimitRerun:
		pr, err := rerunTarget(ctx, tx, ref.TaskID, in.PR)
		if err != nil {
			return "", err
		}
		req.Repo, req.Number, req.Head = pr.Repo, pr.Number, pr.Head
		used, err := RerunsAtHeadTx(ctx, tx, ref.TaskID, pr.Repo, pr.Number, pr.Head)
		if err != nil {
			return "", err
		}
		req.From = RerunsAllowed(st.LimitGrants, pr.Repo, pr.Number, pr.Head)
		if used < req.From {
			return "", refusef(rLimitRequestRerunLeft, pr.Repo, pr.Number)
		}
		req.To = req.From + 1
	}
	item := QuestionItem{Question: render(limitQuestionTmpl, req),
		Choices: []Choice{{Label: limitChoice(req)}, {Label: limitNo}}}
	items, err := NormalizeItems([]QuestionItem{item})
	if err != nil {
		return "", err
	}
	id, err := AskItemsTx(ctx, tx, ref, items)
	if err != nil {
		return "", err
	}
	req.QuestionID = id
	requests := append(slices.DeleteFunc(slices.Clone(st.LimitRequests), func(r LimitRequest) bool { return r.Limit == in.Limit }), req)
	if err := setLimitState(ctx, tx, d.WorkflowID, st.LimitGrants, requests); err != nil {
		return "", err
	}
	_, err = ledger.Append(ctx, tx, ref.Event(EvLimitRequested, ledger.ActorAgent, req.payload()))
	return id, err
}

func (r LimitRequest) payload() map[string]any {
	p := map[string]any{"questionId": r.QuestionID, "limit": r.Limit, "from": r.From, "to": r.To, "reason": r.Reason}
	if r.Limit == LimitRerun {
		p["repo"], p["number"], p["head"] = r.Repo, r.Number, r.Head
	}
	return p
}

// rerunTarget is the open pull request a rerun request names (repo#n, #n
// or n), or the task's only open one when it names none.
func rerunTarget(ctx context.Context, tx pgx.Tx, taskID, named string) (LimitsPR, error) {
	repo, number := "", 0
	if strings.TrimSpace(named) != "" {
		var err error
		if repo, number, err = parsePR(named); err != nil {
			return LimitsPR{}, err
		}
	}
	rows, err := tx.Query(ctx, `SELECT r.name, pr.number, COALESCE(pr.head_sha, '')
		FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id
		WHERE pr.task_id = $1 AND pr.state IN ('open', 'draft') AND ($2 = 0 OR pr.number = $2) AND ($3 = '' OR r.name = $3)
		ORDER BY pr.created_at, pr.id`, taskID, number, repo)
	if err != nil {
		return LimitsPR{}, err
	}
	prs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (LimitsPR, error) {
		var p LimitsPR
		return p, r.Scan(&p.Repo, &p.Number, &p.Head)
	})
	switch {
	case err != nil:
		return LimitsPR{}, err
	case len(prs) == 0:
		return LimitsPR{}, refuseText(rLimitRequestNoPullRequest)
	case len(prs) > 1:
		return LimitsPR{}, refuseText(rLimitRequestNamePullRequest)
	case prs[0].Head == "":
		return LimitsPR{}, refusef(rLimitRequestNoHead, prs[0].Repo, prs[0].Number)
	}
	return prs[0], nil
}

// setLimitState writes the delivery's grants and requests, keys a step's
// transition keeps as the row holds them (Workflow's Owned).
func setLimitState(ctx context.Context, tx pgx.Tx, wfID string, grants []LimitGrant, requests []LimitRequest) error {
	g, _ := json.Marshal(grants)
	r, _ := json.Marshal(requests)
	if len(grants) == 0 {
		g = []byte("null")
	}
	if len(requests) == 0 {
		r = []byte("null")
	}
	_, err := tx.Exec(ctx, `UPDATE workflow_runs SET state = (state - 'limitGrants' - 'limitRequests')
			|| jsonb_strip_nulls(jsonb_build_object('limitGrants', $2::jsonb, 'limitRequests', $3::jsonb))
		WHERE id = $1`, wfID, g, r)
	return err
}

// IsLimitRequestTx says the question is a limit request of the task's
// delivery still waiting on its answer.
func IsLimitRequestTx(ctx context.Context, tx pgx.Tx, taskID, questionID string) (bool, error) {
	d, err := ReadDelivery(ctx, tx, taskID)
	if err != nil || d == nil {
		return false, err
	}
	return slices.ContainsFunc(d.State.LimitRequests, func(r LimitRequest) bool { return r.QuestionID == questionID }), nil
}

// Approver is who answered a limit request: the person, and whether they
// administer the organisation, which raising a fix-round limit takes.
type Approver struct {
	Person, ActorType, ActorID string
	Admin                      bool
}

// mayRaise says whether a holds the permission the limit's setting takes:
// a fix-round limit is the delivery policy's or the organisation's GitHub
// settings', both an organisation admin's to change; a rerun is a person's
// action on the pull request, which the answer route already allowed a
// (the task's owner or an admin).
func (a Approver) mayRaise(limit string) bool {
	return limit == LimitRerun || a.Admin
}

// LimitAnswerTx settles a limit request with a person's answer to its
// question: the approving choice, by someone who may raise the limit,
// appends the grant (and limit.granted); any other answer drops the request
// and grants nothing. A question that is no limit request is left alone.
func LimitAnswerTx(ctx context.Context, tx pgx.Tx, ref RunRef, questionID string, a Answered, by Approver) error {
	d, err := LoadDelivery(ctx, tx, ref.TaskID)
	if err != nil || d == nil {
		return err
	}
	st := &d.State
	i := slices.IndexFunc(st.LimitRequests, func(r LimitRequest) bool { return r.QuestionID == questionID })
	if i < 0 {
		return nil
	}
	req := st.LimitRequests[i]
	requests := slices.Delete(slices.Clone(st.LimitRequests), i, i+1)
	approved := len(a.Answers) == 1 && slices.Equal(a.Answers[0].Choices, []int{0}) && a.Answers[0].Text == ""
	if !approved {
		return setLimitState(ctx, tx, d.WorkflowID, st.LimitGrants, requests)
	}
	if !by.mayRaise(req.Limit) {
		return LimitApprovalError{render(limitRefusedTmpl, req)}
	}
	name, err := PersonName(ctx, tx, by.Person)
	if err != nil {
		return err
	}
	g := LimitGrant{Limit: req.Limit, Extra: req.Extra(), Repo: req.Repo, Number: req.Number, Head: req.Head,
		By: by.Person, ByName: name, Asked: questionID, At: time.Now().UTC()}
	if err := setLimitState(ctx, tx, d.WorkflowID, append(slices.Clone(st.LimitGrants), g), requests); err != nil {
		return err
	}
	p := req.payload()
	p["extra"], p["by"], p["byName"] = g.Extra, g.By, g.ByName
	ev := ref.Event(EvLimitGranted, by.ActorType, p)
	ev.ActorID = by.ActorID
	_, err = ledger.Append(ctx, tx, ev)
	return err
}
