package agenttools

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Coauthor is one person on a task as a commit's trailer names them.
type Coauthor struct {
	Name, Email string
}

// Trailer is the line git adds for them.
func (c Coauthor) Trailer() string { return "Co-authored-by: " + c.Name + " <" + c.Email + ">" }

// CoauthorPerson is what dude knows of a person for their trailer.
type CoauthorPerson struct {
	Name         string
	CommitEmail  string
	GitHubLogin  string
	GitHubUserID int64
	Email        string
}

// Coauthors are the trailers for people, in their order on the task: each
// by their commit email, else their GitHub noreply address (which needs
// both login and id), else the address they sign in with; one with none is
// left out, as is one whose address is the committing bot's or one already
// named (addresses compare ignoring case, as GitHub's do).
func Coauthors(people []CoauthorPerson, botEmail string) []Coauthor {
	seen := map[string]bool{strings.ToLower(botEmail): botEmail != ""}
	var out []Coauthor
	for _, p := range people {
		email := strings.TrimSpace(p.CommitEmail)
		if email == "" && p.GitHubLogin != "" && p.GitHubUserID != 0 {
			email = fmt.Sprintf("%d+%s@users.noreply.github.com", p.GitHubUserID, p.GitHubLogin)
		}
		if email == "" {
			email = strings.TrimSpace(p.Email)
		}
		name := trailerText(p.Name)
		if email == "" || seen[strings.ToLower(email)] || strings.ContainsAny(email, "<>\r\n") {
			continue
		}
		if name == "" {
			name = email
		}
		seen[strings.ToLower(email)] = true
		out = append(out, Coauthor{Name: name, Email: email})
	}
	return out
}

// trailerText is a name as a trailer can carry it: one line, no angle
// brackets (which would end it early).
func trailerText(s string) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '<', '>', '\r', '\n':
			return -1
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// taskCoauthors reads the people on the caller's own task now, in order:
// a Run reads only its own task (a session's agent has none).
func taskCoauthors(ctx context.Context, tx pgx.Tx, c Caller) ([]CoauthorPerson, error) {
	if c.TaskID == "" {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT p.name, COALESCE(p.commit_email::text, ''), COALESCE(p.github_login, ''),
			COALESCE(p.github_user_id, 0), COALESCE(p.email::text, '')
		FROM task_people tp JOIN people p ON p.id = tp.person_id
		WHERE tp.task_id = $1 AND p.removed_at IS NULL ORDER BY tp.position, tp.person_id`, c.TaskID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[CoauthorPerson])
}

// coauthors is GET /git/coauthors: the Co-authored-by trailers for a commit
// the calling Run makes now, one a line, as text — what the commit hook in
// the agent's image appends. Not a tool, and not in the ledger: it is read
// on every commit, and says nothing to a person.
func (s *Server) coauthors(w http.ResponseWriter, r *http.Request) {
	c, _ := r.Context().Value(callerKey{}).(Caller)
	var people []CoauthorPerson
	if err := s.DB.InOrg(r.Context(), c.Org, func(tx pgx.Tx) (err error) {
		people, err = taskCoauthors(r.Context(), tx, c)
		return err
	}); err != nil {
		s.Log.Error("agent tools: reading a task's co-authors failed", "run", c.RunID, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// The bot commits with a GitHub App: never its own co-author. Not known
	// (no forge, GitHub down) leaves nobody out on its account.
	bot := ""
	if s.Forges != nil && len(people) > 0 {
		if gh, err := s.Forges.For(r.Context(), c.Org); err == nil && gh != nil && gh.IsApp() {
			if id, err := gh.Bot(r.Context()); err == nil {
				bot = id.Email
			}
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	var b strings.Builder
	for _, co := range Coauthors(people, bot) {
		b.WriteString(co.Trailer() + "\n")
	}
	_, _ = w.Write([]byte(b.String()))
}
