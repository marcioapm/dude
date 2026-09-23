package phases

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// AgentConfig is how the orchestrator gives agents their credentials.
//
// OpenCode reads providers from opencode.json and keys from auth.json. Both
// travel to lux as file secrets: tmpfs, never snapshotted, supplied again on
// every resume, never stored by lux.
type AgentConfig struct {
	// Contents of OpenCode's auth.json.
	OpenCodeAuth string
	// OpenCode's provider definitions (the "provider" object of opencode.json).
	OpenCodeProviders json.RawMessage
	// Image used when a project names none.
	DefaultImage string
	// Hosts every agent may reach besides its model provider. "*" turns
	// egress filtering off.
	Egress []string
	// Wall-clock limit for one phase, across pauses.
	Timeout string
}

// LoadAgentConfig reads the agent configuration from the environment,
// falling back to this machine's OpenCode setup, so a developer with
// OpenCode configured can run real agents without another step.
func LoadAgentConfig() (AgentConfig, error) {
	c := AgentConfig{
		DefaultImage: envOr("DUDE_AGENT_IMAGE", "localhost/dude-runtime:dev"),
		Timeout:      envOr("DUDE_AGENT_TIMEOUT", "2h"),
	}
	home, _ := os.UserHomeDir()
	if b, err := readEnvOrFile("DUDE_OPENCODE_AUTH", home+"/.local/share/opencode/auth.json"); err != nil {
		return c, err
	} else {
		c.OpenCodeAuth = string(b)
	}
	cfg, err := readEnvOrFile("DUDE_OPENCODE_CONFIG", home+"/.config/opencode/opencode.json")
	if err != nil {
		return c, err
	}
	if len(cfg) > 0 {
		var full struct {
			Provider json.RawMessage `json:"provider"`
		}
		if err := json.Unmarshal(cfg, &full); err != nil {
			return c, fmt.Errorf("opencode config: %w", err)
		}
		c.OpenCodeProviders = full.Provider
	}
	for _, h := range strings.Split(os.Getenv("DUDE_AGENT_EGRESS"), ",") {
		if h = strings.TrimSpace(h); h != "" {
			c.Egress = append(c.Egress, h)
		}
	}
	return c, nil
}

