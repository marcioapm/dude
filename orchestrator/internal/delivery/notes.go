package delivery

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// Epic notes: short working facts every agent on an epic must follow,
// numbered per epic and never edited (docs/design/agent-coordination.md).
// A note is pinned by an agent, a person or a filed brainstorm proposal;
// replaced by a newer one; or retired by a person. Every change is told to
// each live phase Run on the epic at its next step, as a directive of its
// own; a Run that starts later reads the current notes in its briefing.

// Bounds: current notes per epic, and a note's characters.
const (
	NotesMax    = 10
	NoteTextMax = 2000
)

// NotesLimitText is the refusal of a note past the limit, whoever pins it.
var NotesLimitText = fmt.Sprintf("This epic has %d notes. Replace one (`dude note --replaces N`) instead.", NotesMax)

// NoteRefusal is a note refused: Reason is limit, replaced, no_note,
// too_long or empty; Current the epic's current notes then.
type NoteRefusal struct {
	Reason  string
	Msg     string
	Current int
}

func (r NoteRefusal) Error() string { return r.Msg }

// Note is a note as agents and people read it.
type Note struct {
	ID        string     `json:"id"`
	N         int        `json:"n"`
	Text      string     `json:"text"`
	By        string     `json:"by"`
	ByRun     string     `json:"byRunId,omitempty"`
	ByPerson  string     `json:"byPersonId,omitempty"`
	Session   string     `json:"fromSessionId,omitempty"`
	Replaces  int        `json:"replaces,omitempty"`
	At        time.Time  `json:"at"`
	Replaced  *time.Time `json:"replacedAt,omitempty"`
	Retired   *time.Time `json:"retiredAt,omitempty"`
	RetiredBy string     `json:"retiredBy,omitempty"`
}

// PinInput is one note pinned: by an agent's Run (Run) or a person
// (Person, from a brainstorm session's proposal when Session), replacing
// note Replaces when it is not 0. Actor is the ledger's actor id for a
// person's (their key, or themselves).
type PinInput struct {
	EpicID, Text           string
	Replaces               int
	Run                    RunRef
	Person, Session, Actor string
}

// Pinned is a note pinned: its number, the note it replaced, how many live
// agents are told at their next step, and the epic's current notes after.
type Pinned struct {
	ID       string `json:"id"`
	N        int    `json:"n"`
	Replaces int    `json:"replaces,omitempty"`
	Told     int    `json:"told"`
	Current  []Note `json:"current"`
}

// LockEpicTx takes the epic's row for the rest of tx: pins, replaces and
// retirements on one epic are counted and numbered one at a time. NO KEY
// UPDATE, so tasks inserted under it (FOR KEY SHARE) are not held up.
func LockEpicTx(ctx context.Context, tx pgx.Tx, epicID string) (projectID string, err error) {
	err = tx.QueryRow(ctx, `SELECT project_id FROM epics WHERE id = $1 FOR NO KEY UPDATE`, epicID).Scan(&projectID)
	return projectID, err
}

