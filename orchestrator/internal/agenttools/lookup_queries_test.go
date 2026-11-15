package agenttools

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// statements counts the statements a pool runs, BEGIN/COMMIT and
// set_config included: each is a round trip.
type statements struct{ n atomic.Int64 }

func (c *statements) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.n.Add(1)
	return ctx
}

func (*statements) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// A task Run's token lookup reads whether its task is review only in the
// runs row it reads already: a writing Run's tool request makes no round
// trip for it, and a review-only conductor still finds ask_author.
func TestLookupReadsReviewOnlyWithTheRun(t *testing.T) {
	ctx := context.Background()
	s, owner, org := toolWorld(t)
	counter := &statements{}
	cfg, err := pgxpool.ParseConfig(s.DB.Pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Tracer = counter
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	s = &Server{DB: &db.DB{Pool: pool}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ownerExec(t, owner, `INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES ('t2', $1, 'p', 2, 'Review'),
		('t3', $1, 'p', 3, 'Adopted'), ('t4', $1, 'p', 4, 'Continued')`, org)
	ownerExec(t, owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, role, kind, status, mcp_token_hash)
		VALUES ('rw', $1, 'p', 't', 1, 'conductor', 'agent', 'running', $2), ('rr', $1, 'p', 't2', 1, 'conductor', 'agent', 'running', $3),
		('ra', $1, 'p', 't3', 1, 'conductor', 'agent', 'running', $4), ('rc', $1, 'p', 't4', 1, 'conductor', 'agent', 'running', $5)`,
		org, HashToken("tok-writing"), HashToken("tok-review"), HashToken("tok-adopted"), HashToken("tok-continued"))
	ownerExec(t, owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch) VALUES ('repo', $1, 'p', 'r', 'https://github.com/o/r', 'main')`, org)
	// A writing task adopted from a pull request (review or continue
	// mode) has a source row too: it is not review only (tests M2).
	ownerExec(t, owner, `INSERT INTO task_sources (task_id, organization_id, repository_id, kind, mode, number, head_branch, base_branch)
		VALUES ('t2', $1, 'repo', 'pr', $2, 7, 'feature/x', 'main'), ('t3', $1, 'repo', 'pr', $3, 8, 'feature/y', 'main'),
		('t4', $1, 'repo', 'pr', $4, 9, 'feature/z', 'main')`, org, delivery.ModeReviewOnly, delivery.ModeReview, delivery.ModeContinue)
	for _, c := range []struct {
		token      string
		reviewOnly bool
	}{{"tok-writing", false}, {"tok-adopted", false}, {"tok-continued", false}, {"tok-review", true}} {
		counter.n.Store(0)
		got, err := s.lookup(ctx, c.token)
		if err != nil {
			t.Fatal(err)
		}
		if n := counter.n.Load(); n > lookupStatements {
			t.Errorf("%s: the lookup ran %d statements, want at most %d", c.token, n, lookupStatements)
		}
		t.Logf("%s: %d statements", c.token, counter.n.Load())
		if got.reviewOnly != c.reviewOnly {
			t.Errorf("%s: reviewOnly %v", c.token, got.reviewOnly)
		}
		if _, ok := got.findTool("ask_author"); ok != c.reviewOnly {
			t.Errorf("%s: finds ask_author %v", c.token, ok)
		}
	}
	counter.n.Store(0)
	if _, err := s.lookup(ctx, "tok-writing"); err != nil {
		t.Fatal(err)
	}
	// The token, BEGIN, set_config, the run row, the capability layers
	// (capabilities.Resolve), COMMIT: none for task_sources alone.
	if n := counter.n.Load(); n > lookupStatements {
		t.Errorf("a writing Run's lookup ran %d statements, want at most %d", n, lookupStatements)
	}
}

// lookupStatements is a task Run's lookup at c53d7f057, before
// review-only tools: what a writing Run's lookup still costs.
const lookupStatements = 5
