package phases

import "testing"

// Each setting read from the environment reaches the config: a missing
// line here once left agents without dude's tools, silently.
func TestAgentConfigReadsTheEnvironment(t *testing.T) {
	t.Setenv("DUDE_OPENCODE_AUTH", "{}")
	t.Setenv("DUDE_OPENCODE_CONFIG", "{}")
	t.Setenv("DUDE_TOOLS_URL", "http://10.0.0.5:3120/")
	t.Setenv("DUDE_AGENT_IMAGE", "img:1")
	t.Setenv("DUDE_AGENT_TIMEOUT", "5m")
	t.Setenv("DUDE_AGENT_EGRESS", "a.example, b.example")
	c, err := LoadAgentConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.ToolsURL != "http://10.0.0.5:3120/" || c.DefaultImage != "img:1" || c.Timeout != "5m" || len(c.Egress) != 2 {
		t.Errorf("config = %+v", c)
	}
}