// PinNoteTx pins a note on the epic, or replaces one, and tells the
// epic's live agents. Refused with NoteRefusal: past NotesMax current notes
// (a replace never counts), or replacing a note that is not current.
func PinNoteTx(ctx context.Context, tx pgx.Tx, org string, in PinInput) (Pinned, error) {
	text := strings.TrimSpace(in.Text)
	switch {
	case text == "":
		return Pinned{}, NoteRefusal{Reason: "empty", Msg: "a note needs its text"}
	case len([]rune(text)) > NoteTextMax:
		return Pinned{}, NoteRefusal{Reason: "too_long", Msg: fmt.Sprintf("a note is at most %d characters: a short working fact", NoteTextMax)}
	}
	projectID, err := LockEpicTx(ctx, tx, in.EpicID)
	if err != nil {
		return Pinned{}, err
	}
	var current int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM epic_notes WHERE epic_id = $1 AND replaced_at IS NULL AND retired_at IS NULL`,
		in.EpicID).Scan(&current); err != nil {
		return Pinned{}, err
	}
	var was *Note
	if in.Replaces != 0 {
		old, err := noteByN(ctx, tx, in.EpicID, in.Replaces)
		if err != nil {
			return Pinned{}, err
		}
		if refused := notCurrent(ctx, tx, in.EpicID, old, in.Replaces, current); refused != nil {
			return Pinned{}, refused
		}
		was = old
	} else if current >= NotesMax {
		return Pinned{}, NoteRefusal{Reason: "limit", Msg: NotesLimitText, Current: current}
	}
	var by string
	if in.Run.RunID != "" {
		if by, err = RunLabel(ctx, tx, in.Run.RunID); err != nil {
			return Pinned{}, err
		}
	} else if by, err = PersonName(ctx, tx, in.Person); err != nil {
		return Pinned{}, err
	}
	if by == "" {
		by = "Someone"
	}
	var out Pinned
	out.ID = ids.New(ids.EpicNote)
	var replacesID *string
	if was != nil {
		replacesID = &was.ID
		out.Replaces = was.N
		if _, err := tx.Exec(ctx, `UPDATE epic_notes SET replaced_at = now() WHERE id = $1`, was.ID); err != nil {
			return Pinned{}, err
		}
	}
	if err := tx.QueryRow(ctx, `INSERT INTO epic_notes (id, organization_id, epic_id, n, text, by_run_id, by_person, from_session_id,
			replaces_id, by_label)
		VALUES ($1, $2, $3, (SELECT COALESCE(max(n), 0) + 1 FROM epic_notes WHERE epic_id = $3), $4, NULLIF($5, ''), NULLIF($6, ''),
			NULLIF($7, ''), $8, $9)
		RETURNING n`, out.ID, org, in.EpicID, text, in.Run.RunID, in.Person, in.Session, replacesID, by).Scan(&out.N); err != nil {
		return Pinned{}, err
	}
	payload := map[string]any{"note": out.ID, "n": out.N, "epicId": in.EpicID, "by": by, "text": text,
		"replaces": nil, "from_session": db.Nullable(in.Session)}
	if was != nil {
		payload["replaces"] = was.N
	}
	if err := noteEvent(ctx, tx, org, projectID, in, EvNotePinned, payload); err != nil {
		return Pinned{}, err
	}
	change, line := "added", fmt.Sprintf("Epic note added: %d · %s · %s", out.N, by, text)
	if was != nil {
		change, line = "replaced", fmt.Sprintf("Epic note replaced: %d → %d · %s · %s\n(was: %s)", was.N, out.N, by, text, was.Text)
	}
	if out.Told, err = announceNoteTx(ctx, tx, org, in.EpicID, out.ID, in.Run.RunID, change, line); err != nil {
		return Pinned{}, err
	}
	out.Current, err = CurrentNotes(ctx, tx, in.EpicID)
	return out, err
}

// notCurrent refuses replacing a note that is not current: none by that
// number, or one already replaced or retired, naming what took its place.
func notCurrent(ctx context.Context, tx pgx.Tx, epicID string, old *Note, n, current int) error {
	switch {
	case old == nil:
		return NoteRefusal{Reason: "no_note", Msg: fmt.Sprintf("this epic has no note %d", n), Current: current}
	case old.Replaced != nil:
		var next int
		if err := tx.QueryRow(ctx, `SELECT n FROM epic_notes WHERE replaces_id = $1`, old.ID).Scan(&next); err != nil {
			return err
		}
		return NoteRefusal{Reason: "replaced", Current: current, Msg: fmt.Sprintf("note %d was replaced by note %d at %s: replace note %d",
			n, next, old.Replaced.UTC().Format("15:04"), next)}
	case old.Retired != nil:
		return NoteRefusal{Reason: "retired", Current: current, Msg: fmt.Sprintf("note %d was retired at %s: pin a new note instead",
			n, old.Retired.UTC().Format("15:04"))}
	}
	return nil
}

// RetireNoteTx takes a current note off the epic, by a person, with no
// replacement, and tells the epic's live agents. Agents never retire.
func RetireNoteTx(ctx context.Context, tx pgx.Tx, org, epicID string, n int, person string, w Writer) (Note, error) {
	projectID, err := LockEpicTx(ctx, tx, epicID)
	if err != nil {
		return Note{}, err
	}
	old, err := noteByN(ctx, tx, epicID, n)
	if err != nil {
		return Note{}, err
	}
	if refused := notCurrent(ctx, tx, epicID, old, n, 0); refused != nil {
		return Note{}, refused
	}
	name, err := PersonName(ctx, tx, person)
	if err != nil {
		return Note{}, err
	}
	if name == "" {
		name = "Someone"
	}
	if _, err := tx.Exec(ctx, `UPDATE epic_notes SET retired_at = now(), retired_by = NULLIF($2, '') WHERE id = $1`, old.ID, person); err != nil {
		return Note{}, err
	}
	ev := ledger.Event{Type: EvNoteRetired, OrganizationID: org, ProjectID: projectID, ActorType: w.ActorType, ActorID: w.ActorID,
		Source: ledger.SourceOrchestrator, CorrelationID: epicID,
		Payload: map[string]any{"note": old.ID, "n": old.N, "epicId": epicID, "by": name}}
	if _, err := ledger.Append(ctx, tx, ev); err != nil {
		return Note{}, err
	}
	if _, err := announceNoteTx(ctx, tx, org, epicID, old.ID, "", "retired",
		fmt.Sprintf("Epic note retired: %d · %s · no longer in force: %s", old.N, name, old.Text)); err != nil {
		return Note{}, err
	}
	retired, err := noteByN(ctx, tx, epicID, n)
	if err != nil {
		return Note{}, err
	}
	return *retired, nil
}

// noteEvent records epic.note.pinned: on the pinning Run for an agent's,
// on the epic (its correlation) for a person's.
func noteEvent(ctx context.Context, tx pgx.Tx, org, projectID string, in PinInput, typ string, payload map[string]any) error {
	if in.Run.RunID != "" {
		ev := in.Run.Event(typ, ledger.ActorAgent, payload)
		_, err := ledger.Append(ctx, tx, ev)
		return err
	}
	actorType, actorID := ledger.ActorHuman, in.Actor
	if actorID == "" {
		actorID = in.Person
	}
	_, err := ledger.Append(ctx, tx, ledger.Event{Type: typ, OrganizationID: org, ProjectID: projectID, ActorType: actorType,
		ActorID: actorID, Source: ledger.SourceOrchestrator, CorrelationID: in.EpicID, Payload: payload})
	return err
}

// announceNoteTx queues the change's line for every live phase Run on the
// epic's tasks but the one that made it: one directive each, recorded in
// epic_note_reads, told when read (notesToldTx). Conductors are not told at
// a step — a directive would wake a parked one — but in their digest.
func announceNoteTx(ctx context.Context, tx pgx.Tx, org, epicID, noteID, except, change, line string) (int, error) {
	rows, err := tx.Query(ctx, `SELECT r.id, r.project_id, r.task_id FROM runs r JOIN tasks t ON t.id = r.task_id
		WHERE t.epic_id = $1 AND r.phase IS NOT NULL AND r.kind = 'agent' AND `+liveRun+`
		  AND r.control <> 'abort' AND r.push_request_id IS NULL AND r.id <> $2
		ORDER BY r.created_at FOR NO KEY UPDATE OF r`, epicID, except)
	if err != nil {
		return 0, err
	}
	type live struct{ ID, Project, Task string }
	runs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[live])
	if err != nil {
		return 0, err
	}
	for _, r := range runs {
		ref := RunRef{Org: org, ProjectID: r.Project, TaskID: r.Task, RunID: r.ID}
		id, _, err := QueueDirective(ctx, tx, ref, Directive{Text: line, Scope: "run"})
		if err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO epic_note_reads (note_id, run_id, change, organization_id, directive_id)
			VALUES ($1, $2, $3, $4, $5) ON CONFLICT (note_id, run_id, change) DO UPDATE SET directive_id = EXCLUDED.directive_id`,
			noteID, r.ID, change, org, id); err != nil {
			return 0, err
		}
	}
	return len(runs), nil
}

