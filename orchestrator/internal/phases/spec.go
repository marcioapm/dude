package phases

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
	"slices"

	"github.com/marciomartins/dude/orchestrator/internal/config"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// AgentConfig is how the orchestrator gives agents their model access.
//
// Every OpenCode Run gets the same two things: the LLM API's base URL, as plain
// env, and its key, as a lux env secret (never stored by lux, supplied again
// on every resume). The agent image defines the providers that read them.
type AgentConfig struct {
	// DUDE_LLM_URL: the LLM API's base URL, before the API path
	// (https://…/v1). Its host is agents' model egress.
	LLMURL string
	// DUDE_LLM_KEY: the LLM API's key.
	LLMKey string
	// Image used when a project names none.
	DefaultImage string
	// Hosts every agent may reach besides its model provider. "*" turns
	// egress filtering off.
	Egress []string
	// Agents may run docker or podman in their Run
	// (DUDE_AGENT_NESTED_CONTAINERS). lux places such Runs only on hosts
	// offering nested containers, so it is off unless its hosts do.
	NestedContainers bool
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

// LoadAgentConfig reads the agent configuration from the resolved settings.
func LoadAgentConfig(cfg *config.Config) (AgentConfig, error) {
	c := AgentConfig{
		LLMURL:       cfg.String("DUDE_LLM_URL"),
		LLMKey:       cfg.String("DUDE_LLM_KEY"),
		DefaultImage: cfg.String("DUDE_AGENT_IMAGE"),
		Timeout:      cfg.String("DUDE_AGENT_TIMEOUT"),
		ToolsURL:     cfg.String("DUDE_TOOLS_URL"),
		ToolsService: cfg.Bool("DUDE_TOOLS_SERVICE"),
		ToolsKey:     []byte(cfg.String("DUDE_TOOLS_KEY")),
		Egress:       cfg.List("DUDE_AGENT_EGRESS"),

		NestedContainers: cfg.Bool("DUDE_AGENT_NESTED_CONTAINERS"),
	}
	if len(c.ToolsKey) == 0 {
		c.ToolsKey = []byte(cfg.String("DUDE_ORCHESTRATOR_TOKEN"))
	}
	if c.LLMURL != "" {
		if err := ValidateHTTPURL(c.LLMURL); err != nil {
			return c, fmt.Errorf("%s: %w", cfg.Label("DUDE_LLM_URL"), err)
		}
	}
	return c, nil
}

// ValidateHTTPURL accepts an http or https URL with a host and no
// credentials in it.
func ValidateHTTPURL(raw string) error {
	// Errors name the scheme and host at most: the value may hold a secret.
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return errors.New("not a URL")
	case u.User != nil:
		return errors.New("the URL must not carry credentials")
	case (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "":
		return fmt.Errorf("need an http or https URL with a host, not scheme %q host %q", u.Scheme, u.Host)
	}
	return nil
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
	// The role's reasoning effort, "" for the model's own; and its running
	// time per session in minutes, 0 for none of its own.
	Effort           string
	TimeLimitMinutes int
	Prompt           string
	// Every repository the task names, each at the commit this phase
	// starts from.
	Repos      []specRepo
	PushBranch string
	ForgeToken string
	// The Run's token for dude's tools; "" gives it none.
	ToolsToken string
	// The login for Image's registry; nil when it needs none.
	Registry *RegistryLogin
	// The machine it runs on; nil leaves lux's default size and pool.
	Machine *delivery.Machine
}

// MachineSpec puts a machine size on a lux spec: its resources, memory and
// disk in bytes, and its pool when it names one (none is the tenant's
// default pool in lux). A nil machine leaves lux's defaults.
func MachineSpec(m *delivery.Machine, spec *lux.Spec) {
	if m == nil {
		return
	}
	spec.Resources = &lux.Resources{CPUs: m.CPUs, Memory: m.MemoryMiB << 20, Disk: m.DiskGiB << 30}
	if m.Pool != nil && *m.Pool != "" {
		spec.Placement = &lux.PlacementSpec{Pool: *m.Pool}
	}
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

// repoRefs is where each repository starts, by name.
func repoRefs(repos []specRepo) map[string]string {
	refs := make(map[string]string, len(repos))
	for _, r := range repos {
		refs[r.Name] = r.Ref
	}
	return refs
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
			// On every stop lux can see coming, the checkout's final diff
			// (livediff.go).
			BeforeStop: beforeStop(repoRefs(in.Repos)),
		},
		Volumes: []lux.Volume{
			// The checkout, and the agent's session transcript: the two
			// things a resume on another host needs.
			{Name: "workspace", Path: workspaceDir, Kind: "state"},
			{Name: "home", Path: agentHome, Kind: "state"},
		},
		Timeout: c.Timeout,
	}
	// A role's own limit is the organization's or project's choice, and
	// wins over the operator's blanket one.
	if in.TimeLimitMinutes > 0 {
		spec.Timeout = fmt.Sprintf("%dm", in.TimeLimitMinutes)
	}
	if in.Effort != "" {
		spec.Labels["dude.effort"] = in.Effort
	}
	in.Registry.Apply(&spec)
	MachineSpec(in.Machine, &spec)
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

	// Before the scripted agent returns: the contract suite runs it on a
	// real lux, which must place it like the agent it stands in for.
	if c.NestedContainers {
		spec.Sandbox = &lux.Sandbox{NestedContainers: true}
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

	spec.Env = maps.Clone(colourEnv)
	// The image's OpenCode config defines the providers, reading the URL and
	// key from these; the Run adds only its model and effort, which
	// OpenCode merges over that file (OPENCODE_CONFIG_CONTENT).
	if c.LLMURL != "" {
		spec.Env["DUDE_LLM_URL"] = c.LLMURL
	}
	spec.Env["OPENCODE_CONFIG_CONTENT"] = openCodeConfig(in.Model, in.Effort)
	if c.LLMKey != "" {
		spec.Secrets = append(spec.Secrets, lux.Secret{Name: "DUDE_LLM_KEY", Value: c.LLMKey, As: "env"})
	}
	spec.Network = egress(c)
	return spec
}

// openCodeConfig is a Run's model and reasoning effort as OpenCode config.
// OpenCode passes an agent's unknown options to the provider as model
// options; reasoningEffort is the one providers read. Their scale stops at
// high, so dude's "max" is the most they take.
func openCodeConfig(model, effort string) string {
	config := map[string]any{"model": model}
	if effort != "" {
		if effort == "max" {
			effort = "high"
		}
		config["agent"] = map[string]any{"build": map[string]any{"reasoningEffort": effort}}
	}
	b, _ := json.Marshal(config)
	return string(b)
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

// egress allows the LLM API's host and anything configured. Without either,
// filtering is off rather than leaving an agent that cannot reach its own
// model.
func egress(c AgentConfig) *lux.Network {
	hosts := map[string]bool{}
	var cidrs []string
	for _, h := range c.Egress {
		if h == "*" {
			return &lux.Network{Unrestricted: true}
		}
		hosts[h] = true
	}
	if u, err := url.Parse(c.LLMURL); err == nil && u.Hostname() != "" {
		hosts[u.Hostname()] = true
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
