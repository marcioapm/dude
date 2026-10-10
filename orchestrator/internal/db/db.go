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
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DB struct{ Pool *pgxpool.Pool }

// Open connects a pool whose times are UTC at both ends. A timestamptz is
// scanned into a time.Time in UTC, not the process's zone, so it encodes to
// JSON as "…Z" like every other time the browser merges it with; and the
// session's TimeZone is UTC, so a time Postgres renders itself (json_build_object,
// ::text) says the same instant in the same zone, whatever the server's default.
func Open(ctx context.Context, url string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	cfg.AfterConnect = func(_ context.Context, conn *pgx.Conn) error {
		scanUTC(conn.TypeMap())
		return nil
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return &DB{Pool: pool}, nil
}

// scanUTC replaces timestamptz, and the array type built on it, with
// codecs that scan into UTC.
func scanUTC(m *pgtype.Map) {
	tz := &pgtype.Type{Name: "timestamptz", OID: pgtype.TimestamptzOID, Codec: &pgtype.TimestamptzCodec{ScanLocation: time.UTC}}
	m.RegisterType(tz)
	m.RegisterType(&pgtype.Type{Name: "_timestamptz", OID: pgtype.TimestamptzArrayOID, Codec: &pgtype.ArrayCodec{ElementType: tz}})
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

// Nullable turns "" into SQL NULL. The scope columns are nullable, and an
// empty string would read as "belongs to the thing with no id".
func Nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// NonNil makes a nil slice empty, so it encodes as [] rather than null.
func NonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// LikeLiteral escapes text for LIKE: % and _ match themselves.
func LikeLiteral(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
