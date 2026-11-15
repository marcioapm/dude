package delivery

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// The project's authorQuestionTimeoutMinutes: unset, zero, negative or not
// a whole number of minutes is the default 3 days, and never an error (a
// wait pass reads it every time); a positive whole number is minutes.
func TestAuthorQuestionTimeoutFromThePolicy(t *testing.T) {
	w := newGoldenWorld(t)
	s := &Store{}
	for _, c := range []struct {
		policy string
		want   time.Duration
	}{
		{`{}`, 72 * time.Hour},
		{`{"authorQuestionTimeoutMinutes":5}`, 5 * time.Minute},
		{`{"authorQuestionTimeoutMinutes":0}`, 72 * time.Hour},
		{`{"authorQuestionTimeoutMinutes":-3}`, 72 * time.Hour},
		{`{"authorQuestionTimeoutMinutes":"x"}`, 72 * time.Hour},
		{`{"authorQuestionTimeoutMinutes":"1.5"}`, 72 * time.Hour},
		{`{"authorQuestionTimeoutMinutes":1.5}`, 72 * time.Hour},
		{`{"authorQuestionTimeoutMinutes":null}`, 72 * time.Hour},
		{`{"authorQuestionTimeoutMinutes":99999999999}`, 72 * time.Hour},
	} {
		w.exec(t, `UPDATE projects SET delivery_policy = $2::jsonb WHERE id = 'prj_g' AND organization_id = $1`, c.policy)
		var got time.Duration
		w.in(t, func(ctx context.Context, tx pgx.Tx) (_ string, err error) {
			got, err = s.authorQuestionTimeoutTx(ctx, tx, "prj_g")
			return "", err
		})
		if got != c.want {
			t.Errorf("%s: %s, want %s", c.policy, got, c.want)
		}
	}
	// The Store's own (tests) over the project's.
	w.exec(t, `UPDATE projects SET delivery_policy = '{"authorQuestionTimeoutMinutes":5}' WHERE id = 'prj_g' AND organization_id = $1`)
	s.AuthorQuestionTimeout = time.Second
	w.in(t, func(ctx context.Context, tx pgx.Tx) (string, error) {
		got, err := s.authorQuestionTimeoutTx(ctx, tx, "prj_g")
		if got != time.Second {
			t.Errorf("the Store's override: %s", got)
		}
		return "", err
	})
}
