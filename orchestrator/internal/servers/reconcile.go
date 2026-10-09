package servers

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// reconcileServers makes a wakeable preview's lux servers what the
// project's recipes say now, before lux next starts them (createServers on
// declare, wakeClaimed on every wake):
//
//   - a server whose recipe's lux input (Recipe.Input) differs from what lux
//     has (GET /v1/servers by the dude.preview label) is PATCHed with the
//     fields that differ;
//   - an autostart recipe with no server gets one;
//   - a server whose recipe was removed, or no longer starts in previews,
//     is deleted. With none left the preview ends: there is nothing to open.
//
// No server is deleted to be recreated: lux patches every field Input sets
// (port, command, workdir, env); the name, the one field it cannot, is the
// server's identity here, so a renamed recipe is a delete and a create.
// Secrets are not part of a server: a resume or a submit sends them.
//
// done: the preview was failed or ended here, and the caller does nothing
// more with it.
func (p *Previews) reconcileServers(ctx context.Context, r wakeRun) (done bool, err error) {
	var recipes []Recipe
	var settings PreviewSettings
	// Every server dude recorded for the preview, gone ones too: one lux
	// lost (attachAll, the feed) is not made again here.
	var recorded []recordedServer
	of := PreviewOf{TaskID: r.TaskID, ProjectID: r.ProjectID}
	readAt := time.Now()
	if err := p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		var err error
		if recipes, err = LoadRecipes(ctx, tx, r.ProjectID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT p.key_prefix || '-' || t.number, p.slug FROM tasks t JOIN projects p ON p.id = t.project_id
			WHERE t.id = $1`, r.TaskID).Scan(&of.TaskKey, &of.ProjectSlug); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT name, lux_server_id, deleted_at IS NULL FROM preview_servers WHERE run_id = $1`, r.ID)
		if err != nil {
			return err
		}
		if recorded, err = pgx.CollectRows(rows, pgx.RowToStructByPos[recordedServer]); err != nil {
			return err
		}
		return loadSettings(ctx, tx, r.ProjectID, &settings)
	}); err != nil {
		return false, err
	}
	// What lux has of them, by id: what each recipe is compared with. A
	// first declare has nothing to compare (createServer looks for its own).
	var inLux []lux.TenantServer
	if r.Status != "pending" || slices.ContainsFunc(recorded, func(s recordedServer) bool { return s.Live }) {
		var err error
		if inLux, err = p.Lux.ListServers(ctx, "", "dude.preview="+r.ID); err != nil {
			return false, err
		}
	}
	byID := map[string]lux.TenantServer{}
	for _, ts := range inLux {
		byID[ts.ID] = ts
	}
	primary, err := p.primaryRepoName(ctx, r)
	if err != nil {
		return false, err
	}
	wanted := map[string]bool{}
	for _, rc := range recipes {
		wanted[rc.Name] = rc.Autostart
	}
	kept, dropped := 0, 0
	for _, sv := range recorded {
		switch {
		case !sv.Live:
		case !wanted[sv.Name]:
			if err := p.dropServer(ctx, r, sv.LuxID); err != nil {
				return false, err
			}
			dropped++
		default:
			kept++
		}
	}
	// The servers lux has for the preview that dude had no row of and no
	// recipe wants: a delete in dropServer that did not reach lux. One
	// dropped above is in recorded, so it is not deleted twice; one a
	// recipe wants is adopted instead (createServer); one created after
	// recorded was read may be another orchestrator's, recorded since.
	for _, ts := range byID {
		if wanted[ts.Name] || ts.CreatedAt.After(readAt) ||
			slices.ContainsFunc(recorded, func(s recordedServer) bool { return s.LuxID == ts.ID }) {
			continue
		}
		if err := p.Lux.DeleteServer(ctx, ts.ID); err != nil && !lux.IsNotFound(err) {
			return false, err
		}
	}
	for _, rc := range recipes {
		if !rc.Autostart {
			continue
		}
		in, err := rc.Input(primary)
		if err != nil {
			p.Log.Warn("a preview server lux would refuse was left out", "run", r.ID, "server", rc.Name, "error", err)
			continue
		}
		i := slices.IndexFunc(recorded, func(s recordedServer) bool { return s.Name == rc.Name })
		switch {
		case i >= 0 && recorded[i].Live:
			// One lux no longer has is left alone: its server.deleted
			// (the feed) ends the preview, and attachAll records it gone.
			if cur, ok := byID[recorded[i].LuxID]; ok {
				err = p.updateServer(ctx, r, cur, in)
			}
		case i < 0:
			if err = p.addServer(ctx, r, of, in, settings); err == nil {
				kept++
			}
		}
		// A refusal no retry changes fails the preview, as lux's answer
		// to a submit does.
		if le, ok := lux.AsError(err); ok && !le.Retryable() {
			return true, p.fail(ctx, r.previewRun, fmt.Sprintf("lux refused preview server %s: %s", rc.Name, le.Message))
		}
		if err != nil {
			return false, err
		}
	}
	if dropped > 0 && kept == 0 {
		// Its last server dropped: nothing is left to open, as when lux
		// deletes one (Feed.Apply).
		return true, p.complete(ctx, r, "no server starts in previews", ledger.ActorSystem, r.ID)
	}
	return false, nil
}