// readEnvOrFile reads $name as a path if it names a file and as the value
// otherwise; unset, it reads the fallback file if there is one.
func readEnvOrFile(name, fallback string) ([]byte, error) {
	v := os.Getenv(name)
	if v == "" {
		b, err := os.ReadFile(fallback)
		if os.IsNotExist(err) {
			return nil, nil
		}
		return b, err
	}
	if b, err := os.ReadFile(v); err == nil {
		return b, nil
	}
	return []byte(v), nil
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// Where things live inside the container.
const (
	workspaceDir = "/workspace"
	agentHome    = "/home/agent"
)

// FakeModelPrefix selects the scripted fake agent: deterministic, free, and
// what the tests use to drive the whole pipeline without a model.
const FakeModelPrefix = "fake/"

// specInput is everything a phase Run's spec is built from.
type specInput struct {
	RunID, OrganizationID, WorkItemID, Phase, Role string
	Image                                          string
	Model                                          string
	Prompt                                         string
	RepoName, RepoURL, Ref                         string
	PushBranch                                     string
	ForgeToken                                     string
	// Fake agent only: whether a fixer has already been through, which is
	// what the scripted reviewer's verdict depends on.
	FixesDone int
}

// buildSpec turns a phase Run into a lux RunSpec.
//
// Everything that decides behaviour is here and nowhere in lux: which image,
// which model, what the agent is told, which commit it starts from and where
// its work is pushed.
func buildSpec(c AgentConfig, in specInput) lux.Spec {
	repoPath := workspaceDir + "/repos/" + in.RepoName
	spec := lux.Spec{
		Name: fmt.Sprintf("%s %s", in.Phase, in.WorkItemID),
		Labels: map[string]string{
			"dude.org": in.OrganizationID, "dude.workItem": in.WorkItemID,
			"dude.run": in.RunID, "dude.phase": in.Phase,
		},
		Image: lux.Image{Ref: in.Image},
		Workload: lux.Workload{
			Adapter: "opencode",
			Prompt:  in.Prompt,
			Workdir: repoPath,
		},
		Volumes: []lux.Volume{
			// The checkout, and the agent's session transcript: the two
			// things a resume on another host needs.
			{Name: "workspace", Path: workspaceDir, Kind: "state"},
			{Name: "home", Path: agentHome, Kind: "state"},
		},
		Git: &lux.Git{
			Repositories: []lux.Repository{{Name: in.RepoName, URL: in.RepoURL, Ref: in.Ref, Path: repoPath}},
		},
		Timeout: c.Timeout,
	}
	if in.ForgeToken != "" {
		// Used by lux to clone and push; never placed in the container.
		spec.Git.Repositories[0].Credential = "GIT_TOKEN"
		spec.Secrets = append(spec.Secrets, lux.Secret{Name: "GIT_TOKEN", Value: in.ForgeToken})
	}
	if in.PushBranch != "" {
		spec.Git.Push = &lux.Push{Branch: in.PushBranch}
	}

	if strings.HasPrefix(in.Model, FakeModelPrefix) {
		spec.Workload.Adapter = "acp"
		spec.Workload.Command = []string{"lux-fake"}
		spec.Workload.Prompt = fakeScript(in.Phase, in.RunID, in.FixesDone)
		// A task marked [hang] keeps its agent busy, so a test has a live
		// agent to steer, pause and abort.
		if strings.Contains(in.Prompt, "[hang]") {
			spec.Workload.Prompt = "sleep 3600\n" + spec.Workload.Prompt
		}
		return spec
	}

	config, _ := json.Marshal(map[string]any{"provider": c.OpenCodeProviders, "model": in.Model})
	spec.Secrets = append(spec.Secrets,
		lux.Secret{Name: "opencode_auth", Value: c.OpenCodeAuth, As: "file", Path: agentHome + "/.local/share/opencode/auth.json"},
		lux.Secret{Name: "opencode_config", Value: string(config), As: "file", Path: agentHome + "/.config/opencode/opencode.json"},
	)
	spec.Network = egress(c)
	return spec
}

// egress allows the model providers' hosts and anything configured. Without
// either, filtering is off rather than leaving an agent that cannot reach
// its own model.
func egress(c AgentConfig) *lux.Network {
	hosts := map[string]bool{}
	for _, h := range c.Egress {
		if h == "*" {
			return &lux.Network{Unrestricted: true}
		}
		hosts[h] = true
	}
	var providers map[string]struct {
		Options struct {
			BaseURL string `json:"baseURL"`
		} `json:"options"`
	}
	_ = json.Unmarshal(c.OpenCodeProviders, &providers)
	for _, p := range providers {
		if u, err := url.Parse(p.Options.BaseURL); err == nil && u.Hostname() != "" {
			hosts[u.Hostname()] = true
		}
	}
	if len(hosts) == 0 {
		return &lux.Network{Unrestricted: true}
	}
	n := &lux.Network{}
	for h := range hosts {
		n.Egress = append(n.Egress, lux.EgressRule{Host: h})
	}
	return n
}

// fakeScript is what the scripted agent does for one phase, in lux-fake's
// script language (lux/cmd/lux-fake). Test-only, and the reason the whole
// pipeline can run without a model.
//
// The reviewer raises one blocking finding until a fixer has been through,
// then reports clean — exactly the shape the review → fix loop needs: one
// cycle, then convergence. Its reply is assembled in a file and read back,
// because each reply line would otherwise run into the next.
func fakeScript(phase, runID string, fixesDone int) string {
	commit := func(file, text, msg string) string {
		return fmt.Sprintf("append %s %s\ncommit %s", file, text, msg)
	}
	switch phase {
	case delivery.PhaseImplement:
		return commit("FACTORY.md", "Written by run "+runID, "Add FACTORY.md for "+runID)
	case delivery.PhaseFix:
		// Content names the Run, so a second fix is still a change.
		return commit("FIXED.md", "addressed by "+runID, "Address review findings for "+runID)
	case delivery.PhaseSimplify:
		return commit("FACTORY.md", "simplified", "Simplify "+runID)
	case delivery.PhaseReview:
		if fixesDone > 0 {
			return "echo reviewed the fix; no further problems"
		}
		lines := []string{
			"---",
			"severity: blocking",
			"category: correctness",
			"file: FACTORY.md",
			"line: 1",
			"title: FACTORY.md does not record the fix",
			"description: The change is missing a record that the review was addressed.",
			"suggested_fix: Add a file naming what was fixed.",
		}
		var b strings.Builder
		for _, l := range lines {
			b.WriteString("append /tmp/review.yaml " + l + "\n")
		}
		b.WriteString("read /tmp/review.yaml")
		return b.String()
	}
	return "echo nothing to do for " + phase
}
