package agenttools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/capabilities"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestReadRunSerializedBudget(t *testing.T) {
	for _, tc := range []struct {
		name, report, artifact            string
		metadata, irreducible             bool
		reportClipped, artifactsClipped   bool
		artifactName, artifactDescription string
		largeResult                       bool
	}{
		{name: "html", report: strings.Repeat("<", 16000), reportClipped: true},
		{name: "control", report: strings.Repeat("\x01", 16000), reportClipped: true},
		{name: "quotes-backslashes", report: strings.Repeat("\"\\", 8000)},
		{name: "multibyte", report: strings.Repeat("界", 17000), reportClipped: true},
		{name: "artifact-html", report: "normal report", artifact: "<", reportClipped: true, artifactsClipped: true},
		{name: "artifact-quotes-backslashes", report: strings.Repeat("\"\\", 8000), artifact: "\"\\", reportClipped: true},
		{name: "metadata", report: "normal report", metadata: true},
		{name: "large-result", report: strings.Repeat("r", 16000), artifactName: "artifact", metadata: true, largeResult: true},
		{name: "name-at-cap", report: "ok", artifactName: strings.Repeat("n", 200)},
		{name: "name-over-cap", report: "ok", artifactName: strings.Repeat("n", 201), artifactsClipped: true},
		{name: "description-at-cap", report: "ok", artifactDescription: strings.Repeat("d", 1000)},
		{name: "description-over-cap", report: "ok", artifactDescription: strings.Repeat("d", 1001), artifactsClipped: true},
		{name: "artifact-multibyte", report: "ok", artifactName: strings.Repeat("界", 67), artifactsClipped: true},
		{name: "irreducible", report: "normal report", irreducible: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			token := f.conductor(t)
			f.run(t, "inspection_child", "investigator", "completed")
			mustExec(t, f.owner, `UPDATE runs SET source_kind='task_conductor',parent_run_id='run_cond',child_profile='investigator',child_capabilities='{}' WHERE id='inspection_child'`)
			mustExec(t, f.owner, `INSERT INTO events (id,organization_id,task_id,run_id,event_type,actor_type,actor_id,source,payload) VALUES ('evt_report',$1,$2,'inspection_child','agent.message','agent','inspection_child','orchestrator',jsonb_build_object('text',$3::text))`, f.org, f.item, tc.report)
			store := &delivery.Store{DB: f.app}
			if _, err := store.SettleChild(context.Background(), f.org, "inspection_child", delivery.ChildOutcome{Status: "completed"}); err != nil {
				t.Fatal(err)
			}
			if tc.artifact != "" {
				for i := range 20 {
					mustExec(t, f.owner, `INSERT INTO artifacts(id,organization_id,run_id,kind,name,description,storage_key,sha256) VALUES ($1,$2,'inspection_child','report',$3,$4,$1,'hash')`, fmt.Sprintf("artifact_%02d", i), f.org, strings.Repeat(tc.artifact, 100), strings.Repeat(tc.artifact, 500))
				}
			}
			if tc.artifactName != "" || tc.artifactDescription != "" {
				mustExec(t, f.owner, `INSERT INTO artifacts(id,organization_id,run_id,kind,name,description,storage_key,sha256) VALUES ('literal',$1,'inspection_child','report',$2,$3,'literal','hash')`, f.org, tc.artifactName, tc.artifactDescription)
			}
			if tc.metadata {
				mustExec(t, f.owner, `UPDATE runs SET heads=jsonb_build_object('repo',$1::text) WHERE id='inspection_child'`, strings.Repeat("<", 16000))
			}
			if tc.irreducible {
				mustExec(t, f.owner, `INSERT INTO artifacts(id,organization_id,run_id,kind,name,storage_key,sha256) VALUES ('large_kind',$1,'inspection_child',$2,'name','key','hash')`, f.org, strings.Repeat("<", 16000))
			}
			if tc.largeResult {
				mustExec(t, f.owner, `UPDATE child_results SET verify=jsonb_build_object('large',$1::text) WHERE child_run_id='inspection_child'`, strings.Repeat("x", 2*1024*1024))
			}
			encodings := agenttools.CountInspectionMetadataEncodings(t)
			var raw json.RawMessage
			code := f.postAs(t, token, "read_run", `{"run":"inspection_child","limit":20}`, &raw)
			if *encodings != 1 {
				t.Fatalf("metadata encoded %d times, want once", *encodings)
			}
			if tc.irreducible {
				if code != 422 || !json.Valid(raw) || len(raw) > 64000 {
					t.Fatalf("irreducible inspection: status %d, %d bytes", code, len(raw))
				}
				var refused struct{ Error string }
				if err := json.Unmarshal(raw, &refused); err != nil {
					t.Fatal(err)
				}
				if refused.Error != "child Run inspection exceeds 64000 bytes even after clipping; request fewer artifacts" {
					t.Fatalf("unexpected refusal: %q", refused.Error)
				}
				return
			}
			if code != 200 || !json.Valid(raw) || !utf8.Valid(raw) || len(raw) > 64000 {
				t.Fatalf("inspection: status %d, valid JSON %v, %d bytes", code, json.Valid(raw), len(raw))
			}
			var out struct {
				Report                                           string
				ReportClipped, ArtifactsClipped, MetadataClipped bool
				Artifacts                                        []struct{ Name, Description string }
			}
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatal(err)
			}
			if out.ReportClipped != tc.reportClipped || out.ArtifactsClipped != tc.artifactsClipped || out.MetadataClipped != tc.metadata {
				t.Fatalf("clipping: report=%v artifacts=%v metadata=%v", out.ReportClipped, out.ArtifactsClipped, out.MetadataClipped)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"recordedHeads", "result"} {
				if _, present := fields[key]; present == out.MetadataClipped {
					t.Fatalf("%s presence disagrees with metadataClipped", key)
				}
			}
			loss := func(text, original string) bool {
				t.Helper()
				payload := ""
				if text != "" {
					rest, ok := strings.CutPrefix(text, "⟦external ")
					id, body, marked := strings.Cut(rest, "⟧")
					var closed bool
					payload, _, closed = strings.Cut(body, "⟦/external "+id+"⟧")
					if !ok || !marked || id == "" || !closed {
						t.Fatal("missing external wrapper")
					}
				} else if original != "" {
					t.Fatal("nonempty text lost its wrapper")
				}
				if strings.ContainsRune(payload, '\uFFFD') || !strings.HasPrefix(original, payload) {
					t.Fatal("payload is not an intact prefix of original")
				}
				return payload != original
			}
			if out.ReportClipped != loss(out.Report, tc.report) {
				t.Fatal("report flag disagrees with actual loss")
			}
			wantArtifacts := 0
			if tc.artifact != "" {
				wantArtifacts = 20
			} else if tc.artifactName != "" || tc.artifactDescription != "" {
				wantArtifacts = 1
			}
			if len(out.Artifacts) != wantArtifacts {
				t.Fatalf("artifacts: got %d want %d", len(out.Artifacts), wantArtifacts)
			}
			artifactLoss := false
			for _, a := range out.Artifacts {
				name, description := tc.artifactName, tc.artifactDescription
				if tc.artifact != "" {
					name, description = strings.Repeat(tc.artifact, 100), strings.Repeat(tc.artifact, 500)
				}
				nameLoss, descriptionLoss := loss(a.Name, name), loss(a.Description, description)
				artifactLoss = artifactLoss || nameLoss || descriptionLoss
				if tc.artifact == "" && !tc.largeResult {
					for _, field := range []struct {
						text, original string
						cap            int
					}{{a.Name, name, 200}, {a.Description, description, 1000}} {
						want := field.original
						for len(want) > field.cap {
							_, size := utf8.DecodeLastRuneInString(want)
							want = want[:len(want)-size]
						}
						if want != "" && !strings.Contains(field.text, "⟧"+want+"⟦/external ") {
							t.Fatal("artifact did not retain the payload up to its initial byte cap")
						}
					}
				}
			}
			if out.ArtifactsClipped != artifactLoss {
				t.Fatal("artifact flag disagrees with actual loss")
			}
		})
	}
}

