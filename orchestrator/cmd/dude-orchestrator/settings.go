package main

import (
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/config"
	"github.com/marciomartins/dude/orchestrator/internal/phases"
	"github.com/marciomartins/dude/orchestrator/internal/registry"
)

// settings is every value run builds its services from, resolved from the
// config and checked, with no I/O: run is left to open, dial and listen.
type settings struct {
	DatabaseURL string
	Token       string // the service token the backend authenticates with
	Listen      string
	ToolsListen string // "" serves no agent tools

	LuxURL, LuxKey string
	ConsoleURL     string // lux.console_url, else lux.url

	// previews.domain ("": lux's own) and previews.reap_after.
	PreviewDomain    string
	PreviewReapAfter time.Duration

	Agent    phases.AgentConfig
	Registry registrySettings

	ReconcileEvery       time.Duration
	ParkAfter, IdleAfter time.Duration
	DiffEvery            time.Duration
	MachineUSDPerHour    float64
	LuxCostEvery         time.Duration

	FactoryLogins []string

	VAPIDPublic, VAPIDPrivate, VAPIDSubject string

	Embeddings          embeddingsConfig // URL "" for none
	EmbeddingsModel     string
	EmbeddingsDimension int
}

// registrySettings are registry.FromEnv's inputs; getenv hands them to it.
type registrySettings struct {
	Mode, Host, Credential, ECRRoleARN string
}

func (r registrySettings) getenv(name string) string {
	switch name {
	case "DUDE_REGISTRY_AUTH":
		return r.Mode
	case "DUDE_REGISTRY":
		return r.Host
	case "DUDE_REGISTRY_CREDENTIAL":
		return r.Credential
	case "DUDE_ECR_ROLE_ARN":
		return r.ECRRoleARN
	}
	return ""
}

// missingError is a required setting absent from both the file and the
// environment; main reports it on its own and exits 2.
type missingError struct{ label string }

func (e missingError) Error() string { return e.label + " is required" }

// indexDimensions is the size of memory's vector index; another is a migration.
const indexDimensions = 768

// previewDomainRe is a domain of DNS labels, at least two.
var previewDomainRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func settingsFrom(cfg *config.Config) (settings, error) {
	var s settings
	required := []struct {
		env string
		dst *string
	}{
		{"DATABASE_URL", &s.DatabaseURL}, {"LUX_URL", &s.LuxURL}, {"LUX_API_KEY", &s.LuxKey},
		{"DUDE_ORCHESTRATOR_TOKEN", &s.Token},
	}
	for _, r := range required {
		if *r.dst = cfg.String(r.env); *r.dst == "" {
			return settings{}, missingError{cfg.Label(r.env)}
		}
	}
	s.Listen = cfg.String("DUDE_ORCHESTRATOR_LISTEN")
	s.ToolsListen = cfg.String("DUDE_TOOLS_LISTEN")
	if s.ConsoleURL = cfg.String("LUX_CONSOLE_URL"); s.ConsoleURL == "" {
		s.ConsoleURL = s.LuxURL
	}
	s.PreviewDomain = strings.Trim(strings.ToLower(cfg.String("DUDE_PREVIEW_DOMAIN")), ".")
	if s.PreviewDomain != "" && !previewDomainRe.MatchString(s.PreviewDomain) {
		return settings{}, fmt.Errorf("%s: not a domain: %q", cfg.Label("DUDE_PREVIEW_DOMAIN"), s.PreviewDomain)
	}
	if s.PreviewReapAfter = cfg.Duration("DUDE_PREVIEW_REAP_AFTER"); s.PreviewReapAfter <= 0 {
		return settings{}, fmt.Errorf("%s: not a positive duration: %v", cfg.Label("DUDE_PREVIEW_REAP_AFTER"), s.PreviewReapAfter)
	}

	agent, err := phases.LoadAgentConfig(cfg)
	if err != nil {
		return settings{}, fmt.Errorf("agent configuration: %w", err)
	}
	s.Agent = agent
	s.Registry = registrySettings{
		Mode: cfg.String("DUDE_REGISTRY_AUTH"), Host: cfg.String("DUDE_REGISTRY"),
		Credential: cfg.String("DUDE_REGISTRY_CREDENTIAL"), ECRRoleARN: cfg.String("DUDE_ECR_ROLE_ARN"),
	}
	if err := registry.Check(s.Registry.getenv, agent.DefaultImage); err != nil {
		return settings{}, fmt.Errorf("registry login: %w", err)
	}

	s.ReconcileEvery = cfg.Duration("DUDE_PR_RECONCILE")
	s.ParkAfter, s.IdleAfter = cfg.Duration("DUDE_PARK_AFTER"), cfg.Duration("DUDE_IDLE_AFTER")
	s.DiffEvery = cfg.Duration("DUDE_DIFF_EVERY")
	// One rate for every lux host until lux reports each host's own.
	if s.MachineUSDPerHour = cfg.Float("DUDE_MACHINE_USD_PER_HOUR"); s.MachineUSDPerHour < 0 {
		return settings{}, fmt.Errorf("%s: not a rate: %v", cfg.Label("DUDE_MACHINE_USD_PER_HOUR"), s.MachineUSDPerHour)
	}
	if s.LuxCostEvery = cfg.Duration("DUDE_LUX_COST_EVERY"); s.LuxCostEvery <= 0 {
		return settings{}, fmt.Errorf("%s: not a positive duration: %v", cfg.Label("DUDE_LUX_COST_EVERY"), s.LuxCostEvery)
	}
	s.FactoryLogins = cfg.List("DUDE_FACTORY_LOGINS")
	s.VAPIDPublic, s.VAPIDPrivate = cfg.String("DUDE_VAPID_PUBLIC_KEY"), cfg.String("DUDE_VAPID_PRIVATE_KEY")
	s.VAPIDSubject = cfg.String("DUDE_VAPID_SUBJECT")

	if s.Embeddings, err = embeddingsFromEnv(cfg.Getenv); err != nil {
		return settings{}, err
	}
	if s.Embeddings.URL != "" {
		// Only a live embedder writes vectors, so only then must its size fit the index.
		if dims := cfg.Int("DUDE_EMBEDDINGS_DIMENSIONS"); dims != indexDimensions {
			return settings{}, fmt.Errorf("%s: the index holds %d dimensions, not %d",
				cfg.Label("DUDE_EMBEDDINGS_DIMENSIONS"), indexDimensions, dims)
		}
		s.EmbeddingsModel, s.EmbeddingsDimension = cfg.String("DUDE_EMBEDDINGS_MODEL"), indexDimensions
	}
	return s, nil
}

// logConfig reports which file was read and what the loader warned about.
func logConfig(log *slog.Logger, cfg *config.Config) {
	if cfg.Path != "" {
		log.Info("configuration file read", "path", cfg.Path)
	}
	for _, w := range cfg.Warnings {
		log.Warn(w)
	}
}
