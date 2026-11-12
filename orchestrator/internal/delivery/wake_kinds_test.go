package delivery

import (
	"context"
	"regexp"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// wakeKinds is every kind of reason dude records for a conductor: those
// with a constant by it, and those its callers spell out.
var wakeKinds = []string{
	"decision", "escalation", "question", "safety", "steer_read", "steer_failed", "pr_merged", "pr_closed",
	"published", "publish_refused", "publish_stalled", "checkout", WakeStalled, WakeUnblocked, WakeFeedback, WakeWait,
	WakeRecovered, WakeRound, WakeChecks, WakeEpicMerge,
}

// The database takes a wake of every kind dude records, and its CHECK
// names no other: a migration that rewrites the kind list and drops one
// (two branches each adding a kind to the same list) fails here, not as
// a wake that is never recorded.
func TestEveryWakeKindIsRecorded(t *testing.T) {
	ctx := context.Background()
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	for _, q := range []string{
		`INSERT INTO projects(id,organization_id,name,slug,key_prefix) VALUES('prj_k',$1,'K','k','K')`,
		`INSERT INTO tasks(id,organization_id,project_id,number,title,goal,status) VALUES('wi_k',$1,'prj_k',1,'K','K','running')`,
	} {
		if _, err := owner.Exec(ctx, q, org); err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range wakeKinds {
		err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
			_, err := RecordWakeTx(ctx, tx, org, "wi_k", kind, "kind:"+kind, "a reason of kind "+kind)
			return err
		})
		if err != nil {
			t.Errorf("a %s wake is refused: %v", kind, err)
		}
	}
	var n int
	if err := owner.QueryRow(ctx, `SELECT count(DISTINCT kind) FROM conductor_wakes WHERE task_id = 'wi_k'`).Scan(&n); err != nil || n != len(wakeKinds) {
		t.Errorf("%d kinds recorded of %d (%v)", n, len(wakeKinds), err)
	}
	var def string
	if err := owner.QueryRow(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'conductor_wakes_kind_check'`).Scan(&def); err != nil {
		t.Fatal(err)
	}
	var allowed []string
	for _, m := range regexp.MustCompile(`'([a-z_]+)'::text`).FindAllStringSubmatch(def, -1) {
		allowed = append(allowed, m[1])
	}
	slices.Sort(allowed)
	want := slices.Sorted(slices.Values(wakeKinds))
	if !slices.Equal(allowed, want) {
		t.Errorf("the CHECK allows %q, dude records %q", allowed, want)
	}
}