// notesToldTx settles the note changes a directive carried, once read:
// read_at, and agent.note.told on the Run.
func notesToldTx(ctx context.Context, tx pgx.Tx, org, directiveID string) error {
	rows, err := tx.Query(ctx, `UPDATE epic_note_reads nr SET read_at = now()
		FROM epic_notes n, runs r
		WHERE nr.directive_id = $1 AND nr.read_at IS NULL AND n.id = nr.note_id AND r.id = nr.run_id
		RETURNING n.id, n.n, nr.change, COALESCE((SELECT o.n FROM epic_notes o WHERE o.id = n.replaces_id), 0), r.id,
			COALESCE(r.project_id, ''), COALESCE(r.task_id, ''),
			(SELECT e.payload->>'tool' FROM events e WHERE e.run_id = r.id AND e.event_type = 'agent.tool.called'
			 ORDER BY e.cursor DESC LIMIT 1)`, directiveID)
	if err != nil {
		return err
	}
	type told struct {
		Note               string
		N                  int
		Change             string
		Was                int
		Run, Project, Task string
		After              *string
	}
	reads, err := pgx.CollectRows(rows, pgx.RowToStructByPos[told])
	if err != nil {
		return err
	}
	for _, t := range reads {
		ref := RunRef{Org: org, ProjectID: t.Project, TaskID: t.Task, RunID: t.Run}
		payload := map[string]any{"note": t.Note, "n": t.N, "change": t.Change, "was": nil, "after": t.After, "directive": directiveID}
		if t.Was != 0 {
			payload["was"] = t.Was
		}
		if _, err := ledger.Append(ctx, tx, ref.Event(EvNoteTold, ledger.ActorSystem, payload)); err != nil {
			return err
		}
	}
	return nil
}

