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
		"DUDE_AGENT_IMAGE": "img:1", "DUDE_AGENT_EGRESS": "a.example, b.example", "DUDE_AGENT_NESTED_CONTAINERS": "true",
		"DUDE_ORCHESTRATOR_TOKEN": "service-token",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.ToolsURL != "http://10.0.0.5:3120/" || !c.ToolsService || c.DefaultImage != "img:1" || len(c.Egress) != 2 || !c.NestedContainers ||
		c.LLMURL != "https://llm.example/v1" || c.LLMKey != "sk-test" || string(c.ToolsKey) != "service-token" {
		t.Errorf("config = %+v", c)
	}
	c, err = LoadAgentConfig(settings(t, map[string]string{"DUDE_TOOLS_SERVICE": "off", "DUDE_TOOLS_KEY": "tools-key",
		"DUDE_ORCHESTRATOR_TOKEN": "service-token"}))
	if err != nil || c.ToolsService || string(c.ToolsKey) != "tools-key" || c.DefaultImage != "localhost/dude-runtime:dev" ||
		c.NestedContainers {
		t.Errorf("config = %+v, %v", c, err)
	}
}

func TestAgentConfigReadsTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dude.toml")
	if err := os.WriteFile(path, []byte(`
[llm]
url = "https://llm.example/v1"
key = "sk-file"
[orchestrator]
token = "file-service-token"
[agent]
image = "img:2"
timeout = "48h"
egress = ["a.example"]
[tools]
service = false
url = "http://10.0.0.7:3200/"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadAgentConfig(settings(t, map[string]string{"DUDE_CONFIG": path, "DUDE_AGENT_IMAGE": "img:env"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.LLMURL != "https://llm.example/v1" || c.LLMKey != "sk-file" || c.DefaultImage != "img:env" || c.Timeout != "48h" ||
		len(c.Egress) != 1 || c.Egress[0] != "a.example" || c.ToolsService || c.ToolsURL != "http://10.0.0.7:3200/" ||
		string(c.ToolsKey) != "file-service-token" {
		t.Errorf("config = %+v", c)
	}
}

// tools.key signs Runs' tool tokens; without one of its own it is the
// service token, from wherever that came.
func TestTheToolsKeyFallsBackToTheServiceToken(t *testing.T) {
	file := func(text string) string {
		path := filepath.Join(t.TempDir(), "dude.toml")
		if err := os.WriteFile(path, []byte("[orchestrator]\ntoken = \"file-token\"\n"+text), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for name, tc := range map[string]struct {
		text string
		vars map[string]string
		want string
	}{
		"absent":                    {text: "", want: "file-token"},
		"empty":                     {text: "[tools]\nkey = \"\"\n", want: "file-token"},
		"explicit in the file":      {text: "[tools]\nkey = \"file-tools-key\"\n", want: "file-tools-key"},
		"env over the file's key":   {text: "[tools]\nkey = \"file-tools-key\"\n", vars: map[string]string{"DUDE_TOOLS_KEY": "env-tools-key"}, want: "env-tools-key"},
		"env token, no tools key":   {text: "", vars: map[string]string{"DUDE_ORCHESTRATOR_TOKEN": "env-token"}, want: "env-token"},
		"env token, empty key":      {text: "[tools]\nkey = \"\"\n", vars: map[string]string{"DUDE_ORCHESTRATOR_TOKEN": "env-token"}, want: "env-token"},
		"env token, file tools key": {text: "[tools]\nkey = \"file-tools-key\"\n", vars: map[string]string{"DUDE_ORCHESTRATOR_TOKEN": "env-token"}, want: "file-tools-key"},
	} {
		vars := map[string]string{"DUDE_CONFIG": file(tc.text)}
		for k, v := range tc.vars {
			vars[k] = v
		}
		c, err := LoadAgentConfig(settings(t, vars))
		if err != nil || string(c.ToolsKey) != tc.want {
			t.Errorf("%s: tools key %q, %v; want %q", name, c.ToolsKey, err, tc.want)
		}
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
