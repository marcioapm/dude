package main

import (
	"fmt"
	"io"

	"github.com/marciomartins/dude/orchestrator/internal/config"
)

// resolve is every configuration check startup makes before it opens a
// connection: the loader's, then settingsFrom's. cfg is nil when the loader
// failed.
func resolve(opts config.Options) (*config.Config, settings, error) {
	cfg, err := config.Load(config.Orchestrator, opts)
	if err != nil {
		return nil, settings{}, fmt.Errorf("configuration: %w", err)
	}
	set, err := settingsFrom(cfg)
	return cfg, set, err
}

// validate is `dude-orchestrator validate`: resolve and nothing else, so it
// refuses what startup refuses on configuration and connects, listens and
// writes nothing. Exit 0 with "ok: <file>" on stdout, 1 with startup's error
// on stderr, 2 on extra arguments. Warnings go to stderr either way.
func validate(args []string, opts config.Options, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintln(stderr, "usage: dude-orchestrator validate")
		return 2
	}
	cfg, _, err := resolve(opts)
	if cfg != nil {
		for _, w := range cfg.Warnings {
			fmt.Fprintln(stderr, "warning: "+w)
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	path := cfg.Path
	if path == "" {
		path = "no file"
	}
	fmt.Fprintln(stdout, "ok: "+path)
	return 0
}
