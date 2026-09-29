package main

import (
	"errors"

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
func embeddingsFromEnv(getenv func(string) string) (embeddingsConfig, error) {
	var c embeddingsConfig
	switch url := getenv("DUDE_EMBEDDINGS_URL"); {
	case url == "off":
		c.Off = "DUDE_EMBEDDINGS_URL is off"
		return c, nil
	case url != "":
		c.URL, c.URLFrom = url, "DUDE_EMBEDDINGS_URL"
	case getenv("DUDE_LLM_URL") != "":
		c.URL, c.URLFrom = getenv("DUDE_LLM_URL"), "DUDE_LLM_URL"
	default:
		c.Off = "neither DUDE_EMBEDDINGS_URL nor DUDE_LLM_URL is set"
		return c, nil
	}
	if err := phases.ValidateHTTPURL(c.URL); err != nil {
		return embeddingsConfig{}, errors.New(c.URLFrom + " for embeddings: " + err.Error())
	}
	if key := getenv("DUDE_EMBEDDINGS_KEY"); key != "" {
		c.Key, c.KeyFrom = key, "DUDE_EMBEDDINGS_KEY"
	} else if key := getenv("DUDE_LLM_KEY"); key != "" {
		c.Key, c.KeyFrom = key, "DUDE_LLM_KEY"
	} else {
		return embeddingsConfig{}, errors.New("embeddings at " + c.URLFrom + " need a key: set DUDE_EMBEDDINGS_KEY or DUDE_LLM_KEY")
	}
	return c, nil
}
