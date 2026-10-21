package agenttools

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

type serversOut struct {
	Source   string              `json:"source"`
	Count    int                 `json:"count"`
	Section  string              `json:"section"`
	Recipes  []recipeOut         `json:"recipes,omitempty"`
	Previews map[string]valueOut `json:"previews,omitempty"`
}

type recipeOut struct {
	Name      string   `json:"name"`
	Command   string   `json:"command"`
	Setup     *string  `json:"setup"`
	Workdir   string   `json:"workdir"`
	Port      int      `json:"port"`
	Autostart bool     `json:"autostartInPreviews"`
	EnvNames  []string `json:"envNames"`
}

func projectServersOut(ctx context.Context, tx pgx.Tx, w wrapper, projects []projectRow, detailed bool) (map[string]*serversOut, error) {
	ids := make([]string, len(projects))
	for i, p := range projects {
		ids[i] = p.ID
	}
	// server_recipe and preview_settings supply the page's persisted fields and defaults.
	rows, err := tx.Query(ctx, `SELECT p.id, preview_settings(p), p.preview_settings,
   COALESCE((SELECT json_agg(server_recipe(s) ORDER BY s.name) FROM project_servers s WHERE s.project_id=p.id),'[]')
   FROM projects p WHERE p.id=ANY($1) ORDER BY p.id`, ids)
	if err != nil {
		return nil, err
	}
	out := map[string]*serversOut{}
	defer rows.Close()
	for rows.Next() {
		var id string
		var previews, stored map[string]any
		var raw json.RawMessage
		if err := rows.Scan(&id, &previews, &stored, &raw); err != nil {
			return nil, err
		}
		var recipes []struct {
			Name      string
			Command   string
			Setup     *string
			Workdir   string
			Port      int
			Autostart bool                    `json:"autostartInPreviews"`
			Env       []struct{ Name string } `json:"env"`
		}
		if err := json.Unmarshal(raw, &recipes); err != nil {
			return nil, err
		}
		s := &serversOut{Source: sourceProject, Count: len(recipes), Section: "servers"}
		if detailed {
			s.Previews = map[string]valueOut{}
			for k, v := range previews {
				src := sourceDude
				if _, ok := stored[k]; ok {
					src = sourceProject
				}
				if k == "imageId" && v != nil {
					src = sourceProject
				}
				switch x := v.(type) {
				case string:
					v = w.text(x, 500)
				case []any:
					values := make([]string, 0, len(x))
					for _, host := range x {
						if h, ok := host.(string); ok {
							values = append(values, w.text(h, 255))
						}
					}
					v = values
				}
				s.Previews[k] = valueOut{Value: v, Source: src}
			}
			for _, r := range recipes {
				item := recipeOut{Name: w.text(r.Name, 120), Command: w.text(r.Command, 1000), Workdir: w.text(r.Workdir, 500), Port: r.Port, Autostart: r.Autostart, EnvNames: []string{}}
				if r.Setup != nil {
					setup := w.text(*r.Setup, 1000)
					item.Setup = &setup
				}
				for _, e := range r.Env {
					item.EnvNames = append(item.EnvNames, w.text(e.Name, 200))
				}
				s.Recipes = append(s.Recipes, item)
			}
		}
		out[id] = s
	}
	return out, rows.Err()
}
