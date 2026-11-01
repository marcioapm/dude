package delivery

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"

	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// WakeRound is the conductor_wakes kind for a conducted review round's
// wakes: after its review, the round's findings at the fix bar listed with
// the fix before it; or at its pipeline's fix (start_phase then), what
// stopped the review queued behind it or that it waits.
const WakeRound = "round"

// roundLineChars bounds a round wake's text.
const roundLineChars = 2000

// roundShown is how many findings at the bar a round wake lists; the rest
// are counted, read with findings.
const roundShown = 12

// longLoop is the round from which the round wake says the loop is long:
// a fact, never a bound (owner, step 3 Q4).
const longLoop = 8

var (
	roundWakeTmpl  = mustTemplate("wakes/round")
	roundFixTmpl   = mustTemplate("wakes/facts/round-fix")
	roundRangeTmpl = mustTemplate("wakes/facts/round-range")
)

// roundFinding is a finding at the bar as a round wake lists it, scanned
// in its fields' order.
type roundFinding struct {
	ID, Severity, Title, Where string
	// yes, no, or "" for not said.
	Introduced          string
	Trivial, Unverified bool
	// On lines the fix before the round changed (touches_fix_run_id).
	Touches bool
}

// severityRank orders findings most severe first.
var severityRank = map[string]int{"blocking": 0, "high": 1, "medium": 2, "low": 3, "note": 4}

// roundLine is a conducted review round's wake: its number and what it
// reviewed, the findings open at the fix bar (most severe first, at most
// roundShown, each with what its reviewer and the layer said of it), how
// many are below it, the fix before it, and from round longLoop on that
// the loop is long. round is the round's review Runs; findings the loop's
// (loopFindings).
func (w *steps) roundLine(ctx context.Context, sc workflow.StepContext, st *State, round []string, findings []FindingState) (string, error) {
	bar, weighed := atFixBar(st, st.Policy, findings)
	var atBar []string
	below, people := 0, 0
	for _, f := range weighed {
		switch {
		case f.Human:
			if f.Status == "open" || f.Status == "delivered" {
				people++
			}
		case IsBlocking(bar, f):
			atBar = append(atBar, f.ID)
		case f.Status == "open":
			below++
		}
	}
	data := roundWake{Round: st.Round, Open: len(atBar), Below: below, People: people}
	err := w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
		var of, parent, parentPhase string
		var base map[string]string
		rows, err := tx.Query(ctx, `SELECT DISTINCT COALESCE(category, '') FROM runs WHERE id = ANY($1) ORDER BY 1`, round)
		if err != nil {
			return err
		}
		if data.Lenses, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT COALESCE(r.review_range->>'of', ''), COALESCE(r.review_range->'base', '{}'::jsonb),
				COALESCE(r.parent_run_id, ''), COALESCE(p.phase::text, '')
			FROM runs r LEFT JOIN runs p ON p.id = r.parent_run_id WHERE r.id = ANY($1) ORDER BY r.created_at, r.id LIMIT 1`,
			round).Scan(&of, &base, &parent, &parentPhase); err != nil && !db.IsNotFound(err) {
			return err
		}
		var repos []string
		for _, repo := range slices.Sorted(maps.Keys(base)) {
			repos = append(repos, fmt.Sprintf("%s@%s..%s", repo, short(base[repo]), short(st.RoundHeads[repo])))
		}
		data.Range = render(roundRangeTmpl, struct {
			Of    string
			Repos []string
		}{of, repos})
		if len(atBar) > 0 {
			rows, err := tx.Query(ctx, `SELECT id, severity::text, title, COALESCE(NULLIF(repo, '') || '/', '') || COALESCE(file, '?')
					|| CASE WHEN COALESCE(line, 0) > 0 THEN ':' || line ELSE '' END,
					CASE introduced WHEN true THEN 'yes' WHEN false THEN 'no' ELSE '' END, COALESCE(trivial, false), COALESCE(unverified, false),
					touches_fix_run_id IS NOT NULL AND touches_fix_run_id = $2
				FROM review_findings WHERE id = ANY($1)`, atBar, parent)
			if err != nil {
				return err
			}
			shown, err := pgx.CollectRows(rows, pgx.RowToStructByPos[roundFinding])
			if err != nil {
				return err
			}
			order := map[string]int{}
			for i, id := range atBar {
				order[id] = i
			}
			slices.SortFunc(shown, func(a, b roundFinding) int {
				return cmp.Or(cmp.Compare(severityRank[a.Severity], severityRank[b.Severity]),
					cmp.Compare(boolRank(a.Unverified), boolRank(b.Unverified)), cmp.Compare(order[a.ID], order[b.ID]))
			})
			data.Shown = shown[:min(len(shown), roundShown)]
			data.More = len(atBar) - len(data.Shown)
			data.all = shown
		}
		if parent == "" || parentPhase != PhaseFix && parentPhase != PhaseSimplify {
			return nil
		}
		out, err := phaseOutcomeTx(ctx, tx, parent)
		if err != nil {
			return err
		}
		var commits []string
		for _, repo := range slices.Sorted(maps.Keys(out.Heads)) {
			commits = append(commits, repo+"@"+short(out.Heads[repo]))
		}
		outcome := out.WriterOutcome
		if outcome == OutcomeDone {
			outcome = ""
		}
		data.Fix = render(roundFixTmpl, struct {
			Phase, RunID, Outcome, Note string
			Commits                     []string
		}{parentPhase, parent, outcome, out.WriterOutcomeNote, commits})
		return nil
	})
	if err != nil {
		return "", err
	}
	// Fit the bound with the decision it waits on: the last shown
	// findings give way, counted, so the tail (how many more, the fix,
	// the long loop) is never what the clip cuts.
	text := data.text()
	for len(data.Shown) > 0 && utf8.RuneCountInString(oneLine(decisionWaitingNote(PointReviewed, text))) > roundLineChars {
		data.Shown = data.Shown[:len(data.Shown)-1]
		data.More++
		text = data.text()
	}
	return text, nil
}

// roundWake is what a round wake says (wakes/round).
type roundWake struct {
	Round, Open, Below, People, More int
	Range                            string
	Lenses                           []string
	Shown                            []roundFinding
	Fix                              string
	// Every finding at the bar, in the order shown: those past Shown are
	// counted in More, and those of them on the fix's lines in MoreTouch.
	all []roundFinding
}

// text renders the wake; from round longLoop on it says the loop is long.
func (r roundWake) text() string {
	touch := 0
	for _, f := range r.all[min(len(r.Shown), len(r.all)):] {
		if f.Touches {
			touch++
		}
	}
	return render(roundWakeTmpl, struct {
		roundWake
		Long      bool
		MoreTouch int
	}{r, r.Round >= longLoop, touch})
}

func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}
