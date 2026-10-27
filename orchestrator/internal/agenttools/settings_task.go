package agenttools

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// The settings tool's task view: what one task's delivery is held to,
// read through delivery.TaskLimitsTx as the limits tool and the checks
// read it.

type taskRaiseOut struct {
	Limit string `json:"limit"`
	// A rerun's: the pull request head it is for.
	PullRequest string `json:"pullRequest,omitempty"`
	grantedOut
}

// frozenOut is one policy key of a live delivery that differs from what
// the settings say today: the frozen value applies to this delivery.
type frozenOut struct {
	Frozen any      `json:"frozen"`
	Today  valueOut `json:"today"`
}

type taskViewOut struct {
	Key string `json:"key"`
	// The limits tool's words for the delivery's state.
	Delivery string `json:"delivery"`
	Note     string `json:"note"`
	// The policy the task's delivery is held to, each key with its source
	// today; for a live delivery the frozen values, and where one differs
	// from today's, both under differs.
	Policy  map[string]valueOut  `json:"policy"`
	Differs map[string]frozenOut `json:"differsFromToday,omitempty"`
	Raises  []taskRaiseOut       `json:"raisedForThisTask,omitempty"`
	// ReviewersFor over the delivery's latest changed paths; absent while
	// none are known.
	Reviewers     []string `json:"reviewers,omitempty"`
	ReviewersNote string   `json:"reviewersNote,omitempty"`
	// Fix rounds spent: this review round's, and each pull request's.
	FixRoundsThisReviewRound int            `json:"fixRoundsThisReviewRound"`
	FixRoundsByPullRequest   map[string]int `json:"fixRoundsByPullRequest"`
}

// settingsTask is the task the call names, in the caller's organisation:
// by key (delivery.TaskByOrgKey, as create_task and relate find one) or
// by id; with none named, a task agent's own. "" for none.
func settingsTask(ctx context.Context, tx pgx.Tx, c Caller, named string) (id, projectID string, err error) {
	named = strings.TrimSpace(named)
	if named == "" {
		return c.TaskID, c.ProjectID, nil
	}
	if _, ok := delivery.TaskKey(named); ok {
		t, found, err := delivery.TaskByOrgKey(ctx, tx, named)
		if err == nil && found {
			err = fenced(ctx, tx, c, t.ProjectID, "")
		}
		if err != nil || found {
			return t.ID, t.ProjectID, err
		}
	} else {
		err := tx.QueryRow(ctx, `SELECT id, project_id FROM tasks WHERE id = $1`, named).Scan(&id, &projectID)
		if err == nil {
			err = fenced(ctx, tx, c, projectID, "")
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return id, projectID, err
		}
	}
	return "", "", refuse(rSettingsNoTask, named)
}

// taskView is what the task's delivery is held to.
func taskView(ctx context.Context, tx pgx.Tx, w wrapper, taskID string) (*taskViewOut, error) {
	l, err := delivery.TaskLimitsTx(ctx, tx, taskID)
	if err != nil {
		return nil, err
	}
	key, err := delivery.TaskKeyOf(ctx, tx, taskID)
	if err != nil {
		return nil, err
	}
	out := &taskViewOut{Key: key, Delivery: limitsText("delivery", l.Delivery), Note: settingsText("frozen", l.Delivery),
		Policy: map[string]valueOut{}, FixRoundsThisReviewRound: l.PRIteration, FixRoundsByPullRequest: l.PRFixes}
	held, today := reflect.ValueOf(l.Policy), reflect.ValueOf(l.Now)
	for i, k := range delivery.PolicyKeys() {
		v, now := held.Field(i).Interface(), today.Field(i).Interface()
		if slices.Contains(delivery.LivePolicyKeys, k) {
			// Applied as it is now, whatever the delivery froze (or, frozen
			// before the key existed, decoded as zero).
			v = now
		}
		out.Policy[k] = policyValue(w, k, v, l.NowSetIn[k])
		if l.Delivery == delivery.DeliveryLive && !reflect.DeepEqual(v, now) {
			if out.Differs == nil {
				out.Differs = map[string]frozenOut{}
			}
			out.Differs[k] = frozenOut{Frozen: policyText(w, k, v), Today: valueOut{Value: policyText(w, k, now), Source: sourceOf(l.NowSetIn[k])}}
			out.Policy[k] = valueOut{Value: policyText(w, k, v), Source: "frozen"}
		}
	}
	for _, g := range l.Grants {
		r := taskRaiseOut{Limit: g.Limit, grantedOut: grantedOf(g)}
		r.By = w.text(r.By, 100)
		if g.Limit == delivery.LimitRerun {
			r.PullRequest = fmt.Sprintf("%s#%d@%s", g.Repo, g.Number, g.Head)
		}
		out.Raises = append(out.Raises, r)
	}
	if len(l.ChangedPaths) > 0 {
		out.Reviewers = delivery.ReviewersFor(l.Policy, slices.Clone(l.ChangedPaths))
		out.ReviewersNote = settingsText("reviewers", nil)
	}
	return out, nil
}
