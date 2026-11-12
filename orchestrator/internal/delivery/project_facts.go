package delivery

import (
	"context"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
)

// What every conductor briefing and every Run's first prompt carries of its
// project's memory: the facts pinned on the project (migration 262), so an
// agent has them in front of it without thinking to search. Bounded in
// code, not a setting: at most ProjectFactsMax facts, each fact's text cut
// at projectFactChars, and at most projectFactsBytes of facts in all. 20
// facts of about 200 bytes is ~4KB, a few percent of a first prompt; a
// project that pins more than a briefing can carry is told how many more
// there are, and search finds them.
const (
	ProjectFactsMax   = 20
	projectFactsBytes = 4000
	projectFactChars  = 600
)

type projectFact struct {
	ID, Title, Text, By string
	pinnedAt            time.Time
	cut                 bool
}

// ProjectFacts is the "Project facts" section of a briefing or a Run's
// first prompt: project's live pinned facts, "" for none. Over the bound,
// the most recently confirmed or edited are kept (updated_at, which an
// edit or a pin moves), shown oldest pin first, and the section says how
// many more are pinned. Only the project's own: an organization's, a
// session's or another project's memory is never pinned to it.
func ProjectFacts(ctx context.Context, tx pgx.Tx, projectID string) (string, error) {
	if projectID == "" {
		return "", nil
	}
	// Each fact's text is trimmed as strings.TrimSpace trims (factSpace)
	// and read only as far as clipFact can show, with whether the whole of
	// it, trimmed, is longer: whitespace at the cut does not hide the cut.
	// At most ProjectFactsMax of them, and the count of all of them for the
	// "N more" line. Who pinned it is named as memory's selectMemory names
	// it: keep the two alike.
	rows, err := tx.Query(ctx, `SELECT m.id, m.title, left(btrim(m.content, $4), $2),
			char_length(btrim(m.content, $4)) > $2,
			coalesce((SELECT name FROM people WHERE id = m.pinned_by_person_id),
				(SELECT 'the ' || replace(role::text, '_', ' ') FROM runs WHERE id = m.pinned_by_run_id), ''),
			m.pinned_at, count(*) OVER ()
		FROM memories m
		WHERE m.project_id = $1 AND m.pinned_at IS NOT NULL AND m.archived_at IS NULL AND m.session_id IS NULL
		ORDER BY greatest(m.updated_at, m.pinned_at) DESC, m.id
		LIMIT $3`, projectID, projectFactChars, ProjectFactsMax, factSpace)
	if err != nil {
		return "", err
	}
	total := 0
	all, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (projectFact, error) {
		var f projectFact
		return f, r.Scan(&f.ID, &f.Title, &f.Text, &f.cut, &f.By, &f.pinnedAt, &total)
	})
	if err != nil || len(all) == 0 {
		return "", err
	}
	var kept []projectFact
	used := 0
	for _, f := range all {
		f.Title = oneLine(f.Title)
		f.Text = strings.ReplaceAll(clipFact(f.Text, f.cut), "\n", "\n  ")
		size := len(f.Title) + len(f.Text) + len(f.By) + len(f.ID)
		if len(kept) > 0 && used+size > projectFactsBytes {
			break
		}
		used += size
		kept = append(kept, f)
	}
	// Shown oldest pin first: a stable order, the newest last.
	slices.SortFunc(kept, func(a, b projectFact) int {
		if c := a.pinnedAt.Compare(b.pinnedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return render(projectFactsTmpl, struct {
		Facts []projectFact
		More  int
	}{kept, total - len(kept)}), nil
}

// clipFact cuts a fact's text, trimmed and read to projectFactChars runes,
// at projectFactChars-1 and an ellipsis when the whole of it is longer
// (cut), saying so: the whole of it is one get_memory away. A fence it
// leaves open, cut or not, is closed: the facts after it, and the prompt
// after them, are not read as code.
func clipFact(s string, cut bool) string {
	if r := []rune(s); cut && len(r) >= projectFactChars-1 {
		s = string(r[:projectFactChars-1]) + "…"
	}
	return closeFence(s)
}

// factSpace is every rune strings.TrimSpace trims (unicode.IsSpace), for
// btrim: a fact is trimmed in the database as it was in Go, so its length,
// and whether it is cut, are the same.
var factSpace = func() string {
	var b strings.Builder
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if unicode.IsSpace(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}()
