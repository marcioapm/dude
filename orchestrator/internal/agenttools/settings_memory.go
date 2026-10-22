package agenttools

import (
	"context"
	"encoding/json"
	"strings"
)

type memorySettingsOut struct {
	Source         string `json:"source"`
	MemberEditable bool   `json:"memberEditable"`
	EmbeddingState string `json:"embeddingState"`
	Model          string `json:"model,omitempty"`
	Dimensions     int    `json:"dimensions,omitempty"`
	DynamicStatus  string `json:"dynamicStatus"`
	Note           string `json:"note"`
}

// The shared embedder's getters are read before the settings transaction,
// never Embed. Model is the provider's as given; readSettings quotes it.
func prepareSettings(ctx context.Context, c Caller, args json.RawMessage) any {
	var in settingsIn
	_ = json.Unmarshal(args, &in)
	if s := strings.TrimSpace(in.Section); s != "" && s != "memory" {
		return nil
	}
	out := &memorySettingsOut{Source: "operator", EmbeddingState: "not-configured", DynamicStatus: "unavailable", Note: settingsText("memory-unavailable", nil)}
	if c.env.embedder != nil {
		out.EmbeddingState = "configured"
		out.Model = c.env.embedder.Model()
		out.Dimensions = c.env.embedder.Dimensions()
	}
	return out
}
