package delivery

import (
	"sync"

	"github.com/jackc/pgx/v5"
)

// txWatch counts, while a test server answers a request, the test
// database's sessions idle in a transaction (as chat's notices test does):
// a transaction held open across a network read shows there. Each count
// is made on a connection of its own, so requests may be in flight at once.
type txWatch struct {
	w    *childWorld
	mu   sync.Mutex
	seen int // requests watched
	open int // the most transactions open during one
	errs []error
}

func (w *childWorld) watchTxs() *txWatch { return &txWatch{w: w} }

// see counts the open transactions now; a nil watch counts nothing.
func (x *txWatch) see() {
	if x == nil {
		return
	}
	n, err := x.count()
	x.mu.Lock()
	defer x.mu.Unlock()
	x.seen++
	if err != nil {
		x.errs = append(x.errs, err)
	}
	x.open = max(x.open, n)
}

func (x *txWatch) count() (int, error) {
	conn, err := pgx.Connect(x.w.ctx, x.w.owner.Config().ConnString())
	if err != nil {
		return 0, err
	}
	defer func() { _ = conn.Close(x.w.ctx) }()
	var n int
	err = conn.QueryRow(x.w.ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()
		AND pid <> pg_backend_pid() AND state LIKE 'idle in transaction%'`).Scan(&n)
	return n, err
}

// mustSeeNone fails unless a request was watched and no transaction was
// open during any.
func (x *txWatch) mustSeeNone(what string) {
	x.w.t.Helper()
	x.mu.Lock()
	defer x.mu.Unlock()
	switch {
	case len(x.errs) > 0:
		x.w.t.Fatalf("%s: counting open transactions: %v", what, x.errs[0])
	case x.seen == 0:
		x.w.t.Fatalf("%s: no request watched", what)
	case x.open != 0:
		x.w.t.Fatalf("%s: %d transactions open during the read", what, x.open)
	}
}