// Production JSON dispatch + real PostgreSQL settlement/outbox: not a real
// model acceptance test or a full lux stack test.
func TestCohortDispatchAndInspection(t *testing.T) {
	f := setup(t)
	token := f.run(t, "run_cohort_parent", "conductor", "running")
	mustExec(t, f.owner, `UPDATE runs SET phase=NULL WHERE id='run_cohort_parent'`)
	mustExec(t, f.owner, `INSERT INTO workflow_runs(id,organization_id,task_id,workflow_type,idempotency_key,status,step,state) VALUES ('wf_cohort',$1,$2,'task.delivery','cohort','waiting','conductorDecision','{"attempt":1}')`, f.org, f.item)
	var batch struct {
		CohortID string
		Runs     []struct{ RunID, Profile string }
	}
	brief := `{"key":"parallel","runs":[{"profile":"investigator","prompt":"investigate A"},{"profile":"reviewer","prompt":"review B"},{"profile":"investigator","note":"check C"}]}`
	if status := f.postAs(t, token, "start_agents", brief, &batch); status != 200 || len(batch.Runs) != 3 {
		t.Fatalf("start %d %+v", status, batch)
	}
	store := &delivery.Store{DB: f.app}
	ctx := context.Background()
	for i, r := range batch.Runs {
		report := fmt.Sprintf("report member %d: passing claims do not override failure ```nested````", i)
		if i == 0 {
			report += strings.Repeat("界", 17000)
		}
		mustExec(t, f.owner, `INSERT INTO events (id,organization_id,task_id,run_id,event_type,actor_type,actor_id,source,payload) VALUES ('evt_' || $3,$1,$2,$3,'agent.message','agent',$3,'orchestrator',jsonb_build_object('text',$4::text))`, f.org, f.item, r.RunID, report)
		status := "completed"
		if i == 1 {
			status = "failed"
		}
		if _, err := store.SettleChild(ctx, f.org, r.RunID, delivery.ChildOutcome{Status: status}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.EnqueueChildResults(ctx); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := f.owner.QueryRow(ctx, `SELECT count(*) FROM directives WHERE run_id='run_cohort_parent'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		want := 0
		if i == 2 {
			want = 1
		}
		if n != want {
			t.Fatalf("after %d: %d directives want %d", i+1, n, want)
		}
		var read map[string]any
		if code := f.postAs(t, token, "read_run", fmt.Sprintf(`{"run":%q}`, r.RunID), &read); code != 200 {
			t.Fatalf("read %d %+v", code, read)
		}
		if read["status"] != status || read["reportPresent"] != true || read["settled"] != true {
			t.Fatalf("inspection %+v", read)
		}
		if i == 0 && read["reportClipped"] != true {
			t.Fatal("large report not labeled clipped")
		}
		if !strings.Contains(fmt.Sprint(read["report"]), "report member") {
			t.Fatal("missing report")
		}
		raw, _ := json.Marshal(read)
		if len(raw) > 64000 {
			t.Fatal("inspection oversized")
		}
	}
	if code, _ := f.post(t, token, "read_run", `{"run":"unknown"}`); code != 422 {
		t.Fatalf("unknown run %d", code)
	}
	var before int
	if err := f.owner.QueryRow(ctx, `SELECT count(*) FROM runs`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if code, _ := f.post(t, token, "start_agents", `{"runs":[{"profile":"investigator"},{"profile":"invalid"}]}`); code != 422 {
		t.Fatalf("invalid second %d", code)
	}
	var after int
	if err := f.owner.QueryRow(ctx, `SELECT count(*) FROM runs`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatal("invalid request created partial children")
	}
	worker := f.run(t, "run_cohort_worker", "implementer", "running")
	for _, tool := range []string{"start_agents", "read_run"} {
		if code, _ := f.post(t, worker, tool, `{}`); code != 404 {
			t.Fatalf("worker %s: %d", tool, code)
		}
	}
}

func TestCohortInspectionScope(t *testing.T) {
	f := setup(t)
	token := f.run(t, "inspection_parent", "conductor", "running")
	mustExec(t, f.owner, `UPDATE runs SET status='completed' WHERE id='inspection_parent'`)
	other := f.run(t, "other_parent", "conductor", "running")
	mustExec(t, f.owner, `UPDATE runs SET status='completed' WHERE id='other_parent'`)
	mustExec(t, f.owner, `UPDATE runs SET status='running' WHERE id='inspection_parent'`)
	f.run(t, "inspection_child", "investigator", "completed")
	mustExec(t, f.owner, `UPDATE runs SET source_kind='task_conductor',parent_run_id='inspection_parent',child_profile='investigator',child_capabilities='{}' WHERE id='inspection_child'`)
	if code, _ := f.post(t, token, "read_run", `{"run":"inspection_child"}`); code != 200 {
		t.Fatalf("own child: %d", code)
	}
	mustExec(t, f.owner, `UPDATE runs SET status='completed' WHERE id='inspection_parent'`)
	mustExec(t, f.owner, `UPDATE runs SET status='running' WHERE id='other_parent'`)
	if code, _ := f.post(t, other, "read_run", `{"run":"inspection_child"}`); code != 422 {
		t.Fatalf("another parent's child: %d", code)
	}
	mustExec(t, f.owner, `UPDATE runs SET status='completed' WHERE id='other_parent'`)
	mustExec(t, f.owner, `UPDATE runs SET status='running' WHERE id='inspection_parent'`)
	mustExec(t, f.owner, `INSERT INTO tasks(id,organization_id,project_id,number,title,goal) SELECT 'inspection_other_task',organization_id,project_id,2,'Other','Other' FROM tasks WHERE id=$1`, f.item)
	mustExec(t, f.owner, `UPDATE runs SET task_id='inspection_other_task' WHERE id='inspection_child'`)
	if code, _ := f.post(t, token, "read_run", `{"run":"inspection_child"}`); code != 422 {
		t.Fatalf("cross-task child: %d", code)
	}
}

func TestCohortCapabilitiesDeniedOnJSONAndMCP(t *testing.T) {
	srv := &agenttools.Server{}
	srv.ResolveCapabilitiesWith(func(_ context.Context, _ pgx.Tx, a capabilities.Agent) (capabilities.Grants, error) {
		return capabilities.Default(a.Role).Without(capabilities.Cohorts, capabilities.ChildInspect), nil
	})
	f := setupWith(t, srv)
	token := f.run(t, "cohort_reduced", "conductor", "running")
	listed := f.getTools(t, token)
	cs, err := f.connect(t, token)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"start_agents", "read_run"} {
		if _, ok := listed[name]; ok {
			t.Errorf("JSON lists denied %s", name)
		}
		for _, tool := range list.Tools {
			if tool.Name == name {
				t.Errorf("MCP lists denied %s", name)
			}
		}
		if code, _ := f.post(t, token, name, `{}`); code != 404 {
			t.Errorf("JSON %s: %d", name, code)
		}
		if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{}}); err == nil {
			t.Errorf("MCP allowed %s", name)
		}
	}
}
