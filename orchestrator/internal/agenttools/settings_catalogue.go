package agenttools

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/judge"
)

type modelOut struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Model       string `json:"model"`
	Effort      string `json:"effort"`
	Position    int    `json:"position"`
	Configured  bool   `json:"configured"`
}

type modelsOut struct {
	Count   int        `json:"count"`
	Section string     `json:"section"`
	Source  string     `json:"source"`
	Tiers   []modelOut `json:"tiers,omitempty"`
	Next    string     `json:"next,omitempty"`
}

type modelCursor struct {
	Org      string `json:"org"`
	Position int    `json:"position"`
	ID       string `json:"id"`
}

// catalogueLimit is an explicit catalogue section's page size.
func catalogueLimit(limit int) (int, error) {
	if limit == 0 {
		return 20, nil
	}
	if limit < 1 || limit > 50 {
		return 0, refuseText(settingsText("catalogue-limit", nil))
	}
	return limit, nil
}

// pageFit drops a page's trailing items, keeping at least one, until it
// encodes to 40 KiB at most: a tool result is held to 64 KiB, and JSON
// escaping can grow bounded text sixfold. It returns how many it kept.
func pageFit(page any, n int, trim func(int)) (int, error) {
	for ; n > 1; n-- {
		encoded, err := json.Marshal(page)
		if err != nil {
			return 0, err
		}
		if len(encoded) <= 40<<10 {
			return n, nil
		}
		trim(n - 1)
	}
	return n, nil
}

func modelCatalogue(w wrapper, tiers map[string]delivery.Tier, org string, in settingsIn) (*modelsOut, error) {
	out := &modelsOut{Count: len(tiers), Section: "models", Source: sourceOrg}
	if in.Section != "models" {
		return out, nil
	}
	limit, err := catalogueLimit(in.Limit)
	if err != nil {
		return nil, err
	}
	var after modelCursor
	if in.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(in.Cursor)
		if err != nil || json.Unmarshal(raw, &after) != nil || after.Org != org || after.ID == "" {
			return nil, refuseText(settingsText("catalogue-cursor", nil))
		}
	}
	ordered := make([]delivery.Tier, 0, len(tiers))
	for _, t := range tiers {
		ordered = append(ordered, t)
	}
	slices.SortFunc(ordered, func(a, b delivery.Tier) int {
		return cmp.Or(cmp.Compare(a.Position, b.Position), cmp.Compare(a.ID, b.ID))
	})
	for _, t := range ordered {
		if after.ID != "" && (t.Position < after.Position || t.Position == after.Position && t.ID <= after.ID) {
			continue
		}
		if len(out.Tiers) == limit {
			last := out.Tiers[len(out.Tiers)-1]
			raw, _ := json.Marshal(modelCursor{Org: org, Position: last.Position, ID: last.ID})
			out.Next = base64.RawURLEncoding.EncodeToString(raw)
			break
		}
		item := modelOut{ID: t.ID, Name: w.text(t.Name, 96), Description: w.text(t.Description, 320), Model: w.text(t.Model, 800), Effort: t.Effort, Position: t.Position, Configured: t.Model != ""}
		out.Tiers = append(out.Tiers, item)
		encoded, err := json.Marshal(out)
		if err != nil {
			return nil, err
		}
		if len(encoded) > 40<<10 && len(out.Tiers) > 1 {
			out.Tiers = out.Tiers[:len(out.Tiers)-1]
			last := out.Tiers[len(out.Tiers)-1]
			raw, _ := json.Marshal(modelCursor{Org: org, Position: last.Position, ID: last.ID})
			out.Next = base64.RawURLEncoding.EncodeToString(raw)
			break
		}
	}
	return out, nil
}

type judgementsOut struct {
	Enabled bool    `json:"enabled"`
	State   string  `json:"state"`
	TierID  *string `json:"tierId"`
	Tier    *refOut `json:"tier,omitempty"`
	Source  string  `json:"source"`
}

func readJudgements(ctx context.Context, tx pgx.Tx, w wrapper) (*judgementsOut, error) {
	out := &judgementsOut{Source: sourceOrg, State: "off"}
	if err := tx.QueryRow(ctx, `SELECT judgement_tier_id FROM organizations WHERE id = current_organization_id()`).Scan(&out.TierID); err != nil {
		return nil, err
	}
	if out.TierID == nil {
		return out, nil
	}
	t, err := judge.LoadTier(ctx, tx, *out.TierID)
	if err != nil && !errors.Is(err, judge.ErrNoModel) {
		return nil, err
	}
	out.Enabled = true
	out.State = "configured"
	if errors.Is(err, judge.ErrNoModel) {
		out.State = "model-unset"
	}
	model := w.text(t.Model, 200)
	out.Tier = &refOut{ID: t.ID, Name: w.text(t.Name, 24), Model: &model}
	return out, nil
}
