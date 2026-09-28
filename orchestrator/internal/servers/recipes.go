// Package servers is what a Run serves: a project's server recipes turned
// into lux servers on a task's Run, and branch previews — Runs with no
// agent, serving a task's branch.
//
// lux owns servers: their records, processes, health, URLs, and what
// happens to them when a Run moves (they stop). dude owns the recipes (what
// a project can serve and how to start it), which Run of a task a person
// sees, and the preview Runs it starts, parks and resumes. A server's state
// is always read from lux, never kept here: lux reports each change as a
// server.* event on the Run's stream, which dude turns into a
// servers.changed event so a watching browser reads again.
package servers

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/phases"
)

// Recipe is one of a project's servers, as a person defined it.
type Recipe struct {
	Name    string `json:"name"`
	Port    int    `json:"port"`
	Command string `json:"command"`
	// Relative to the repository's checkout; "" is its root.
	Workdir string `json:"workdir"`
	// Run before the command on every start; nil for none.
	Setup     *string  `json:"setup"`
	Env       []EnvVar `json:"env"`
	Autostart bool     `json:"autostartInPreviews"`
}

type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// NamePattern is lux's rule for a server's name: it is part of its URL
// (<name>-<run>.<domain>), a DNS label, so it starts with a letter, does not
// end with '-', and is short enough that the label stays under 63.
var NamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,29}$`)

// ValidName says whether lux would take name for a server.
func ValidName(name string) bool {
	return NamePattern.MatchString(name) && !strings.HasSuffix(name, "-")
}

// ShellCommand is how a recipe's command runs: through a shell, after its
// setup if it has one, exec'd so that the server is the process lux starts
// and stops.
func ShellCommand(setup *string, command string) []string {
	line := "exec " + command
	if setup != nil && strings.TrimSpace(*setup) != "" {
		line = *setup + " && " + line
	}
	return []string{"sh", "-c", line}
}

// Workdir is where a server's command runs: its workdir under the checkout
// of repo (dude's layout, phases.RepoPath), or the workspace with no
// repository.
func Workdir(repo, workdir string) string {
	base := "/workspace"
	if repo != "" {
		base = phases.RepoPath(repo)
	}
	return path.Join(base, workdir)
}

// Input is the recipe as a lux server of a Run whose checkout of the
// project's code is repo.
func (r Recipe) Input(repo string) lux.ServerInput {
	in := lux.ServerInput{Name: r.Name, Port: r.Port, Command: ShellCommand(r.Setup, r.Command), Workdir: Workdir(repo, r.Workdir)}
	if len(r.Env) > 0 {
		in.Env = map[string]string{}
		for _, e := range r.Env {
			in.Env[e.Name] = e.Value
		}
	}
	return in
}

// LoadRecipes reads a project's recipes, by name.
func LoadRecipes(ctx context.Context, tx pgx.Tx, projectID string) ([]Recipe, error) {
	rows, err := tx.Query(ctx, `SELECT name, port, command, workdir, setup, env, autostart_in_previews
		FROM project_servers WHERE project_id = $1 ORDER BY name`, projectID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Recipe, error) {
		var r Recipe
		var env []byte
		if err := row.Scan(&r.Name, &r.Port, &r.Command, &r.Workdir, &r.Setup, &env, &r.Autostart); err != nil {
			return r, err
		}
		return r, json.Unmarshal(env, &r.Env)
	})
}

// recipesJSON reads a project's recipes as the API shows them
// (server_recipe, migration 054).
func recipesJSON(ctx context.Context, tx pgx.Tx, projectID string) (json.RawMessage, error) {
	var out json.RawMessage
	err := tx.QueryRow(ctx, `SELECT COALESCE(json_agg(server_recipe(s) ORDER BY s.name), '[]')
		FROM project_servers s WHERE s.project_id = $1`, projectID).Scan(&out)
	return out, err
}

// PreviewSettings is how a project's branch previews run (preview_settings,
// migration 054, fills in the defaults).
type PreviewSettings struct {
	// nil: the project's runtime image.
	Image              *string  `json:"image"`
	Egress             []string `json:"egress"`
	IdleTimeoutMinutes float64  `json:"idleTimeoutMinutes"`
}

// primaryRepo is the repository a Run's servers run in: the first the Run
// checked out (a task's repositories are in name order), else the
// project's first. "" for work on no repository.
func primaryRepo(ctx context.Context, tx pgx.Tx, runRepos []string, projectID string) (string, error) {
	if len(runRepos) > 0 {
		return runRepos[0], nil
	}
	var name string
	err := tx.QueryRow(ctx, `SELECT COALESCE(min(name), '') FROM repositories WHERE project_id = $1`, projectID).Scan(&name)
	return name, err
}

// ManualServer is a server a person adds that is not one of the project's
// recipes: a name and a port, and optionally a command to start it.
type ManualServer struct {
	Name    string          `json:"name"`
	Port    int             `json:"port"`
	Command json.RawMessage `json:"command"`
	Workdir string          `json:"workdir"`
	Env     json.RawMessage `json:"env"`
}

// Input turns what a person typed into a lux server: a command given as a
// string runs through a shell, as a recipe's does; as a list, as it is. Env
// is a map or a list of {name, value}.
func (m ManualServer) Input(repo string) (lux.ServerInput, error) {
	in := lux.ServerInput{Name: m.Name, Port: m.Port}
	if len(m.Command) > 0 && string(m.Command) != "null" {
		var line string
		if json.Unmarshal(m.Command, &line) == nil {
			if strings.TrimSpace(line) != "" {
				in.Command = ShellCommand(nil, line)
			}
		} else if err := json.Unmarshal(m.Command, &in.Command); err != nil {
			return in, fmt.Errorf("command must be a string or a list of strings")
		}
	}
	if m.Workdir != "" || in.Command != nil {
		in.Workdir = Workdir(repo, m.Workdir)
	}
	if len(m.Env) > 0 && string(m.Env) != "null" {
		if json.Unmarshal(m.Env, &in.Env) != nil {
			var list []EnvVar
			if err := json.Unmarshal(m.Env, &list); err != nil {
				return in, fmt.Errorf("env must be a map or a list of {name, value}")
			}
			in.Env = map[string]string{}
			for _, e := range list {
				in.Env[e.Name] = e.Value
			}
		}
	}
	return in, nil
}

// ValidWorkdir: a workdir is inside the checkout — relative, never climbing
// out of it.
func ValidWorkdir(w string) bool {
	if w == "" {
		return true
	}
	if path.IsAbs(w) {
		return false
	}
	for _, seg := range strings.Split(path.Clean(w), "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}
