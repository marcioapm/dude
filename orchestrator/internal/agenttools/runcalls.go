package agenttools

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// run_calls (capabilities.ChecksDecide) lists the shell calls a writer Run
// of the task, and its subagents, started after a commit was made, newest
// first, each with its command, its recorded exit and its output's tail:
// the evidence a checks wake shows (delivery.ChecksEvidence), for any
// commit and filtered. The commit is read in the Run's container (one
// exec, delivery.CheckoutScript), as the gate reads it; with the container
// not running, the latest checks ask's reading of the Run's HEAD stands in
// for its HEAD.

type runCallsIn struct {
	Run    string `json:"run,omitempty"`
	Commit string `json:"commit,omitempty"`
	Filter string `json:"filter,omitempty"`
}

// runCallsAsk is what run_calls checked: the Run, its container, and the
// checkouts to read there.
type runCallsAsk struct {
	RunID, LuxRunID string
	// Repos: the repositories the Run may push, read in its container.
	Repos  []string
	Commit string
	Filter string
	// Asked: the latest checks ask's evidence, for a container that is
	// not running.
	Asked     *delivery.ChecksEvidence
	AskedTurn int64
}

// runCallsRead is what the exec read: the checkouts at the commit, and
// when it was sent; OK: the read accounted for every repository; Down:
// the container could not be asked.
type runCallsRead struct {
	delivery.CheckoutRead
	OK, Down bool
}

type runCallOut struct {
	At      time.Time `json:"at"`
	Command string    `json:"command"`
	Exit    string    `json:"exit"`
	Tail    string    `json:"tail,omitempty"`
	Child   string    `json:"child,omitempty"`
}

type runCallsOut struct {
	External string                  `json:"external"`
	Run      string                  `json:"run"`
	Commits  []delivery.CheckoutHead `json:"commits"`
	// TreeClean: the working tree held nothing but HEAD when read (only
	// for HEAD).
	TreeClean *bool `json:"treeClean,omitempty"`
	// From: where the commit was read ("the Run's checkout, now", or the
	// checks ask that read it).
	From  string       `json:"from"`
	Since time.Time    `json:"since"`
	Calls []runCallOut `json:"calls"`
	More  bool         `json:"more,omitempty"`
	Note  string       `json:"note"`
}

// runCallsFilterMax bounds a filter: words of a command, not a script.
const runCallsFilterMax = 200

