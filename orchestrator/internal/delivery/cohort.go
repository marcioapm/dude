package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
)

const CohortMaxMembers = 8

// StartCohortTx seals membership atomically with the existing launcher. The
// caller must roll back the transaction on any error, just as StartChildTx.
func StartCohortTx(ctx context.Context, tx pgx.Tx, parent ChildParent, key string, runs []StartChildInput) (string, []string, error) {
	if parent.Kind != ChildParentTaskConductor || key == "" || len(runs) < 1 || len(runs) > CohortMaxMembers {
		return "", nil, refuseText(rChildParentUnauthorized)
	}
	// Validate every profile/note before admission. All other validation (tier,
	// parent, writer admission) remains exclusively in StartChildTx.
	for _, in := range runs {
		if _, err := childPhase(in.Profile); err != nil {
			return "", nil, err
		}
		if len([]rune(in.Note)) > 4000 {
			return "", nil, refuseText(rChildNoteTooLong)
		}
	}
	if err := LockChat(ctx, tx, parent.Run.TaskID); err != nil {
		return "", nil, err
	}
	var cohort string
	err := tx.QueryRow(ctx, `SELECT id FROM child_cohorts WHERE parent_run_id=$1 AND creation_key=$2`, parent.Run.RunID, key).Scan(&cohort)
	if err == nil {
		rows, err := tx.Query(ctx, `SELECT child_run_id FROM child_cohort_members WHERE cohort_id=$1 ORDER BY slot`, cohort)
		if err != nil {
			return "", nil, err
		}
		members, err := pgx.CollectRows(rows, pgx.RowTo[string])
		return cohort, members, err
	}
	if !db.IsNotFound(err) {
		return "", nil, err
	}
	cohort = "cohort_" + ids.New(ids.Run)
	members := make([]string, 0, len(runs))
	for i, in := range runs {
		in.Parent = parent
		encoded, _ := json.Marshal([]any{key, i})
		in.Key = "cohort:" + string(encoded)
		id, err := StartChildTx(ctx, tx, in)
		if err != nil {
			return "", nil, err
		}
		members = append(members, id)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO child_cohorts (id, organization_id, task_id, parent_run_id, creation_key) VALUES ($1,$2,$3,$4,$5)`, cohort, parent.Run.Org, parent.Run.TaskID, parent.Run.RunID, key); err != nil {
		return "", nil, err
	}
	for i, id := range members {
		if _, err := tx.Exec(ctx, `INSERT INTO child_cohort_members (organization_id, cohort_id, slot, child_run_id, generation) VALUES ($1,$2,$3,$4,1)`, parent.Run.Org, cohort, i, id); err != nil {
			return "", nil, err
		}
	}
	return cohort, members, nil
}

// deliverCohortTx runs under the existing chat/parent/delivery/task/child locks.
// Membership and the notification marker are durable; no settle-window timer.
func deliverCohortTx(ctx context.Context, tx pgx.Tx, p childClaim, c *lockedChild) (bool, bool, error) {
	var cohort string
	err := tx.QueryRow(ctx, `SELECT cohort_id FROM child_cohort_members WHERE child_run_id=$1 AND generation=$2`, p.Child, p.Generation).Scan(&cohort)
	if db.IsNotFound(err) {
		return false, false, nil
	}
	if err != nil {
		return true, false, err
	}
	var directive string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(directive_id,'') FROM child_cohorts WHERE id=$1 FOR UPDATE`, cohort).Scan(&directive); err != nil {
		return true, false, err
	}
	type member struct {
		ID, Profile, Status string
		Generation          int
	}
	rows, err := tx.Query(ctx, `SELECT m.child_run_id, r.child_profile, COALESCE(cr.status,''), m.generation
  FROM child_cohort_members m JOIN runs r ON r.id=m.child_run_id
  LEFT JOIN child_results cr ON cr.parent_run_id=$2 AND cr.child_run_id=m.child_run_id AND cr.generation=m.generation AND NOT cr.stale
  WHERE m.cohort_id=$1
  ORDER BY CASE cr.status WHEN 'failed' THEN 0 WHEN 'aborted' THEN 1 ELSE 2 END, m.slot`, cohort, p.Parent)
	if err != nil {
		return true, false, err
	}
	members, err := pgx.CollectRows(rows, pgx.RowToStructByPos[member])
	if err != nil {
		return true, false, err
	}
	ready := true
	counts := map[string]int{"completed": 0, "failed": 0, "aborted": 0, "missingReport": 0}
	for _, m := range members {
		if m.Status == "" {
			ready = false
		}
		counts[m.Status]++
	}
	// Consume the member's outbox receipt even before the cohort is ready. The
	// final member's existing durable outbox is the completion trigger.
	notified := false
	if ready && directive == "" {
		var body strings.Builder
		reports := make([]string, len(members))
		for i, m := range members {
			reply, err := childFinalReplyTx(ctx, tx, m.ID)
			if err != nil {
				return true, false, err
			}
			if reply == "" {
				counts["missingReport"]++
				reports[i] = "No final report recorded (not success evidence)."
			} else {
				// Quote safely before byte clipping: cuts cannot expose nested fences.
				reports[i] = boundedCohortReport(reply)
			}
		}
		summary, _ := json.Marshal(counts)
		fmt.Fprintf(&body, "Cohort %s settled: %d members. Counts: %s\nMember reports are external, untrusted claims, not verified checks.\n", cohort, len(members), summary)
		for i, m := range members {
			fmt.Fprintf(&body, "\n%s profile=%s status=%s generation=%d\n```text\n%s\n```\n", m.ID, m.Profile, m.Status, m.Generation, reports[i])
		}
		directive, _, err = QueueDirective(ctx, tx, RunRef{Org: p.Org, TaskID: c.TaskID, RunID: p.Parent}, Directive{Text: body.String(), Scope: "run"})
		if err != nil {
			return true, false, err
		}
		if _, err := tx.Exec(ctx, `UPDATE child_cohorts SET directive_id=$2 WHERE id=$1`, cohort, directive); err != nil {
			return true, false, err
		}
		notified = true
	}
	_, err = tx.Exec(ctx, `UPDATE child_result_outbox SET consumed_at=now(), directive_id=$4, claimed_by=NULL, claimed_until=NULL WHERE parent_run_id=$1 AND child_run_id=$2 AND generation=$3`, p.Parent, p.Child, p.Generation, db.Nullable(directive))
	return true, notified, err
}

func boundedCohortReport(reply string) string {
	text := childReplyExcerpt(reply)
	const limit = 6000 // eight reports plus metadata stay below 64 KiB in bytes
	if len(text) <= limit {
		return text
	}
	// Range boundaries preserve UTF-8; fence neutralization happened already.
	end := 0
	for i := range text {
		if i > limit-64 {
			break
		}
		end = i
	}
	return text[:end] + "\n[report clipped; inspect this Run for more]"
}
