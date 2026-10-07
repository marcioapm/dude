package delivery

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// A phase Run that makes no progress, told to whoever decides what to do:
// the task's conductor (one stalled wake for every such Run of the task),
// or, on a plain delivery, its owner (once per Run). dude gathers the facts
// (phases.Syncer.reportStalls); it judges nothing and restarts nothing.

// WakeStalled is the conductor_wakes kind for a phase Run making no progress.
const WakeStalled = "stalled"

// EvRunStalled is a report that a phase Run made no progress. Payload:
// Stall, as JSON. On a plain delivery it is what its owner is told.
const EvRunStalled = "run.stalled"

// How long a conducted task's Run may make no progress before its
// conductor is told, and how long before it is told again of the same
// facts. A Run whose facts changed may be told again after StallWindow.
const (
	StallWindow    = 30 * time.Minute
	StallSameFacts = 60 * time.Minute
)

// Stall is the facts of one Run making no progress, gathered with no model.
type Stall struct {
	RunID    string `json:"runId"`
	Role     string `json:"role"`
	Phase    string `json:"phase"`
	Round    int    `json:"round"`
	Category string `json:"category,omitempty"`
	Tier     string `json:"tier,omitempty"`
	// Seconds since it started, and of the window it made no progress in.
	RunningSecs int64 `json:"runningSecs"`
	WindowSecs  int64 `json:"windowSecs"`
	// "call": a tool call open the whole window; "files": a Run that
	// changes code whose files did not change in it.
	Reasons []string   `json:"reasons"`
	Calls   []OpenCall `json:"calls"`
	// The container's processes as `ps` listed them, trimmed; or why they
	// could not be read.
	Processes []Process `json:"processes"`
	PSError   string    `json:"psError,omitempty"`
	// CPU seconds and network bytes since the last report, or since the
	// Run started (UsageSecs ago), from lux's counters; nil when lux
	// reported none.
	CPUSecs   *float64 `json:"cpuSecs,omitempty"`
	NetBytes  *int64   `json:"netBytes,omitempty"`
	UsageSecs int64    `json:"usageSecs,omitempty"`
	// A Run that changes code: when its files last changed, its tool calls
	// in the window by tool, its latest plan and its last message.
	FilesChangedAt *time.Time     `json:"filesChangedAt,omitempty"`
	ToolsInWindow  map[string]int `json:"toolsInWindow,omitempty"`
	Plan           []string       `json:"plan,omitempty"`
	LastMessage    string         `json:"lastMessage,omitempty"`
}

// OpenCall is a tool call open in a stalled Run.
type OpenCall struct {
	Tool     string `json:"tool"`
	Input    string `json:"input"`
	OpenSecs int64  `json:"openSecs"`
	// Whether a process is expected for it: a command runs as one; the
	// agent's own tools run inside it. Alive is meaningful only for a
	// command, and only when the processes were read.
	Command bool `json:"command"`
	Alive   bool `json:"alive"`
}

// Process is one line of the container's `ps`.
type Process struct {
	PID     int     `json:"pid"`
	Elapsed string  `json:"elapsed"`
	CPU     float64 `json:"cpu"`
	Args    string  `json:"args"`
}

// Text is the report in words, for the conductor's wake and the owner's
// banner: the Run, its open calls, its processes, its CPU and network, and
// for a code-changing Run its files, tools, plan and last message. The
// options line is the reader's.
func (s Stall) Text(options string) string {
	var b strings.Builder
	who := s.Phase
	if s.Category != "" {
		who = s.Category + " " + who
	}
	detail := []string{}
	if s.Round > 0 {
		detail = append(detail, fmt.Sprintf("round %d", s.Round))
	}
	if s.Tier != "" {
		detail = append(detail, s.Tier)
	}
	detail = append(detail, "running "+duration(s.RunningSecs))
	fmt.Fprintf(&b, "Your %s Run %s (%s) has made no progress for %s.", who, s.RunID, strings.Join(detail, ", "), duration(s.WindowSecs))
	if len(s.Calls) > 0 {
		fmt.Fprintf(&b, " It has %s open:", plural(len(s.Calls), "tool call"))
		for i, c := range s.Calls {
			sep := ","
			if i == len(s.Calls)-1 {
				sep = ";"
			}
			fmt.Fprintf(&b, " `%s` %q, for %s%s", c.Tool, c.Input, duration(c.OpenSecs), sep)
		}
		b.WriteString(" " + s.callsLine())
	}
	if s.PSError != "" {
		fmt.Fprintf(&b, " Processes: could not be read (%s).", s.PSError)
	} else if len(s.Processes) > 0 {
		ps := make([]string, len(s.Processes))
		for i, p := range s.Processes {
			ps[i] = fmt.Sprintf("%s (%.1f %% CPU, up %s)", p.Args, p.CPU, p.Elapsed)
		}
		fmt.Fprintf(&b, " Processes: %s.", strings.Join(ps, "; "))
	}
	if s.CPUSecs != nil {
		fmt.Fprintf(&b, " CPU over %s: %.0f s; network: %s.", duration(s.UsageSecs), *s.CPUSecs, bytesWord(*s.NetBytes))
	} else {
		b.WriteString(" lux reported no CPU or network figures.")
	}
	if s.FilesChangedAt != nil || len(s.ToolsInWindow) > 0 {
		if s.FilesChangedAt != nil {
			fmt.Fprintf(&b, " Its files last changed at %s.", s.FilesChangedAt.UTC().Format("15:04 UTC"))
		}
		fmt.Fprintf(&b, " Its tool calls in the window: %s.", toolCounts(s.ToolsInWindow))
		if len(s.Plan) > 0 {
			fmt.Fprintf(&b, " Its plan: %s.", strings.Join(s.Plan, "; "))
		}
		if s.LastMessage != "" {
			fmt.Fprintf(&b, " Its last message: %q.", s.LastMessage)
		}
	}
	if options != "" {
		b.WriteString(" " + options)
	}
	return b.String()
}

