package phases

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// execLux answers every exec with an empty diff and keeps the commands.
type execLux struct {
	lux.Client
	mu   sync.Mutex
	cmds [][]string
}

func (f *execLux) Exec(_ context.Context, _ string, command []string) (lux.ExecResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cmds = append(f.cmds, command)
	return lux.ExecResult{Stdout: []byte("# dude-diff target " + strings.Repeat("0", 40) + "\n")}, nil
}

// A live read passes, after where the checkout started, what lux last
// reported syncing into it: the base branch for a phase Run, the task
// branch and then the base branch for a conductor. Syncs of other branches
// or repositories are not passed; with none recorded, the start alone.
func TestALiveReadIsGivenTheCommitsLuxSyncedIntoTheCheckout(t *testing.T) {
	ctx := context.Background()
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	start, base, tip := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	branch := delivery.BranchFor("wi_"+org, 1)
	for _, q := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_'||$1, $1, 'P', 'prj_'||$1, 'P')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_'||$1, $1, 'prj_'||$1, 1, 'T', 'G')`,
		`INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
			VALUES ('repo_'||$1, $1, 'prj_'||$1, 'target', 'git://h/target.git', 'main')`,
		`INSERT INTO task_repositories (organization_id, task_id, repository_id, access) VALUES ($1, 'wi_'||$1, 'repo_'||$1, 'write')`,
		`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, lux_run_id, lux_state)
			VALUES ('run_'||$1, $1, 'prj_'||$1, 'wi_'||$1, 1, 'lrun_1', 'running')`,
	} {
		if _, err := owner.Exec(ctx, q, org); err != nil {
			t.Fatal(err)
		}
	}
	set := func(baseSHAs, synced string) {
		t.Helper()
		if _, err := owner.Exec(ctx, `UPDATE runs SET base_shas = $2::jsonb, sync_history = $3::jsonb WHERE id = $1`,
			"run_"+org, baseSHAs, synced); err != nil {
			t.Fatal(err)
		}
	}
	fake := &execLux{}
	s := &Syncer{DB: app, Lux: fake}
	phase := phaseRun{ID: "run_" + org, Org: org, ProjectID: "prj_" + org, TaskID: "wi_" + org, Phase: "fix", Attempt: 1, LuxRunID: "lrun_1"}
	conductor := phase
	conductor.Phase, conductor.Role = "", delivery.RoleConductor
	read := func(r phaseRun) string {
		t.Helper()
		if _, err := s.readDiff(ctx, r, false); err != nil {
			t.Fatal(err)
		}
		cmd := fake.cmds[len(fake.cmds)-1]
		if !slices.Equal(cmd[:5], []string{"sh", "-c", diffScript, "dude-diff", "-"}) {
			t.Fatalf("command %q", cmd[:5])
		}
		return strings.Join(cmd[5:], "|")
	}

	set(`{"target":"`+start+`"}`, `{}`)
	if got := read(phase); got != "target|/workspace/repos/target|"+start {
		t.Errorf("no syncs: %q", got)
	}
	set(`{"target":"`+start+`"}`, `{"target":{"main":["`+base+`"],"`+branch+`":["`+tip+`"],"other":["`+tip+`"]},"elsewhere":{"main":["`+tip+`"]}}`)
	if got := read(phase); got != "target|/workspace/repos/target|"+start+" "+base {
		t.Errorf("phase Run: %q, want its start then main's sync", got)
	}
	if got := read(conductor); got != "target|/workspace/repos/target|"+start+" "+tip+" "+base {
		t.Errorf("conductor: %q, want its start, the task branch's sync, then main's", got)
	}
}