func checkRunCalls(ctx context.Context, tx pgx.Tx, c Caller, in runCallsIn) (runCallsAsk, error) {
	ask := runCallsAsk{RunID: strings.TrimSpace(in.Run), Commit: strings.ToLower(strings.TrimSpace(in.Commit)), Filter: strings.TrimSpace(in.Filter)}
	switch {
	case c.TaskID == "":
		return ask, refuseText(rRunCallsNoTask)
	case ask.Commit != "" && (len(ask.Commit) < 7 || len(ask.Commit) > 40 || strings.Trim(ask.Commit, "0123456789abcdef") != ""):
		return ask, refuseText(rRunCallsCommit)
	case len(ask.Filter) > runCallsFilterMax:
		return ask, refuse(rRunCallsFilter, runCallsFilterMax)
	}
	var luxRun, luxState string
	var pushes []string
	q := `SELECT id, COALESCE(lux_run_id, ''), COALESCE(lux_state, ''), COALESCE(lux_pushes, '{}') FROM runs
		WHERE task_id = $1 AND phase IN ('implement', 'fix', 'simplify') AND attempt = task_attempt($1)`
	args := []any{c.TaskID}
	if ask.RunID != "" {
		q, args = q+` AND id = $2`, append(args, ask.RunID)
	}
	err := tx.QueryRow(ctx, q+` ORDER BY created_at DESC, id DESC LIMIT 1`, args...).Scan(&ask.RunID, &luxRun, &luxState, &pushes)
	if errors.Is(err, pgx.ErrNoRows) {
		if ask.RunID != "" {
			return ask, refuse(rRunCallsNotWriter, ask.RunID)
		}
		return ask, refuseText(rRunCallsNoWriter)
	}
	if err != nil {
		return ask, err
	}
	if luxState == "running" && len(pushes) > 0 {
		ask.LuxRunID, ask.Repos = luxRun, pushes
	}
	// The latest checks ask's reading, for a container not running now.
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT payload FROM events WHERE run_id = $1 AND event_type = $2 AND payload ? 'evidence'
		ORDER BY cursor DESC LIMIT 1`, ask.RunID, delivery.EvRunChecksAsked).Scan(&raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ask, err
	}
	if raw != nil {
		var a delivery.ChecksAsked
		if jsonErr := json.Unmarshal(raw, &a); jsonErr == nil && a.Evidence != nil && a.Evidence.Read {
			ask.Asked, ask.AskedTurn = a.Evidence, a.Turn
		}
	}
	return ask, nil
}

func readRunCommit(ctx context.Context, c Caller, ask runCallsAsk) (runCallsRead, error) {
	if ask.LuxRunID == "" || c.env.lux == nil {
		return runCallsRead{Down: true}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	read := runCallsRead{CheckoutRead: delivery.CheckoutRead{ReadStart: time.Now()}}
	res, err := c.env.lux.Exec(ctx, ask.LuxRunID, delivery.CheckoutCommand(ask.Commit, ask.Repos))
	if err != nil {
		if _, ok := lux.AsError(err); ok || ctx.Err() != nil {
			return runCallsRead{Down: true}, nil
		}
		return read, err
	}
	read.Checkouts, read.OK = delivery.ParseCheckouts(string(res.Stdout), res.ExitCode, slices.Compact(slices.Sorted(slices.Values(ask.Repos))))
	return read, nil
}

func listRunCalls(ctx context.Context, tx pgx.Tx, _ Caller, ask runCallsAsk, read runCallsRead) (runCallsOut, error) {
	out := runCallsOut{Run: ask.RunID}
	var since time.Time
	switch {
	case read.OK && len(read.Checkouts.Heads) > 0:
		out.Commits, out.From = read.Checkouts.Heads, runCallsText("from-checkout", nil)
		since = read.Checkouts.Since(read.ReadStart)
		if ask.Commit == "" {
			clean := read.Checkouts.Clean()
			out.TreeClean = &clean
		}
	case read.OK:
		return out, refuse(rRunCallsNoSuchCommit, ask.Commit)
	case ask.Asked != nil && (ask.Commit == "" || slices.ContainsFunc(ask.Asked.Heads, func(h delivery.CheckoutHead) bool {
		return strings.HasPrefix(h.SHA, ask.Commit)
	})):
		out.Commits, out.From, since = ask.Asked.Heads, runCallsText("from-ask", ask.AskedTurn), ask.Asked.Since
		if ask.Commit == "" {
			clean := ask.Asked.Clean
			out.TreeClean = &clean
		}
	default:
		return out, refuseText(rRunCallsUnread)
	}
	calls, more, err := delivery.RunCallsTx(ctx, tx, ask.RunID, since, ask.Filter, delivery.EvidenceCalls, delivery.EvidenceTail)
	if err != nil {
		return out, err
	}
	w := newWrapper()
	out.External, out.Since, out.More = runCallsText("external", delivery.ExternalText(w.id, "…")), since, more
	out.Calls = make([]runCallOut, len(calls))
	for i, rc := range calls {
		out.Calls[i] = runCallOut{At: rc.At, Command: delivery.ExternalText(w.id, rc.Command), Exit: rc.ExitWords(), Child: rc.Child}
		if rc.Tail != "" {
			out.Calls[i].Tail = delivery.ExternalText(w.id, rc.Tail)
		}
	}
	out.Note = runCallsText("note", struct {
		Max  int
		More bool
	}{delivery.EvidenceCalls, more})
	return out, nil
}

// runCallsText renders results/run-calls.md's text name.
func runCallsText(name string, data any) string { return runCallsTexts[name].Render(data) }

var runCallsTexts = func() map[string]delivery.Text {
	out := map[string]delivery.Text{}
	for _, name := range []string{"external", "from-checkout", "from-ask", "note"} {
		out[name] = delivery.MustText("results/run-calls/" + name)
	}
	return out
}()
