package agenttools

import (
	"context"
	"encoding/json"
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

// The shared embedder's getters are read before the settings transaction, never Embed.
func prepareSettings(ctx context.Context, c Caller, args json.RawMessage) any {
	var in settingsIn
	_ = json.Unmarshal(args, &in)
	if in.Section != "" && in.Section != "memory" {
		return nil
	}
	out := &memorySettingsOut{Source: "operator", EmbeddingState: "not-configured", DynamicStatus: "unavailable", Note: settingsText("memory-unavailable", nil)}
	if c.env.embedder != nil {
		out.EmbeddingState = "configured"
		out.Model = newWrapper().text(c.env.embedder.Model(), 800)
		out.Dimensions = c.env.embedder.Dimensions()
	}
	return out
}
