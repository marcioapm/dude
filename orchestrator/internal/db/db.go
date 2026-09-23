// Package db is the orchestrator's access to the shared Postgres.
//
// Two ways in, mirroring the backend's (apps/control-plane/src/db/client.ts):
//
//   - InOrg scopes a transaction to one organization. Row-level security does
//     the isolation: the connection's role has neither SUPERUSER nor BYPASSRLS,
//     so a query that forgets a tenant filter still sees only that tenant.
//   - InSystem is for background work that has to look across tenants to find
//     the ones needing attention. It switches to dude_sweeper for that
//     transaction only; once it has a row, work continues in InOrg with the
//     row's own organization.
package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DB struct{ Pool *pgxpool.Pool }

func Open(ctx context.Context, url string) (*DB, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return &DB{Pool: pool}, nil
}

func (d *DB) Close() { d.Pool.Close() }

// InOrg runs fn in a transaction scoped to organizationID.
//
// set_config(..., true) is transaction-local, so the tenant can never leak to
// the next user of the pooled connection.
func (d *DB) InOrg(ctx context.Context, organizationID string, fn func(pgx.Tx) error) error {
	if organizationID == "" {
		return errors.New("InOrg requires an organization id")
	}
	return pgx.BeginFunc(ctx, d.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.organization_id', $1, true)`, organizationID); err != nil {
			return err
		}
		return fn(tx)
	})
}

// InSystem runs fn across tenants as the named background task.
//
// SET LOCAL ROLE is transaction-scoped, so the elevated role is dropped on
// commit or rollback. The name shows up in pg_stat_activity, so a slow sweep
// is identifiable without guessing.
func (d *DB) InSystem(ctx context.Context, task string, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, d.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL ROLE dude_sweeper`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('application_name', $1, true)`, "dude:"+task); err != nil {
			return err
		}
		return fn(tx)
	})
}

// IsNotFound reports whether err is a query that matched no row.
func IsNotFound(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
