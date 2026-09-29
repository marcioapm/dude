package main

import "testing"

func TestEmbeddingsComeFromTheLLMUnlessOverriddenOrOff(t *testing.T) {
	llm := map[string]string{"DUDE_LLM_URL": "https://llm.example/v1", "DUDE_LLM_KEY": "sk-llm"}
	with := func(extra map[string]string) func(string) string {
		env := map[string]string{}
		for k, v := range llm {
			env[k] = v
		}
		for k, v := range extra {
			env[k] = v
		}
		return func(k string) string { return env[k] }
	}
	for name, tc := range map[string]struct {
		env       func(string) string
		url, key  string
		off, fail bool
	}{
		"fallback to the LLM": {env: with(nil), url: "https://llm.example/v1", key: "sk-llm"},
		"explicit override": {env: with(map[string]string{
			"DUDE_EMBEDDINGS_URL": "https://emb.example/v1", "DUDE_EMBEDDINGS_KEY": "sk-emb"}),
			url: "https://emb.example/v1", key: "sk-emb"},
		"override URL, LLM key": {env: with(map[string]string{"DUDE_EMBEDDINGS_URL": "https://emb.example/v1"}),
			url: "https://emb.example/v1", key: "sk-llm"},
		"off":                 {env: with(map[string]string{"DUDE_EMBEDDINGS_URL": "off"}), off: true},
		"nothing set":         {env: func(string) string { return "" }, off: true},
		"a URL without a key": {env: func(k string) string { return map[string]string{"DUDE_EMBEDDINGS_URL": "https://emb.example/v1"}[k] }, fail: true},
		"not an http(s) URL":  {env: with(map[string]string{"DUDE_EMBEDDINGS_URL": "llm.example/v1"}), fail: true},
	} {
		t.Run(name, func(t *testing.T) {
			c, err := embeddingsFromEnv(tc.env)
			if tc.fail {
				if err == nil {
					t.Fatalf("got %+v, want an error", c)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.off {
				if c.URL != "" || c.Off == "" {
					t.Errorf("got %+v, want embeddings off with a reason", c)
				}
				return
			}
			if c.URL != tc.url || c.Key != tc.key {
				t.Errorf("got url %q key %q, want %q %q", c.URL, c.Key, tc.url, tc.key)
			}
		})
	}
}
