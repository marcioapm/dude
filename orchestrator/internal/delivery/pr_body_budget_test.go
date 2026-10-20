package delivery_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// A task at the criteria limit, with a long description and the most gated
// Runs reporting the most checks, still gets a pull request body GitHub
// takes, opened or refreshed: the test gate's section is whole, and the
// checks account gets what is left, its headline and the newest Run first.
// With less left than a compact Run needs it keeps only its headline.
func TestThePullRequestBodyFitsGitHubsLimit(t *testing.T) {
	app, owner := dbtest.Open(t)
	ctx := context.Background()
	org := dbtest.Org(t, owner)
	// agenttools.CriteriaMax characters of criteria, multi-byte included.
	const n = 16
	per := agenttools.CriteriaMax / n
	var criteria []string
	for i := range n {
		head := fmt.Sprintf("Criterion %02d: ", i+1)
		criteria = append(criteria, head+strings.Repeat("é", per-len(head)))
	}
	raw, _ := json.Marshal(criteria)
	for _, q := range []string{
		`INSERT INTO projects(id,organization_id,name,slug,key_prefix) VALUES('prj_pb',$1,'PB','pb','PB')`,
		`INSERT INTO tasks(id,organization_id,project_id,number,title,goal,acceptance_criteria,status)
			VALUES('wi_pb',$1,'prj_pb',1,'PB','',$2::jsonb,'running')`,
		`INSERT INTO runs(id,organization_id,project_id,task_id,attempt,phase,status,created_at)
			SELECT 'run_pb' || lpad(g::text, 2, '0'), $1, 'prj_pb', 'wi_pb', 1,
				(CASE g WHEN 0 THEN 'implement' WHEN 51 THEN 'simplify' ELSE 'fix' END)::run_phase, 'completed',
				now() - (52 - g) * interval '1 minute'
			FROM generate_series(0, 51) g`,
		`INSERT INTO runs(id,organization_id,project_id,task_id,attempt,phase,status) VALUES('run_pbt',$1,'prj_pb','wi_pb',1,'test','completed')`,
		`INSERT INTO acceptance_verdicts (organization_id, task_id, run_id, criterion, text, status, heads)
			SELECT $1, 'wi_pb', 'run_pbt', c.n, c.text, 'pass', '{"api":"0123456789abcdef"}'
			FROM jsonb_array_elements_text($2::jsonb) WITH ORDINALITY AS c(text, n)`,
		`INSERT INTO events(id,organization_id,event_type,run_id,task_id,actor_type,actor_id,source,payload)
			SELECT 'ev_pb' || r.id, $1, 'run.checks.judged', r.id, 'wi_pb', 'system', 'dude', 'test',
				jsonb_build_object('turn', 1, 'verified', true, 'checks', (SELECT jsonb_agg(jsonb_build_object(
					'command', repeat('c', 5000), 'result', 'pass', 'decision', 'yes', 'reason', repeat('r ', 2500), 'callId', 'mcall_x'))
					FROM generate_series(1, 30)))
			FROM runs r WHERE r.task_id = 'wi_pb' AND r.phase <> 'test'`,
	} {
		args := []any{org}
		if strings.Contains(q, "$2") {
			args = append(args, string(raw))
		}
		if _, err := owner.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	gh := fakegithub.New("", "acme/pb")
	srv := httptest.NewServer(gh.Handler())
	t.Cleanup(srv.Close)
	forges := forgeOf{forge.NewGitHub(forge.Credential{Auth: "pat", Secret: "ghp", APIBaseURL: srv.URL})}
	if _, err := owner.Exec(ctx, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ('repo_pb', $1, 'prj_pb', 'api', 'https://github.com/acme/pb.git', 'main')`, org); err != nil {
		t.Fatal(err)
	}
	store := &delivery.Store{DB: app}
	gate := "<!-- dude:verification -->\n## Verification\nTested at `0123456`.\n\n"
	for _, c := range criteria {
		gate += "- **pass** — " + c + "\n"
	}
	gate += "\nThe evidence (video, screenshots, `acceptance.md`) is on the task in dude.\n\n### Checks\n" +
		"Checks were verified against the Run's recorded commands.\n\n"
	// Sentences of 50 characters.
	const sentence = "The settings page lets an admin choose the tiers. "
	for i, c := range []struct {
		name, goal, checks string
	}{{
		"a 30,000-character description",
		strings.Repeat(sentence, 600),
		"51 earlier Runs omitted.\n- **Simplifier** — backed\n" +
			"  - 30 checks reported; checks omitted from this account: see the task's Activity.",
	}, {
		"a description leaving less than a compact Run",
		strings.Repeat(sentence, 637),
		"Checks account omitted: see the task's Activity.",
	}} {
		if _, err := owner.Exec(ctx, `UPDATE tasks SET goal = $1 WHERE id = 'wi_pb'`, c.goal); err != nil {
			t.Fatal(err)
		}
		st := &delivery.State{TaskID: "wi_pb", ProjectID: "prj_pb", Attempt: 1, Heads: map[string]string{"api": "0123456789abcdef"}}
		_, opened, err := store.PullRequestText(ctx, org, st)
		if err != nil {
			t.Fatal(err)
		}
		// The pull request as a person left it: their paragraph added and
		// dude's section an earlier, shorter one.
		number := gh.OpenAsPerson("dude", fmt.Sprintf("pb-%d", i), "main", "PB")
		pr := gh.Pull(number)
		edited := strings.Replace(replaceSection(opened, "## Verification\nNot tested yet."),
			"\n\n---\n", "\n\nA reviewer's note.\n\n---\n", 1)
		gh.Set(func(*fakegithub.Server) { pr.Body = edited })
		prID := fmt.Sprintf("pr_pb%d", i)
		if _, err := owner.Exec(ctx, `INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id,
			number, url, head_branch, base_branch, head_sha, title, body, state, checks)
			VALUES ($2, $1, 'prj_pb', 'wi_pb', 'repo_pb', $3, '', 'feature', 'main', '', 'PB', '', 'open', 'unknown')`,
			org, prID, number); err != nil {
			t.Fatal(err)
		}
		st.PullRequestIDs = []string{prID}
		store.RefreshVerification(ctx, org, st, forges)
		var refreshed string
		gh.Set(func(*fakegithub.Server) { refreshed = pr.Body })
		if refreshed == edited {
			t.Fatalf("%s: the refresh left the body as the person did", c.name)
		}
		for _, body := range []struct{ how, text string }{{"opened", opened}, {"refreshed", refreshed}} {
			if n := utf8.RuneCountInString(body.text); n > 65_536 {
				t.Errorf("%s, %s: the pull request body is %d characters, more than GitHub's 65,536", c.name, body.how, n)
			}
			_, section, _ := strings.Cut(body.text, "\n\n<!-- dude:verification -->")
			section, _, _ = strings.Cut(section, "\n<!-- /dude:verification -->")
			if want := gate[len("<!-- dude:verification -->"):] + c.checks; section != want {
				t.Errorf("%s, %s: the verification section ends\n%s\nwant\n%s", c.name, body.how,
					section[max(0, len(section)-400):], want[max(0, len(want)-400):])
			}
		}
	}
}

// replaceSection is body with its marked verification section's text
// replaced by text.
func replaceSection(body, text string) string {
	before, rest, _ := strings.Cut(body, "<!-- dude:verification -->\n")
	_, after, _ := strings.Cut(rest, "\n<!-- /dude:verification -->")
	return before + "<!-- dude:verification -->\n" + text + "\n<!-- /dude:verification -->" + after
}

type forgeOf struct{ gh *forge.GitHub }

func (f forgeOf) For(context.Context, string) (*forge.GitHub, error) { return f.gh, nil }