// recordedServer is a preview_servers row; Live: not marked deleted.
type recordedServer struct {
	Name, LuxID string
	Live        bool
}

// updateServer PATCHes the fields in which a preview server's recipe
// differs from what lux has (cur); nothing when none does. A server lux no
// longer has is left to the feed; a 409 (attached or detached meanwhile) is
// returned as an error that is not lux's refusal, so the wake is tried
// again.
func (p *Previews) updateServer(ctx context.Context, r wakeRun, cur lux.TenantServer, in lux.ServerInput) error {
	var patch lux.PatchServer
	var changed []string
	if cur.Port != in.Port {
		patch.Port = &in.Port
		changed = append(changed, "port")
	}
	if !slices.Equal(cur.Command, in.Command) {
		command := in.Command
		if command == nil {
			command = []string{} // lux: [] removes it
		}
		patch.Command = &command
		changed = append(changed, "command")
	}
	if cur.Workdir != in.Workdir {
		patch.Workdir = &in.Workdir
		changed = append(changed, "workdir")
	}
	// nil and empty are the same env: lux shows none as {}.
	if !maps.Equal(cur.Env, in.Env) {
		env := in.Env
		if env == nil {
			env = map[string]string{}
		}
		patch.Env = &env
		changed = append(changed, "env")
	}
	if len(changed) == 0 {
		return nil
	}
	// Names only: env values may be credentials.
	p.Log.Info("a preview server's recipe changed; updating it in lux", "run", r.ID, "server", in.Name, "fields", changed)
	_, err := p.Lux.PatchServer(ctx, cur.ID, patch)
	if le, ok := lux.AsError(err); ok {
		switch le.Status {
		case http.StatusNotFound:
			return nil
		case http.StatusConflict:
			return fmt.Errorf("patching lux server %s: %v", cur.ID, le)
		}
	}
	return err
}

// dropServer deletes a preview server whose recipe is gone. Its row goes
// first, so the server.deleted lux then reports is of no preview's server
// and ends nothing (Feed.Apply); a delete in lux that fails leaves an
// orphan the next reconcile deletes, or endInLux when the preview ends.
func (p *Previews) dropServer(ctx context.Context, r wakeRun, id string) error {
	p.Log.Info("a preview server's recipe was removed or no longer starts in previews; deleting it", "run", r.ID, "server", id)
	if err := p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM preview_servers WHERE run_id = $1 AND lux_server_id = $2`, r.ID, id)
		return err
	}); err != nil {
		return err
	}
	if err := p.Lux.DeleteServer(ctx, id); err != nil && !lux.IsNotFound(err) {
		return err
	}
	return nil
}

// addServer creates one preview server in lux and records it. Each is
// looked for among the preview's servers in lux first (createServer), so a
// create whose answer was lost is not made twice; a hostname another
// preview holds (409 hostname_taken) is chosen again once, salted with
// this preview's id.
func (p *Previews) addServer(ctx context.Context, r wakeRun, of PreviewOf, in lux.ServerInput, settings PreviewSettings) error {
	sv, err := p.createServer(ctx, r, of, in, settings)
	if err != nil {
		return err
	}
	var kept string
	if err := p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO preview_servers (run_id, organization_id, name, lux_server_id, hostname, url)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (run_id, name) DO UPDATE SET name = preview_servers.name
			RETURNING lux_server_id`,
			r.ID, r.Org, in.Name, sv.ID, deref(sv.Hostname), sv.URL).Scan(&kept)
	}); err != nil {
		return err
	}
	if kept != sv.ID {
		// Another orchestrator recorded its own server for this one
		// first: the one made here is an orphan, deleted.
		if err := p.Lux.DeleteServer(ctx, sv.ID); err != nil && !lux.IsNotFound(err) {
			return err
		}
		return nil
	}
	// An adopted server (a create whose answer was lost, or a leftover a
	// recipe wants again) may be of an older recipe.
	return p.updateServer(ctx, r, sv, in)
}
