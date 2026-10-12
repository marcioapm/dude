package delivery_test

import (
	"context"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// epicWorld is a project ABS with an epic holding ABS-1..3, ABS-4 on no
// epic, and another epic's ABS-5.
type epicWorld struct {
	app       *db.DB
	owner     *pgx.Conn
	org, epic string
	tasks     map[string]string // key → id
	project   string
	otherEpic string
}

func newEpicWorld(t *testing.T) *epicWorld {
	t.Helper()
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	w := &epicWorld{app: app, owner: owner, org: org, project: "prj_" + org, epic: "epc_" + org, otherEpic: "epc_o" + org,
		tasks: map[string]string{}}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(context.Background(), sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix, next_task_number) VALUES ($1, $2, 'P', $1, 'ABS', 6)`, w.project, org)
	exec(`INSERT INTO epics (id, organization_id, project_id, title) VALUES ($1, $3, $4, 'Preview'), ($2, $3, $4, 'Other')`,
		w.epic, w.otherEpic, org, w.project)
	for n, epic := range map[int]any{1: w.epic, 2: w.epic, 3: w.epic, 4: nil, 5: w.otherEpic} {
		id := "wi_" + string(rune('0'+n)) + org
		w.tasks["ABS-"+string(rune('0'+n))] = id
		exec(`INSERT INTO tasks (id, organization_id, project_id, number, epic_id, title, status) VALUES ($1, $2, $3, $4, $5, $6, 'running')`,
			id, org, w.project, n, epic, "Task "+string(rune('0'+n)))
	}
	return w
}

func (w *epicWorld) in(t *testing.T, fn func(tx pgx.Tx) error) {
	t.Helper()
	if err := w.app.InOrg(context.Background(), w.org, fn); err != nil {
		t.Fatal(err)
	}
}

// A task's peers are the other tasks on its epic; a task on no epic has
// none; another epic's task is no peer, by key or by id.
func TestPeersAreTheEpicMinusSelf(t *testing.T) {
	w := newEpicWorld(t)
	ctx := context.Background()
	keys := func(task string) []string {
		var out []string
		w.in(t, func(tx pgx.Tx) error {
			peers, err := delivery.Peers(ctx, tx, w.tasks[task])
			for _, p := range peers {
				out = append(out, p.Key+"/"+p.Via)
			}
			return err
		})
		return out
	}
	if got, want := keys("ABS-2"), []string{"ABS-1/epic", "ABS-3/epic"}; !slices.Equal(got, want) {
		t.Errorf("ABS-2's peers %v, want %v", got, want)
	}
	if got := keys("ABS-4"); len(got) != 0 {
		t.Errorf("a task on no epic has peers: %v", got)
	}
	if got := keys("ABS-5"); len(got) != 0 {
		t.Errorf("the only task on its epic has peers: %v", got)
	}
	w.in(t, func(tx pgx.Tx) error {
		if p, ok, err := delivery.PeerByKey(ctx, tx, w.tasks["ABS-2"], "abs-3"); err != nil || !ok || p.ID != w.tasks["ABS-3"] {
			t.Errorf("abs-3 for ABS-2: %+v %v %v", p, ok, err)
		}
		for _, key := range []string{"ABS-2", "ABS-4", "ABS-5", "nope"} {
			if _, ok, err := delivery.PeerByKey(ctx, tx, w.tasks["ABS-2"], key); err != nil || ok {
				t.Errorf("%s is ABS-2's peer (%v)", key, err)
			}
		}
		if ok, err := delivery.IsPeer(ctx, tx, w.tasks["ABS-1"], w.tasks["ABS-5"]); err != nil || ok {
			t.Errorf("another epic's task is a peer (%v)", err)
		}
		return nil
	})
}
