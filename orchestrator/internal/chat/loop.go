package chat

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The chat loop's bounds.
const (
	// DefaultWorkers is how many deliveries are routed at once when
	// Service.Workers is 0.
	DefaultWorkers = 6
	// MaxAttempts is how many times a delivery that fails transiently is
	// tried before dude gives up on it and tells the person.
	MaxAttempts = 6
	// DeliveryTimeout bounds one attempt at a delivery, every provider and
	// model call in it.
	DeliveryTimeout = 90 * time.Second
	// RoutingTimeout bounds the routing judgement: someone is waiting.
	RoutingTimeout = 20 * time.Second
	// claimLease is how long a claimed delivery is another worker's; past
	// it (a crashed process) the delivery is claimed again.
	claimLease = 3 * time.Minute
	// eventsKept is how long processed deliveries are kept, for dedupe of
	// a provider's late retries.
	eventsKept = 7 * 24 * time.Hour
	// requestsKept is how long Requests keeps a mention.
	requestsKept = 30 * 24 * time.Hour
	backoffBase  = 5 * time.Second
	backoffMax   = 10 * time.Minute
)

// threadKey is what orders deliveries: one thread's in the order received,
// different threads' at once.
func threadKey(provider, workspace, conversation, thread string) string {
	if conversation == "" {
		return ""
	}
	return strings.Join([]string{provider, workspace, conversation, thread}, "\x1f")
}

type claimed struct {
	Provider, ID, Org, Kind string
	Payload                 json.RawMessage
	Attempts                int
	Decision                json.RawMessage
}

func (s *Service) slots() chan struct{} {
	s.once.Do(func() {
		n := s.Workers
		if n <= 0 {
			n = DefaultWorkers
		}
		s.sem = make(chan struct{}, n)
	})
	return s.sem
}

// Wait blocks until every delivery a Sweep started has finished.
func (s *Service) Wait() { s.inflight.Wait() }

// Sweep claims stored deliveries and routes them on at most Workers
// goroutines: each mention, kept mention and clicked choice once, and a
// workspace's removal. A delivery is claimed with FOR UPDATE SKIP LOCKED
// and a lease, so two processes never route one at once, and only when no
// earlier delivery of its thread is still pending. It returns how many it
// started; their work goes on after it returns (Wait).
func (s *Service) Sweep(ctx context.Context) (int, error) {
	slots := s.slots()
	free := cap(slots) - len(slots)
	if free == 0 {
		return 0, nil
	}
	var batch []claimed
	// The lease is read and set on the service's clock, so a test can
	// let one run out.
	now := s.now()
	if err := s.DB.InSystem(ctx, "chat", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `WITH candidate AS (
				SELECT e.provider, e.event_id FROM chat_events e
				WHERE e.processed_at IS NULL AND e.attempts < $1
				  AND (e.next_attempt_at IS NULL OR e.next_attempt_at <= now())
				  AND (e.claimed_until IS NULL OR e.claimed_until < $4)
				  AND (e.thread_key = '' OR NOT EXISTS (SELECT 1 FROM chat_events o
				    WHERE o.thread_key = e.thread_key AND o.processed_at IS NULL AND o.attempts < $1
				      AND (o.received_at, o.event_id) < (e.received_at, e.event_id)))
				ORDER BY e.received_at LIMIT $2 FOR UPDATE SKIP LOCKED)
			UPDATE chat_events e SET claimed_until = $4::timestamptz + $3::interval FROM candidate
			WHERE e.provider = candidate.provider AND e.event_id = candidate.event_id
			RETURNING e.provider, e.event_id, e.organization_id, e.kind, e.payload, e.attempts, e.decision`,
			MaxAttempts, free, claimLease.String(), now)
		if err != nil {
			return err
		}
		batch, err = pgx.CollectRows(rows, pgx.RowToStructByPos[claimed])
		return err
	}); err != nil {
		return 0, err
	}
	for _, d := range batch {
		slots <- struct{}{}
		s.inflight.Add(1)
		go func() {
			defer func() { <-slots; s.inflight.Done() }()
			s.handle(ctx, d)
		}()
	}
	return len(batch), nil
}

