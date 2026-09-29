package main

import (
	"errors"
	"net/url"
	"strings"

	"github.com/marciomartins/dude/orchestrator/internal/phases"
)

// embeddingsConfig is where memory's embeddings come from, and which
// variable supplied each part, for the startup log. URL "" means none.
type embeddingsConfig struct {
	URL, Key         string
	URLFrom, KeyFrom string
	// Why there are none, when URL is "".
	Off string
}

// embeddingsFromEnv reads DUDE_EMBEDDINGS_URL and _KEY, each defaulting to
// the agents' DUDE_LLM_URL and _KEY, so one URL and key serve both.
// DUDE_EMBEDDINGS_URL=off turns embeddings off whatever DUDE_LLM_URL is.
// DUDE_LLM_KEY is only ever sent to DUDE_LLM_URL's origin: an explicit
// DUDE_EMBEDDINGS_URL elsewhere needs its own DUDE_EMBEDDINGS_KEY.
func embeddingsFromEnv(getenv func(string) string) (embeddingsConfig, error) {
	var c embeddingsConfig
	llmURL := getenv("DUDE_LLM_URL")
	switch u := getenv("DUDE_EMBEDDINGS_URL"); {
	case u == "off":
		c.Off = "DUDE_EMBEDDINGS_URL is off"
		return c, nil
	case u != "":
		c.URL, c.URLFrom = u, "DUDE_EMBEDDINGS_URL"
	case llmURL != "":
		c.URL, c.URLFrom = llmURL, "DUDE_LLM_URL"
	default:
		c.Off = "neither DUDE_EMBEDDINGS_URL nor DUDE_LLM_URL is set"
		return c, nil
	}
	if err := phases.ValidateHTTPURL(c.URL); err != nil {
		return embeddingsConfig{}, errors.New(c.URLFrom + " for embeddings: " + err.Error())
	}
	inherited := c.URLFrom == "DUDE_LLM_URL"
	llmKey := getenv("DUDE_LLM_KEY")
	switch key := getenv("DUDE_EMBEDDINGS_KEY"); {
	case key != "":
		c.Key, c.KeyFrom = key, "DUDE_EMBEDDINGS_KEY"
	case llmKey != "" && (inherited || sameOrigin(c.URL, llmURL)):
		c.Key, c.KeyFrom = llmKey, "DUDE_LLM_KEY"
	case llmKey != "":
		return embeddingsConfig{}, errors.New("DUDE_EMBEDDINGS_URL is not on DUDE_LLM_URL's origin, so DUDE_LLM_KEY is not sent to it: set DUDE_EMBEDDINGS_KEY")
	case inherited:
		c = embeddingsConfig{Off: "DUDE_LLM_URL has no key (DUDE_LLM_KEY)"}
	default:
		return embeddingsConfig{}, errors.New("embeddings at DUDE_EMBEDDINGS_URL need a key: set DUDE_EMBEDDINGS_KEY")
	}
	return c, nil
}

// sameOrigin reports whether a and b share scheme, host (case-insensitive)
// and port, with 80 and 443 as the http and https defaults.
func sameOrigin(a, b string) bool {
	oa, okA := origin(a)
	ob, okB := origin(b)
	return okA && okB && oa == ob
}

func origin(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	port := u.Port()
	if port == "" {
		switch scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		default:
			return "", false
		}
	}
	return scheme + "://" + strings.ToLower(u.Hostname()) + ":" + port, true
}