// callsLine says, for the open calls, whether a process is expected and
// whether it is there.
func (s Stall) callsLine() string {
	var parts []string
	for _, c := range s.Calls {
		switch {
		case !c.Command:
			parts = append(parts, fmt.Sprintf("`%s` runs inside the agent: no separate process is expected", c.Tool))
		case s.PSError != "":
			parts = append(parts, fmt.Sprintf("whether `%s`'s command still runs is unknown", c.Tool))
		case c.Alive:
			parts = append(parts, fmt.Sprintf("`%s`'s command is running", c.Tool))
		default:
			parts = append(parts, fmt.Sprintf("no process runs `%s`'s command", c.Tool))
		}
	}
	return strings.Join(slices.Compact(parts), "; ") + "."
}

// ConductorOptions is the conductor's options line; the owner's banner has buttons.
const ConductorOptions = "Leave it, steer it (interrupt to stop its turn), or restart_run(run, note, tier)."

func duration(secs int64) string {
	switch d := time.Duration(secs) * time.Second; {
	case d < time.Minute:
		return fmt.Sprintf("%d s", secs)
	case d < 2*time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	default:
		return fmt.Sprintf("%.1f h", d.Hours())
	}
}

func bytesWord(n int64) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// toolCounts is "41 read, 6 grep, 0 edit, 0 bash": every tool used, most
// first, and edit and bash always, whether used or not.
func toolCounts(m map[string]int) string {
	all := maps.Clone(m)
	if all == nil {
		all = map[string]int{}
	}
	for _, t := range []string{"edit", "bash"} {
		all[t] += 0
	}
	keys := slices.Collect(maps.Keys(all))
	slices.SortFunc(keys, func(a, b string) int {
		if all[a] != all[b] {
			return all[b] - all[a]
		}
		return strings.Compare(a, b)
	})
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%d %s", all[k], k)
	}
	return strings.Join(parts, ", ")
}

// RecordStallTx records a report of Run r: run.stalled with its facts, and
// for a conducted task its conductor's stalled wake, keyed per report so
// the batching window joins it to any wake already due. fingerprint and
// usage are what the next report compares with; prev is when the Run was
// last reported as the caller read it, so two sweeps report it once.
func RecordStallTx(ctx context.Context, tx pgx.Tx, r RunRef, s Stall, conducted bool, fingerprint string, usage any,
	prev *time.Time) error {
	tag, err := tx.Exec(ctx, `UPDATE runs SET stall_reported_at = now(), stall_reasons = $2, stall_fingerprint = $3,
		stall_usage = $4 WHERE id = $1 AND status = 'running' AND stall_reported_at IS NOT DISTINCT FROM $5`,
		r.RunID, s.Reasons, fingerprint, usage, prev)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	payload := map[string]any{"stall": s, "conducted": conducted}
	if !conducted {
		payload["text"] = s.Text("")
	}
	id, err := ledger.Append(ctx, tx, r.Event(EvRunStalled, ledger.ActorSystem, payload))
	if err != nil || !conducted {
		return err
	}
	_, err = RecordWakeTx(ctx, tx, r.Org, r.TaskID, WakeStalled, "stalled:"+id, s.Text(ConductorOptions))
	return err
}

// LeaveStalledTx is the owner's Leave it on a stalled Run's banner: nothing
// is asked again for this Run, and its time limit is the backstop.
func LeaveStalledTx(ctx context.Context, tx pgx.Tx, r RunRef, actor Writer) error {
	tag, err := tx.Exec(ctx, `UPDATE runs SET stall_left_at = now() WHERE id = $1 AND stall_reported_at IS NOT NULL
		AND stall_left_at IS NULL`, r.RunID)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	ev := r.Event(EvRunStallLeft, actor.ActorType, map[string]any{})
	ev.ActorID = actor.ActorID
	_, err = ledger.Append(ctx, tx, ev)
	return err
}

// EvRunStallLeft is the owner choosing to leave a stalled Run as it is.
const EvRunStallLeft = "run.stall_left"
