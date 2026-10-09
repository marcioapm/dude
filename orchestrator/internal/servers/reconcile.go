package servers

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"slices"

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
	// Every server dude recorded for the preview, by name, gone ones too:
	// one lux lost (attachAll, the feed) is not made again here.
	recorded := map[string]bool{}
	of := PreviewOf{TaskID: r.TaskID, ProjectID: r.ProjectID}
	if err := p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		var err error
		if recipes, err = LoadRecipes(ctx, tx, r.ProjectID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT p.key_prefix || '-' || t.number, p.slug FROM tasks t JOIN projects p ON p.id = t.project_id
			WHERE t.id = $1`, r.TaskID).Scan(&of.TaskKey, &of.ProjectSlug); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT name FROM preview_servers WHERE run_id = $1`, r.ID)
		if err != nil {
			return err
		}
		names, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, n := range names {
			recorded[n] = true
		}
		return loadSettings(ctx, tx, r.ProjectID, &settings)
	}); err != nil {
		return false, err
	}
	have, err := p.previewServers(ctx, r.Org, r.ID)
	if err != nil {
		return false, err
	}
	// What lux has of them, by id: what each recipe is compared with. A
	// first declare has nothing to compare (createServer looks for its own).
	var inLux []lux.TenantServer
	if len(have) > 0 || r.Status != "pending" {
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
	live, dropped := 0, 0
	for _, sv := range have {
		if !wanted[sv.Name] {
			if err := p.dropServer(ctx, r, sv.LuxID); err != nil {
				return false, err
			}
			dropped++
			continue
		}
		live++
	}
	if err := p.dropOrphans(ctx, r, inLux, wanted); err != nil {
		return false, err
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
		i := slices.IndexFunc(have, func(s previewServer) bool { return s.Name == rc.Name })
		le, refused := (*lux.Error)(nil), false
		switch {
		case i >= 0:
			// One lux no longer has is left alone: its server.deleted
			// (the feed) ends the preview, and attachAll records it gone.
			if cur, ok := byID[have[i].LuxID]; ok {
				err = p.updateServer(ctx, r, cur, in)
			}
			// 422 is a recipe lux will not take; a 409 (the server
			// attached or detached meanwhile) is tried again.
			le, refused = lux.AsError(err)
			refused = refused && le.Status == http.StatusUnprocessableEntity
		case !recorded[rc.Name]:
			if err = p.addServer(ctx, r, of, in, settings); err == nil {
				live++
			}
			le, refused = lux.AsError(err)
			refused = refused && !le.Retryable()
		}
		if refused {
			return true, p.fail(ctx, r.previewRun, fmt.Sprintf("lux refused preview server %s: %s", rc.Name, le.Message))
		}
		if err != nil {
			return false, err
		}
	}
	if dropped > 0 && live == 0 {
		// Its last server dropped: nothing is left to open, as when lux
		// deletes one (Feed.Apply).
		return true, p.complete(ctx, r, "no server starts in previews", ledger.ActorSystem, r.ID)
	}
	return false, nil
}

// updateServer PATCHes the fields in which a preview server's recipe
// differs from what lux has (cur); nothing when none does.
func (p *Previews) updateServer(ctx context.Context, r wakeRun, cur lux.TenantServer, in lux.ServerInput) error {
	patch, changed := serverPatch(cur, in)
	if len(changed) == 0 {
		return nil
	}
	// Names only: env values may be credentials.
	p.Log.Info("a preview server's recipe changed; updating it in lux", "run", r.ID, "server", in.Name, "fields", changed)
	_, err := p.Lux.PatchServer(ctx, cur.ID, patch)
	if lux.IsNotFound(err) {
		return nil
	}
	return err
}

// serverPatch is what lux needs to be sent to make cur serve in: only the
// fields that differ, and their names.
func serverPatch(cur lux.TenantServer, in lux.ServerInput) (lux.PatchServer, []string) {
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
	return patch, changed
}

// dropServer deletes a preview server whose recipe is gone. Its row goes
// first, so the server.deleted lux then reports is of no preview's server
// and ends nothing (Feed.Apply); a delete in lux that fails leaves an
// orphan dropOrphans deletes on the next wake.
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

// dropOrphans deletes the servers lux has for the preview (its dude.preview
// label) that dude has no row of and no recipe wants: those whose delete
// in dropServer did not reach lux. One a recipe wants is adopted instead
// (createServer).
func (p *Previews) dropOrphans(ctx context.Context, r wakeRun, inLux []lux.TenantServer, wanted map[string]bool) error {
	var ids []string
	if err := p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT lux_server_id FROM preview_servers WHERE run_id = $1`, r.ID)
		if err != nil {
			return err
		}
		ids, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	}); err != nil {
		return err
	}
	for _, sv := range inLux {
		if wanted[sv.Name] || slices.Contains(ids, sv.ID) {
			continue
		}
		if err := p.Lux.DeleteServer(ctx, sv.ID); err != nil && !lux.IsNotFound(err) {
			return err
		}
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
	// An adopted server (a create whose answer was lost, or one dropOrphans
	// spared) may be of an older recipe.
	if patch, changed := serverPatch(sv, in); len(changed) > 0 {
		p.Log.Info("an adopted preview server is of an older recipe; updating it in lux", "run", r.ID, "server", in.Name, "fields", changed)
		if _, err := p.Lux.PatchServer(ctx, sv.ID, patch); err != nil && !lux.IsNotFound(err) {
			return err
		}
	}
	return nil
}
