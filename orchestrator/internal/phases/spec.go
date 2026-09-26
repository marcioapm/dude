package phases

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"slices"
	"strings"

	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
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
	// A limit on a Run's running time (DUDE_AGENT_TIMEOUT), for an operator
	// who wants one; none by default. Agents work for days, and one waiting
	// on a person is parked, not timed out: lux counts only time spent
	// running, and a Run that names no timeout has none (lux d748aa5).
	Timeout string
	// Where agents reach dude's own tools (agenttools), as they see it; ""
	// gives them none. Must not be the lux host or lux's own address: lux
	// never lets a Run reach either.
	ToolsURL string
	// Signs Runs' tokens for the tools (agenttools.RunToken).
	ToolsKey []byte
	// The scripted agent needs nothing but the tools: restrict to them.
	toolsOnly bool
	// lux serves the tools as a local service in the container, for the dude
	// CLI (workload.services, lux 006bf42 and later). On by default; set
	// DUDE_TOOLS_SERVICE=off for a lux without it.
	ToolsService bool
}

// LoadAgentConfig reads the agent configuration from the environment,
// falling back to this machine's OpenCode setup, so a developer with
// OpenCode configured can run real agents without another step.
func LoadAgentConfig() (AgentConfig, error) {
	c := AgentConfig{
		DefaultImage: envOr("DUDE_AGENT_IMAGE", "localhost/dude-runtime:dev"),
		Timeout:      os.Getenv("DUDE_AGENT_TIMEOUT"),
		ToolsURL:     os.Getenv("DUDE_TOOLS_URL"),
		ToolsService: os.Getenv("DUDE_TOOLS_SERVICE") != "off",
		ToolsKey:     []byte(envOr("DUDE_TOOLS_KEY", os.Getenv("DUDE_ORCHESTRATOR_TOKEN"))),
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

// specInput is everything a phase Run's spec is built from.
type specInput struct {
	RunID, OrganizationID, TaskID, Phase, Role string
	Image                                      string
	Model                                      string
	Prompt                                     string
	// Every repository the task names, each at the commit this phase
	// starts from.
	Repos      []specRepo
	PushBranch string
	ForgeToken string
	// The Run's token for dude's tools; "" gives it none.
	ToolsToken string
}

type specRepo struct {
	Name, URL, Ref string
	// Cloned for context only: never pushed.
	ReadOnly bool
}

// workdir is where the agent starts: the one repository, or the directory
// holding them all (the prompt names each).
func workdir(repos []specRepo) string {
	if len(repos) == 1 {
		return RepoPath(repos[0].Name)
	}
	return workspaceDir
}

// RepoPath is where a repository is checked out in the container.
func RepoPath(name string) string { return workspaceDir + "/repos/" + name }

// Which coding agent runs a phase, as recorded on the Run: what the chat
// labels it with, and what dude's translation of its output assumes.
const (
	harnessOpenCode = "opencode"
	harnessScripted = "scripted"
)

// buildSpec turns a phase Run into a lux RunSpec.
//
// Everything that decides behaviour is here and nowhere in lux: which image,
// which model, what the agent is told, which commit it starts from and where
// its work is pushed.
func buildSpec(c AgentConfig, in specInput) lux.Spec {
	spec := lux.Spec{
		Name: fmt.Sprintf("%s %s", in.Phase, in.TaskID),
		Labels: map[string]string{
			"dude.org": in.OrganizationID, "dude.task": in.TaskID,
			"dude.run": in.RunID, "dude.phase": in.Phase,
			"dude.harness": harnessOpenCode, "dude.model": in.Model,
		},
		Image: lux.Image{Ref: in.Image},
		Workload: lux.Workload{
			Adapter: "opencode",
			Prompt:  in.Prompt,
			Workdir: workdir(in.Repos),
		},
		Volumes: []lux.Volume{
			// The checkout, and the agent's session transcript: the two
			// things a resume on another host needs.
			{Name: "workspace", Path: workspaceDir, Kind: "state"},
			{Name: "home", Path: agentHome, Kind: "state"},
		},
		Timeout: c.Timeout,
	}
	if len(in.Repos) > 0 || in.PushBranch != "" {
		spec.Git = &lux.Git{}
		for _, r := range in.Repos {
			repo := lux.Repository{Name: r.Name, URL: r.URL, Ref: r.Ref, Path: RepoPath(r.Name)}
			if in.ForgeToken != "" {
				// Used by lux to clone and push; never placed in the container.
				// Declared as a secret only with a repository that uses it:
				// one declared with none would be the workload's, and lux
				// would refuse it as the credential of one added later.
				repo.Credential = "GIT_TOKEN"
			}
			if r.ReadOnly {
				repo.Push = new(bool)
			}
			spec.Git.Repositories = append(spec.Git.Repositories, repo)
		}
		if in.PushBranch != "" {
			spec.Git.Push = &lux.Push{Branch: in.PushBranch}
		}
		if slices.ContainsFunc(spec.Git.Repositories, func(r lux.Repository) bool { return r.Credential == "GIT_TOKEN" }) {
			spec.Secrets = append(spec.Secrets, lux.Secret{Name: "GIT_TOKEN", Value: in.ForgeToken})
		}
	}

	if c.ToolsURL != "" && in.ToolsToken != "" {
		// dude's own tools, as this Run. lux serves them inside the container
		// — a socket for the dude CLI, loopback for the agent's MCP client —
		// and adds the Run's token on the way out: nothing in the container
		// ever holds it (lux d73b38c). DUDE_TOOLS_SERVICE=off, for a lux
		// without services, falls back to handing the header to the harness.
		auth := []lux.Header{{Name: "Authorization", Secret: "DUDE_TOOLS_AUTH"}}
		if c.ToolsService {
			spec.Workload.Services = []lux.Service{{Name: "dude", URL: c.ToolsURL, Headers: auth, Loopback: true}}
			spec.Workload.MCPServers = []lux.Service{{Name: "dude", Service: "dude"}}
		} else {
			spec.Workload.MCPServers = []lux.Service{{Name: "dude", URL: c.ToolsURL, Headers: auth}}
		}
		spec.Secrets = append(spec.Secrets, lux.Secret{Name: "DUDE_TOOLS_AUTH", Value: "Bearer " + in.ToolsToken})
	}

	// The scripted agent, for tests: lux-fake speaking ACP, following the
	// script fakeagent writes for this phase. The model is a label too, so a
	// stand-in for lux can play the same agent without parsing the script.
	if fakeagent.Is(in.Model) {
		spec.Labels["dude.harness"] = harnessScripted
		spec.Workload.Adapter = "acp"
		spec.Workload.Command = []string{"lux-fake"}
		spec.Workload.Prompt = fakeagent.Script(in.Phase, in.Model, in.RunID)
		if len(spec.Workload.MCPServers) > 0 {
			// Its tools must be reachable; nothing else needs to be.
			spec.Network = egress(AgentConfig{ToolsURL: c.ToolsURL, toolsOnly: true})
		}
		return spec
	}

	spec.Env = colourEnv
	config, _ := json.Marshal(map[string]any{"provider": c.OpenCodeProviders, "model": in.Model})
	spec.Secrets = append(spec.Secrets,
		lux.Secret{Name: "opencode_auth", Value: c.OpenCodeAuth, As: "file", Path: agentHome + "/.local/share/opencode/auth.json"},
		lux.Secret{Name: "opencode_config", Value: string(config), As: "file", Path: agentHome + "/.config/opencode/opencode.json"},
	)
	spec.Network = egress(c)
	return spec
}

// colourEnv makes the tools an agent runs colour their output, though it
// goes to a pipe rather than a terminal: pytest's failures in red, git's
// diffs, ls. The chat renders the colour; the agent reads past it.
var colourEnv = map[string]string{
	"TERM":           "xterm-256color",
	"FORCE_COLOR":    "1", // node, many Python tools
	"CLICOLOR_FORCE": "1", // BSD-style tools, ls on some systems
	"PY_COLORS":      "1", // pytest
	// git, through its environment rather than a config file in the image.
	"GIT_CONFIG_COUNT":   "1",
	"GIT_CONFIG_KEY_0":   "color.ui",
	"GIT_CONFIG_VALUE_0": "always",
}

// egress allows the model providers' hosts and anything configured. Without
// either, filtering is off rather than leaving an agent that cannot reach
// its own model.
func egress(c AgentConfig) *lux.Network {
	hosts := map[string]bool{}
	var cidrs []string
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
	if len(hosts) == 0 && !c.toolsOnly {
		// Nothing configured to restrict to: the agent could not reach its
		// own model otherwise. The tools alone restrict nothing.
		return &lux.Network{Unrestricted: true}
	}
	// dude's tools. An address goes in as an address: lux matches hosts by
	// name and addresses by range.
	if u, err := url.Parse(c.ToolsURL); err == nil && u.Hostname() != "" {
		if ip := net.ParseIP(u.Hostname()); ip != nil {
			cidrs = append(cidrs, ip.String()+"/32")
		} else {
			hosts[u.Hostname()] = true
		}
	}
	n := &lux.Network{}
	for h := range hosts {
		n.Egress = append(n.Egress, lux.EgressRule{Host: h})
	}
	for _, c := range cidrs {
		n.Egress = append(n.Egress, lux.EgressRule{CIDR: c})
	}
	return n
}
