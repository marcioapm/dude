package phases

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/config"
)

// settings resolves the orchestrator's settings from vars alone, with no
// file at the default path.
func settings(t *testing.T, vars map[string]string) *config.Config {
	t.Helper()
	c, err := config.Load(config.Orchestrator, config.Options{
		Getenv:      func(k string) string { return vars[k] },
		DefaultPath: filepath.Join(t.TempDir(), "absent.toml"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Each setting reaches the agent config: a missing line here once left
// agents without dude's tools, silently.
func TestAgentConfigReadsTheEnvironment(t *testing.T) {
	c, err := LoadAgentConfig(settings(t, map[string]string{
		"DUDE_LLM_URL": "https://llm.example/v1", "DUDE_LLM_KEY": "sk-test",
		"DUDE_TOOLS_URL": "http://10.0.0.5:3120/", "DUDE_TOOLS_SERVICE": "",
		"DUDE_AGENT_IMAGE": "img:1", "DUDE_AGENT_EGRESS": "a.example, b.example",
		"DUDE_ORCHESTRATOR_TOKEN": "service-token",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.ToolsURL != "http://10.0.0.5:3120/" || !c.ToolsService || c.DefaultImage != "img:1" || len(c.Egress) != 2 ||
		c.LLMURL != "https://llm.example/v1" || c.LLMKey != "sk-test" || string(c.ToolsKey) != "service-token" {
		t.Errorf("config = %+v", c)
	}
	c, err = LoadAgentConfig(settings(t, map[string]string{"DUDE_TOOLS_SERVICE": "off", "DUDE_TOOLS_KEY": "tools-key",
		"DUDE_ORCHESTRATOR_TOKEN": "service-token"}))
	if err != nil || c.ToolsService || string(c.ToolsKey) != "tools-key" || c.DefaultImage != "localhost/dude-runtime:dev" {
		t.Errorf("config = %+v, %v", c, err)
	}
}

func TestAgentConfigReadsTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dude.toml")
	if err := os.WriteFile(path, []byte(`
[llm]
url = "https://llm.example/v1"
[agent]
image = "img:2"
timeout = "48h"
egress = ["a.example"]
[tools]
service = false
`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadAgentConfig(settings(t, map[string]string{"DUDE_CONFIG": path, "DUDE_AGENT_IMAGE": "img:env"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.LLMURL != "https://llm.example/v1" || c.DefaultImage != "img:env" || c.Timeout != "48h" ||
		len(c.Egress) != 1 || c.Egress[0] != "a.example" || c.ToolsService {
		t.Errorf("config = %+v", c)
	}
}

func TestAnLLMURLThatIsNotHTTPIsRefusedAtStartup(t *testing.T) {
	for _, bad := range []string{"llm.example/v1", "ftp://llm.example/v1", "https://user:pw@llm.example/v1", "https:///v1"} {
		if _, err := LoadAgentConfig(settings(t, map[string]string{"DUDE_LLM_URL": bad})); err == nil ||
			!strings.Contains(err.Error(), "DUDE_LLM_URL") || !strings.Contains(err.Error(), "llm.url") {
			t.Errorf("%q: err = %v, want llm.url (DUDE_LLM_URL) refused", bad, err)
		}
	}
	if _, err := LoadAgentConfig(settings(t, nil)); err != nil {
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
