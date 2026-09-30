package main

import (
	"maps"
	"strings"
	"testing"
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
	const llm = "[llm]\nurl = \"https://llm.example/v1\"\nkey = \"sk-file\"\n"
	for name, tc := range map[string]struct {
		text     string
		vars     map[string]string
		url, key string
		off      string
		fail     []string // what the error must name
	}{
		"the file's LLM": {text: llm, url: "https://llm.example/v1", key: "sk-file"},
		"explicit override": {text: llm + "[embeddings]\nurl = \"https://emb.example/v1\"\nkey = \"sk-emb-file\"\n",
			url: "https://emb.example/v1", key: "sk-emb-file"},
		"same origin inherits the LLM key": {text: llm + "[embeddings]\nurl = \"https://llm.example/embed\"\n",
			url: "https://llm.example/embed", key: "sk-file"},
		"cross origin without a key is refused": {text: llm + "[embeddings]\nurl = \"https://emb.example/v1\"\n",
			fail: []string{"DUDE_EMBEDDINGS_URL", "DUDE_EMBEDDINGS_KEY"}},
		"env key over the file's key": {text: llm + "[embeddings]\nurl = \"https://emb.example/v1\"\nkey = \"sk-emb-file\"\n",
			vars: map[string]string{"DUDE_EMBEDDINGS_KEY": "sk-emb-env"}, url: "https://emb.example/v1", key: "sk-emb-env"},
		"file off":              {text: llm + "[embeddings]\nurl = \"off\"\n", off: "DUDE_EMBEDDINGS_URL is off"},
		"env off over the file": {text: llm, vars: map[string]string{"DUDE_EMBEDDINGS_URL": "off"}, off: "DUDE_EMBEDDINGS_URL is off"},
		"env URL over file off": {text: llm + "[embeddings]\nurl = \"off\"\n",
			vars: map[string]string{"DUDE_EMBEDDINGS_URL": "https://llm.example/v2"}, url: "https://llm.example/v2", key: "sk-file"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := loadConfig(t, tc.text, 0o600, tc.vars)
			c, err := embeddingsFromEnv(cfg.Getenv)
			if tc.fail != nil {
				if err == nil {
					t.Fatalf("got %+v, want an error", c)
				}
				for _, v := range tc.fail {
					if !strings.Contains(err.Error(), v) {
						t.Errorf("error %q does not name %s", err, v)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.URL != tc.url || c.Key != tc.key || c.Off != tc.off {
				t.Errorf("got %+v, want url %q key %q off %q", c, tc.url, tc.key, tc.off)
			}
		})
	}
}
