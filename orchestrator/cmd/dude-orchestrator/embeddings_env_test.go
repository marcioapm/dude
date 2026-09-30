package main

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/config"
)

func TestEmbeddingsComeFromTheLLMUnlessOverriddenOrOff(t *testing.T) {
	llm := map[string]string{"DUDE_LLM_URL": "https://llm.example/v1", "DUDE_LLM_KEY": "sk-llm"}
	only := func(env map[string]string) func(string) string {
		return func(k string) string { return env[k] }
	}
	with := func(extra map[string]string) func(string) string {
		env := maps.Clone(llm)
		maps.Copy(env, extra)
		return only(env)
	}
	for name, tc := range map[string]struct {
		env       func(string) string
		url, key  string
		off       string // the reason, when embeddings are off
		fail      bool
		failNames []string // variables the error must name
	}{
		"fallback to the LLM": {env: with(nil), url: "https://llm.example/v1", key: "sk-llm"},
		"explicit override": {env: with(map[string]string{
			"DUDE_EMBEDDINGS_URL": "https://emb.example/v1", "DUDE_EMBEDDINGS_KEY": "sk-emb"}),
			url: "https://emb.example/v1", key: "sk-emb"},
		"override URL on another origin, no embeddings key": {
			env:  with(map[string]string{"DUDE_EMBEDDINGS_URL": "https://emb.example/v1"}),
			fail: true, failNames: []string{"DUDE_EMBEDDINGS_URL", "DUDE_EMBEDDINGS_KEY"}},
		"override URL on another port, no embeddings key": {
			env:  with(map[string]string{"DUDE_EMBEDDINGS_URL": "https://llm.example:8443/v1"}),
			fail: true, failNames: []string{"DUDE_EMBEDDINGS_URL", "DUDE_EMBEDDINGS_KEY"}},
		"override URL on another scheme, no embeddings key": {
			env:  with(map[string]string{"DUDE_EMBEDDINGS_URL": "http://llm.example/v1"}),
			fail: true, failNames: []string{"DUDE_EMBEDDINGS_URL", "DUDE_EMBEDDINGS_KEY"}},
		"override URL on the LLM's origin falls back to the LLM key": {
			env: with(map[string]string{"DUDE_EMBEDDINGS_URL": "https://LLM.Example:443/embed/v2"}),
			url: "https://LLM.Example:443/embed/v2", key: "sk-llm"},
		"only DUDE_LLM_URL": {env: only(map[string]string{"DUDE_LLM_URL": "https://llm.example/v1"}),
			off: "DUDE_LLM_URL has no key (DUDE_LLM_KEY)"},
		"inherited URL, embeddings key": {env: only(map[string]string{
			"DUDE_LLM_URL": "https://llm.example/v1", "DUDE_EMBEDDINGS_KEY": "sk-emb"}),
			url: "https://llm.example/v1", key: "sk-emb"},
		"off":                 {env: with(map[string]string{"DUDE_EMBEDDINGS_URL": "off"}), off: "DUDE_EMBEDDINGS_URL is off"},
		"nothing set":         {env: only(nil), off: "neither DUDE_EMBEDDINGS_URL nor DUDE_LLM_URL is set"},
		"a URL without a key": {env: only(map[string]string{"DUDE_EMBEDDINGS_URL": "https://emb.example/v1"}), fail: true},
		"not an http(s) URL":  {env: with(map[string]string{"DUDE_EMBEDDINGS_URL": "llm.example/v1"}), fail: true},
	} {
		t.Run(name, func(t *testing.T) {
			c, err := embeddingsFromEnv(tc.env)
			if tc.fail {
				if err == nil {
					t.Fatalf("got %+v, want an error", c)
				}
				for _, v := range tc.failNames {
					if !strings.Contains(err.Error(), v) {
						t.Errorf("error %q does not name %s", err, v)
					}
				}
				if len(tc.failNames) > 0 && (strings.Contains(err.Error(), "sk-llm") || strings.Contains(err.Error(), "example")) {
					t.Errorf("error %q carries a value", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.off != "" {
				if c.URL != "" || c.Key != "" || c.Off != tc.off {
					t.Errorf("got %+v, want embeddings off: %q", c, tc.off)
				}
				return
			}
			if c.URL != tc.url || c.Key != tc.key {
				t.Errorf("got url %q key %q, want %q %q", c.URL, c.Key, tc.url, tc.key)
			}
		})
	}
}

// The resolved configuration's getter feeds embeddingsFromEnv, so the file's
// [llm] and [embeddings] tables reach it and a variable overrides them.
func TestEmbeddingsComeFromTheConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dude.toml")
	if err := os.WriteFile(path, []byte("[llm]\nurl = \"https://llm.example/v1\"\nkey = \"sk-file\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{"DUDE_CONFIG": path}
	load := func() embeddingsConfig {
		cfg, err := config.Load(config.Orchestrator, config.Options{Getenv: func(k string) string { return vars[k] },
			DefaultPath: filepath.Join(t.TempDir(), "absent.toml")})
		if err != nil {
			t.Fatal(err)
		}
		c, err := embeddingsFromEnv(cfg.Getenv)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	if c := load(); c.URL != "https://llm.example/v1" || c.Key != "sk-file" {
		t.Errorf("got %+v, want the file's LLM", c)
	}
	vars["DUDE_EMBEDDINGS_URL"] = "off"
	if c := load(); c.URL != "" || c.Off != "DUDE_EMBEDDINGS_URL is off" {
		t.Errorf("got %+v, want the variable to turn them off", c)
	}
}
