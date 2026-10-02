// dude-image-builder builds the organizations' library images on the dude
// host, one at a time: a Run's finish (adding the dude layer) before any
// build, each oldest first. Rootless podman, as its own unprivileged user;
// it connects to the database as dude_builder, which sees the image tables
// and nothing else. The orchestrator and the backend never run podman.
//
//	dude-image-builder             run until SIGINT/SIGTERM
//	dude-image-builder --version   print the version
//	dude-image-builder validate    check the configuration as startup does
//
// Its settings (dude's configuration file, or each variable):
//
//	DUDE_BUILDER_DATABASE_URL    the dude_builder role's DSN (required)
//	DUDE_BUILDER_REPOSITORY      where versions are pushed: <registry>/<repo> (required)
//	DUDE_LAYER_IMAGE             the dude layer, by digest (required)
//	DUDE_BUILDER_AUTHFILE        podman's auth file for the repository
//	DUDE_BUILDER_PLATFORM        linux/arm64
//	DUDE_BUILDER_CPUS            1.5, each build's CPUs
//	DUDE_BUILDER_MEMORY          1536m, each build's memory and swap
//	DUDE_BUILDER_TIMEOUT         60m, each build's wall-clock limit
//	DUDE_BUILDER_MIN_FREE_BYTES  8 GiB free in podman's storage, pruning once to get it
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"regexp"
	"syscall"

	"github.com/marciomartins/dude/orchestrator/internal/config"
	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/images"
	"github.com/marciomartins/dude/orchestrator/internal/version"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println(version.Version)
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "validate" {
		os.Exit(validate(os.Args[2:], config.Options{}, os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 {
		fmt.Fprintln(os.Stderr, "usage: dude-image-builder [--version | validate]")
		os.Exit(2)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(log); err != nil {
		fmt.Fprintln(os.Stderr, "dude-image-builder: "+err.Error())
		os.Exit(1)
	}
}

type settings struct {
	DatabaseURL, Repository, Layer, Authfile string
	Limits                                   images.Limits
	MinFree                                  int64
}

var repoRef = regexp.MustCompile(`^[a-z0-9.-]+(:[0-9]+)?(/[a-z0-9._-]+)+$`)

// resolve is every configuration check startup makes before it connects.
func resolve(opts config.Options) (*config.Config, settings, error) {
	cfg, err := config.Load(config.Builder, opts)
	if err != nil {
		return nil, settings{}, fmt.Errorf("configuration: %w", err)
	}
	s := settings{
		DatabaseURL: cfg.String("DUDE_BUILDER_DATABASE_URL"),
		Repository:  cfg.String("DUDE_BUILDER_REPOSITORY"),
		Layer:       cfg.String("DUDE_LAYER_IMAGE"),
		Authfile:    cfg.String("DUDE_BUILDER_AUTHFILE"),
		Limits: images.Limits{Platform: cfg.String("DUDE_BUILDER_PLATFORM"), CPUs: cfg.Float("DUDE_BUILDER_CPUS"),
			Memory: cfg.String("DUDE_BUILDER_MEMORY"), Timeout: cfg.Duration("DUDE_BUILDER_TIMEOUT")},
		MinFree: cfg.Int("DUDE_BUILDER_MIN_FREE_BYTES"),
	}
	var problems []error
	need := func(env, v string) {
		if v == "" {
			problems = append(problems, fmt.Errorf("%s is required", cfg.Label(env)))
		}
	}
	need("DUDE_BUILDER_DATABASE_URL", s.DatabaseURL)
	need("DUDE_BUILDER_REPOSITORY", s.Repository)
	need("DUDE_LAYER_IMAGE", s.Layer)
	if s.Layer != "" && !images.IsDigestRef(s.Layer) {
		problems = append(problems, fmt.Errorf("%s must name the layer by digest (…@sha256:<64 hex>), not %q", cfg.Label("DUDE_LAYER_IMAGE"), s.Layer))
	}
	if s.Repository != "" && !repoRef.MatchString(s.Repository) {
		problems = append(problems, fmt.Errorf("%s must be <registry>/<repository> with no tag, not %q", cfg.Label("DUDE_BUILDER_REPOSITORY"), s.Repository))
	}
	if s.Limits.CPUs <= 0 {
		problems = append(problems, fmt.Errorf("%s must be more than 0", cfg.Label("DUDE_BUILDER_CPUS")))
	}
	if _, ok := images.MemoryBytes(s.Limits.Memory); !ok {
		problems = append(problems, fmt.Errorf("%s must be whole bytes or a whole number of k, m or g (1536m, 2g), not %q", cfg.Label("DUDE_BUILDER_MEMORY"), s.Limits.Memory))
	}
	if s.Limits.Timeout <= 0 {
		problems = append(problems, fmt.Errorf("%s must be more than 0", cfg.Label("DUDE_BUILDER_TIMEOUT")))
	}
	return cfg, s, errors.Join(problems...)
}

// validate is `dude-image-builder validate`, as dude-orchestrator's: exit
// 0 with "ok: <file>", 1 with startup's error, 2 on extra arguments.
func validate(args []string, opts config.Options, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintln(stderr, "usage: dude-image-builder validate")
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

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	cfg, s, err := resolve(config.Options{})
	if err != nil {
		return err
	}
	for _, w := range cfg.Warnings {
		log.Warn(w)
	}
	podman := images.CLI{Limits: s.Limits, Authfile: s.Authfile, TLSVerify: true}
	// podman only warns when it cannot apply --memory or the CPU quota, and
	// builds unlimited: the builder does not start on such a host.
	if err := images.CheckLimits(ctx, podman); err != nil {
		return err
	}
	database, err := db.Open(ctx, s.DatabaseURL)
	if err != nil {
		return err
	}
	defer database.Close()
	log.Info("image builder started", "version", version.Version, "repository", s.Repository, "layer", images.LayerShort(s.Layer),
		"cpus", s.Limits.CPUs, "memory", s.Limits.Memory, "platform", s.Limits.Platform)
	b := &images.Builder{
		DB:         database,
		Podman:     podman,
		Repository: s.Repository,
		Layer:      s.Layer,
		Limits:     s.Limits,
		MinFree:    s.MinFree,
		Log:        log,
		Version:    version.Version,
	}
	return b.Run(ctx)
}