// handle routes one claimed delivery and settles it: done; tried again
// later after a transient failure, longer each time (or after the
// provider's Retry-After); or, past MaxAttempts, given up with the person
// told privately.
func (s *Service) handle(parent context.Context, d claimed) {
	timeout := DeliveryTimeout
	if s.deliveryTimeout > 0 {
		timeout = s.deliveryTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	err := s.process(ctx, d)
	settle := context.WithoutCancel(parent)
	if err != nil {
		transient, after := IsTransient(err)
		switch {
		case transient && d.Attempts+1 < MaxAttempts:
			wait := backoffBase << d.Attempts
			if after > wait {
				wait = after
			}
			wait = min(wait, backoffMax)
			s.log().Warn("chat delivery failed; trying again", "provider", d.Provider, "event", d.ID, "attempt", d.Attempts+1,
				"in", wait, "error", err)
			s.settle(settle, d, err, wait)
			return
		case transient:
			s.log().Error("chat delivery failed; giving up", "provider", d.Provider, "event", d.ID, "attempts", d.Attempts+1, "error", err)
			s.giveUp(settle, d, err, true)
		default:
			// Trying again would fail the same way: the person is told now.
			s.log().Warn("chat delivery failed", "provider", d.Provider, "event", d.ID, "error", err)
			s.giveUp(settle, d, err, false)
		}
	}
	s.settle(settle, d, err, 0)
}

// settle records an attempt's outcome: processed (retry 0), or due again
// after retry.
func (s *Service) settle(ctx context.Context, d claimed, failure error, retry time.Duration) {
	var msg *string
	if failure != nil {
		m := clip(failure.Error(), 2000)
		msg = &m
	}
	err := s.DB.InOrg(ctx, d.Org, func(tx pgx.Tx) error {
		if retry > 0 {
			_, err := tx.Exec(ctx, `UPDATE chat_events SET attempts = attempts + 1, last_error = $3, claimed_until = NULL,
				next_attempt_at = now() + $4::interval WHERE provider = $1 AND event_id = $2`, d.Provider, d.ID, msg, retry.String())
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE chat_events SET processed_at = now(), claimed_until = NULL, last_error = $3,
			attempts = attempts + CASE WHEN $3::text IS NULL THEN 0 ELSE 1 END WHERE provider = $1 AND event_id = $2`, d.Provider, d.ID, msg)
		return err
	})
	if err != nil {
		// The lease runs out and the delivery is claimed again.
		s.log().Error("settling a chat delivery failed", "provider", d.Provider, "event", d.ID, "error", err)
	}
}

// IsTransient is whether err is worth trying again, and no sooner than
// when: a provider's TransientError, a deadline, the network, or the
// database's serialisation, deadlock, lock or connection failures.
func IsTransient(err error) (bool, time.Duration) {
	var t *TransientError
	if errors.As(err, &t) {
		return true, t.RetryAfter
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true, 0
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true, 0
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch {
		case pg.Code == "40001", pg.Code == "40P01", pg.Code == "55P03", pg.Code == "57014",
			strings.HasPrefix(pg.Code, "08"), strings.HasPrefix(pg.Code, "53"), strings.HasPrefix(pg.Code, "57P"):
			return true, 0
		}
		return false, 0
	}
	if pgconn.SafeToRetry(err) || pgconn.Timeout(err) {
		return true, 0
	}
	return false, 0
}

// retainBatch bounds one retention delete, so it holds no lock for long.
const retainBatch = 1000

// Retain deletes, across organisations, Requests older than 30 days and
// deliveries processed (or given up) over 7 days ago, at most retainBatch
// of each per call; it returns how many went. It runs apart from routing,
// so no routing transaction waits on it.
func (s *Service) Retain(ctx context.Context) (int, error) {
	var n int
	err := s.DB.InSystem(ctx, "chat-retention", func(tx pgx.Tx) error {
		// No ORDER BY: everything past the window goes, in whatever order
		// the BRIN index finds it.
		tag, err := tx.Exec(ctx, `DELETE FROM chat_requests WHERE id IN (SELECT id FROM chat_requests
			WHERE created_at < now() - $1::interval LIMIT $2)`, requestsKept.String(), retainBatch)
		if err != nil {
			return err
		}
		n += int(tag.RowsAffected())
		tag, err = tx.Exec(ctx, `DELETE FROM chat_events WHERE (provider, event_id) IN (SELECT provider, event_id FROM chat_events
			WHERE received_at < now() - $1::interval AND (processed_at IS NOT NULL OR attempts >= $2) ORDER BY received_at LIMIT $3)`,
			eventsKept.String(), MaxAttempts, retainBatch)
		n += int(tag.RowsAffected())
		return err
	})
	return n, err
}

// serviceState is what a Service keeps between sweeps.
type serviceState struct {
	once     sync.Once
	sem      chan struct{}
	inflight sync.WaitGroup
	// Runs after the routing decision is kept and before it is acted on;
	// tests fail or change the world there.
	beforeMake func(ctx context.Context) error
	// DeliveryTimeout, shortened by tests.
	deliveryTimeout time.Duration
	// Where SettleMarks reads its next page; only its loop calls it.
	marks markPage
}
