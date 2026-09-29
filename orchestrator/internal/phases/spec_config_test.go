package phases

import (
	"strings"
	"testing"
)

// Each setting read from the environment reaches the config: a missing
// line here once left agents without dude's tools, silently.
func TestAgentConfigReadsTheEnvironment(t *testing.T) {
	t.Setenv("DUDE_LLM_URL", "https://llm.example/v1")
	t.Setenv("DUDE_LLM_KEY", "sk-test")
	t.Setenv("DUDE_TOOLS_URL", "http://10.0.0.5:3120/")
	t.Setenv("DUDE_TOOLS_SERVICE", "")
	t.Setenv("DUDE_AGENT_IMAGE", "img:1")
	t.Setenv("DUDE_AGENT_EGRESS", "a.example, b.example")
	c, err := LoadAgentConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.ToolsURL != "http://10.0.0.5:3120/" || !c.ToolsService || c.DefaultImage != "img:1" || len(c.Egress) != 2 ||
		c.LLMURL != "https://llm.example/v1" || c.LLMKey != "sk-test" {
		t.Errorf("config = %+v", c)
	}
}

func TestAnLLMURLThatIsNotHTTPIsRefusedAtStartup(t *testing.T) {
	for _, bad := range []string{"llm.example/v1", "ftp://llm.example/v1", "https://user:pw@llm.example/v1", "https:///v1"} {
		t.Setenv("DUDE_LLM_URL", bad)
		if _, err := LoadAgentConfig(); err == nil || !strings.Contains(err.Error(), "DUDE_LLM_URL") {
			t.Errorf("%q: err = %v, want DUDE_LLM_URL refused", bad, err)
		}
	}
	t.Setenv("DUDE_LLM_URL", "")
	if _, err := LoadAgentConfig(); err != nil {
		t.Errorf("unset: %v", err)
	}
}

func TestARefusedURLsErrorDoesNotRepeatItsPassword(t *testing.T) {
	for _, bad := range []string{"ftp://user:pw@host", "https://user:pw@ho st/v1", "https://user:pw@llm.example/v1"} {
		err := ValidateHTTPURL(bad)
		if err == nil {
			t.Errorf("%q accepted", bad)
		} else if strings.Contains(err.Error(), "pw") {
			t.Errorf("%q: error %q repeats the password", bad, err)
		}
	}
}