// noteColumns is a Note's columns, over epic_notes n.
const noteColumns = `n.id, n.n, n.text, n.by_label, COALESCE(n.by_run_id, ''), COALESCE(n.by_person, ''),
	COALESCE(n.from_session_id, ''), COALESCE((SELECT o.n FROM epic_notes o WHERE o.id = n.replaces_id), 0), n.created_at,
	n.replaced_at, n.retired_at, COALESCE((SELECT p.name FROM people p WHERE p.id = n.retired_by), '')`

func scanNote(row pgx.Row) (Note, error) {
	var n Note
	err := row.Scan(&n.ID, &n.N, &n.Text, &n.By, &n.ByRun, &n.ByPerson, &n.Session, &n.Replaces, &n.At, &n.Replaced, &n.Retired,
		&n.RetiredBy)
	return n, err
}

// noteByN is the epic's note n, nil for none.
func noteByN(ctx context.Context, tx pgx.Tx, epicID string, n int) (*Note, error) {
	note, err := scanNote(tx.QueryRow(ctx, `SELECT `+noteColumns+` FROM epic_notes n WHERE n.epic_id = $1 AND n.n = $2`, epicID, n))
	if db.IsNotFound(err) {
		return nil, nil
	}
	return &note, err
}

// CurrentNotes are the epic's notes in force, by number.
func CurrentNotes(ctx context.Context, tx pgx.Tx, epicID string) ([]Note, error) {
	rows, err := tx.Query(ctx, `SELECT `+noteColumns+` FROM epic_notes n
		WHERE n.epic_id = $1 AND n.replaced_at IS NULL AND n.retired_at IS NULL ORDER BY n.n`, epicID)
	if err != nil {
		return nil, err
	}
	out := []Note{}
	for rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// TaskEpic is the epic a task is on, "" for none.
func TaskEpic(ctx context.Context, tx pgx.Tx, taskID string) (string, error) {
	var epic *string
	err := tx.QueryRow(ctx, `SELECT epic_id FROM tasks WHERE id = $1`, taskID).Scan(&epic)
	if err != nil || epic == nil {
		return "", err
	}
	return *epic, nil
}
