package agenttools

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

type startAgentsIn struct {
	Runs []startAgentIn `json:"runs"`
	Key  string         `json:"key,omitempty"`
}
type startAgentsOut struct {
	CohortID string          `json:"cohortId"`
	Runs     []startAgentOut `json:"runs"`
}

func startAgents(ctx context.Context, tx pgx.Tx, c Caller, in startAgentsIn) (startAgentsOut, error) {
	out := startAgentsOut{}
	if len(in.Runs) < 1 || len(in.Runs) > delivery.CohortMaxMembers {
		return out, refuseText(rCohortSize)
	}
	if c.Role != delivery.RoleConductor {
		return out, refuseText(rChildProfileRequired)
	}
	total := 0
	runs := make([]delivery.StartChildInput, len(in.Runs))
	for i, r := range in.Runs {
		if r.Key != "" {
			return out, refuseText(rCohortMemberKey)
		}
		total += len(r.Prompt) + len(r.Note) + len(r.Tier) + len(r.Profile) + len(r.Key)
		phase := delivery.PhaseForPromptRole(r.Profile)
		if phase == "" || r.Profile == "conductor" || r.Profile == "brainstorm" {
			return out, refuseText(rChildProfileRequired)
		}
		runs[i] = delivery.StartChildInput{Profile: r.Profile, Prompt: r.Prompt, Note: r.Note, Tier: r.Tier, Capabilities: delivery.ChildCapabilities{PublishCode: delivery.Publishes[phase]}}
	}
	if total > 60000 {
		return out, refuseText(rCohortBriefs)
	}
	key := in.Key
	if key == "" {
		key = "tool:" + c.CallID
	}
	id, members, err := delivery.StartCohortTx(ctx, tx, delivery.ChildParent{Kind: delivery.ChildParentTaskConductor, Run: c.run()}, key, runs)
	if err != nil {
		if delivery.IsRefusal(err) {
			return out, refuse("%s", err.Error())
		}
		return out, err
	}
	out.CohortID = id
	out.Runs = make([]startAgentOut, len(members))
	// Read persisted membership on replay, not the caller's potentially changed brief.
	for i, member := range members {
		out.Runs[i].RunID = member
		if err := tx.QueryRow(ctx, `SELECT child_profile, phase::text FROM runs WHERE id=$1`, member).Scan(&out.Runs[i].Profile, &out.Runs[i].Phase); err != nil {
			return out, err
		}
	}
	return out, nil
}

type readRunIn struct {
	Run    string `json:"run"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// readRun deliberately does not return a Run row: prompt, configuration,
// credentials, storage keys and arbitrary event payloads are not inspection.
func readRun(ctx context.Context, tx pgx.Tx, c Caller, in readRunIn) (json.RawMessage, error) {
	if in.Offset < 0 || in.Limit < 0 || in.Limit > 20 {
		return nil, refuseText(rInspectionPage)
	}
	if in.Limit == 0 {
		in.Limit = 10
	}
	var profile, status, replaced, stop string
	var generation int
	var heads json.RawMessage
	err := tx.QueryRow(ctx, `SELECT child_profile,status::text,child_generation,COALESCE(replaced_by,''),COALESCE(child_stop_reason,''),heads
  FROM runs WHERE id=$1 AND task_id=$2 AND parent_run_id=$3 AND source_kind='task_conductor'
  AND organization_id=$4 AND $2<>''`, in.Run, c.TaskID, c.RunID, c.Org).Scan(&profile, &status, &generation, &replaced, &stop, &heads)
	if err == pgx.ErrNoRows {
		return nil, refuseText(rInspectionScope)
	}
	if err != nil {
		return nil, err
	}
	w := newWrapper()
	out := map[string]any{"run": in.Run, "profile": profile, "status": status, "generation": generation, "replacedBy": replaced, "stopReason": stop, "recordedHeads": heads, "reportPresent": false, "reportClipped": false, "settled": false, "artifacts": []any{}}
	// Report only after settlement: terminal transport drain must finish first.
	var result json.RawMessage
	var report string
	reportLimit, nameLimit, descriptionLimit := 16000, 200, 1000
	err = tx.QueryRow(ctx, `SELECT jsonb_build_object('status',status,'stale',stale,'commits',commits,'verify',verify,'findings',findings,'verdicts',verdicts)
 FROM child_results WHERE parent_run_id=$1 AND child_run_id=$2 AND generation=$3`, c.RunID, in.Run, generation).Scan(&result)
	if err != nil && err != pgx.ErrNoRows {
		return nil, err
	}
	if err == nil {
		out["settled"] = true
		out["result"] = result
		report, err = delivery.ChildInspectionReportTx(ctx, tx, in.Run)
		if err != nil {
			return nil, err
		}
		out["reportPresent"] = report != ""
		out["reportClipped"] = len(report) > 16000
		out["report"] = w.text(report, 16000)
	}
	rows, err := tx.Query(ctx, `SELECT id, left(name,201), kind::text, left(COALESCE(description,''),1001) FROM artifacts WHERE run_id=$1 AND name NOT LIKE '.dude-%' ORDER BY id OFFSET $2 LIMIT $3`, in.Run, in.Offset, in.Limit+1)
	if err != nil {
		return nil, err
	}
	type artifact struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Kind        string `json:"kind"`
		Description string `json:"description"`
	}
	arts, err := pgx.CollectRows(rows, pgx.RowToStructByPos[artifact])
	if err != nil {
		return nil, err
	}
	more := len(arts) > in.Limit
	if more {
		arts = arts[:in.Limit]
	}
	rawArts := append([]artifact(nil), arts...)
	out["artifactsClipped"] = false
	out["metadataClipped"] = false
	for i := range arts {
		if len(arts[i].Name) > nameLimit || len(arts[i].Description) > descriptionLimit {
			out["artifactsClipped"] = true
		}
		arts[i].Name = w.text(arts[i].Name, 200)
		arts[i].Description = w.text(arts[i].Description, 1000)
	}
	out["artifacts"] = arts
	out["artifactsMore"] = more
	out["artifactOffset"] = in.Offset
	out["note"] = w.note() + " Reports are agent claims, not verified checks. recordedHeads are stored Run heads, not proof of publication. verify is the recorded verifier outcome, if present. Use run_chat or run_diff only if separately granted."
	// Check the encoded size after every reduction: JSON escaping can expand
	// text sixfold. Re-wrap original text rather than cutting JSON or markers.
	for {
		data, err := json.Marshal(out)
		if err != nil {
			return nil, err
		}
		if len(data) <= 64000 {
			return data, nil
		}
		switch {
		case report != "" && reportLimit > 0:
			reportLimit = min(reportLimit, len(report)) / 2
			out["report"] = w.text(report, reportLimit)
			out["reportClipped"] = true
		case len(arts) > 0 && (nameLimit > 0 || descriptionLimit > 0):
			nameLimit /= 2
			descriptionLimit /= 2
			for i, a := range rawArts {
				if len(a.Name) > nameLimit || len(a.Description) > descriptionLimit {
					out["artifactsClipped"] = true
				}
				arts[i].Name = w.text(a.Name, nameLimit)
				arts[i].Description = w.text(a.Description, descriptionLimit)
			}
		case out["metadataClipped"] == false:
			delete(out, "recordedHeads")
			delete(out, "result")
			out["metadataClipped"] = true
		default:
			return nil, refuseText(rInspectionBudget)
		}
	}
}
