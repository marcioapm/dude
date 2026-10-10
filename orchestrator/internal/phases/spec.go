package phases

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/marciomartins/dude/orchestrator/internal/config"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/llm"
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
	// The dude layer library images are finished with (DUDE_LAYER_IMAGE);
	// "" turns the library off: a Run whose image is a library image fails.
	Layer string
	// The operator's floor (agent.egress): what every agent may reach,
	// under its organisation's and project's lists. "*" turns egress
	// filtering off for every Run.
	Egress []string
	// DefaultImage can run containers (DUDE_AGENT_NESTED_CONTAINERS): a Run
	// on it, agent or preview, asks lux for nested containers. A library
	// image says so per version instead (images.Site.Containers).
	NestedContainers bool
	// The most running time lux gives a phase Run (DUDE_AGENT_TIMEOUT,
	// DefaultTimeout when unset): past it, lux stops the Run and it fails.
	// lux counts only time spent running, so a parked Run is not timed.
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

// DefaultTimeout is a phase Run's hard limit when none is configured.
const DefaultTimeout = "4h"

// LoadAgentConfig reads the agent configuration from the resolved settings.
func LoadAgentConfig(cfg *config.Config) (AgentConfig, error) {
	c := AgentConfig{
		LLMURL:       cfg.String("DUDE_LLM_URL"),
		LLMKey:       cfg.String("DUDE_LLM_KEY"),
		DefaultImage: cfg.String("DUDE_AGENT_IMAGE"),
		Layer:        cfg.String("DUDE_LAYER_IMAGE"),
		Timeout:      cfg.String("DUDE_AGENT_TIMEOUT"),
		ToolsURL:     cfg.String("DUDE_TOOLS_URL"),
		ToolsService: cfg.Bool("DUDE_TOOLS_SERVICE"),
		ToolsKey:     []byte(cfg.String("DUDE_TOOLS_KEY")),
		Egress:       slices.Clone(cfg.List("DUDE_AGENT_EGRESS")),

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
	// Refused here, not left out of a Run: with no model's host, a floor
	// left empty would make every Run unrestricted.
	for i, e := range c.Egress {
		c.Egress[i] = strings.ToLower(e)
		if _, ok := lux.ParseEgressRule(c.Egress[i]); e != "*" && !ok {
			return c, fmt.Errorf("%s: %q: not a hostname, address, range or *.<domain> lux takes", cfg.Label("DUDE_AGENT_EGRESS"), e)
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
	// A session's agent: its session, in place of a task.
	SessionID string
	Image     string
	// Image can run containers: lux places the Run only on a host that
	// allows them.
	NestedContainers bool
	// The model the Run's tier requests, as the proxy names it, and the
	// tier's name (recorded on the Run and as a label).
	Model, ModelTier string
	// The tier's reasoning effort, "" for the model's own, and its extra
	// model options (OpenCode's; for another harness, its args) and
	// request headers.
	Effort  string
	Options map[string]any
	Headers map[string]string
	// The role's harness (delivery.Harness*); "" is OpenCode.
	Harness string
	Prompt  string
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
	// What it may reach besides the operator's floor (RunEgress).
	Egress []string
}

// MachineSpec puts a machine size on a lux spec: its resources, memory and
// disk in bytes, and its pool by lux's id when it names one (none is the
// tenant's default pool in lux). A nil machine leaves lux's defaults.
func MachineSpec(m *delivery.Machine, spec *lux.Spec) {
	if m == nil {
		return
	}
	spec.Resources = &lux.Resources{CPUs: m.CPUs, Memory: m.MemoryMiB << 20, Disk: m.DiskGiB << 30}
	if m.PoolID != nil && *m.PoolID != "" {
		spec.Placement = &lux.PlacementSpec{PoolID: *m.PoolID}
	}
}

// NamePool records on m its pool's name in lux now, for runs.machine, so the
// Run's history says where it ran after the pool is renamed. Best effort: a
// pool lux does not list, or lux not answering, leaves it unnamed, and the
// submit that follows is what decides.
func NamePool(ctx context.Context, c lux.Client, m *delivery.Machine) {
	if m == nil || m.PoolID == nil {
		return
	}
	pools, err := c.Pools(ctx)
	if err != nil {
		return
	}
	for _, p := range pools {
		if p.ID == *m.PoolID {
			name := p.Name
			m.Pool = &name
			return
		}
	}
}

// PoolGone is the reason a Run fails when lux refuses its size's pool as
// unknown (422 unknown_pool): the pool was deleted after the size named it.
// "" for any other error, or with no machine (only a machine places a Run).
func PoolGone(err error, m *delivery.Machine) string {
	if m == nil {
		return ""
	}
	le, ok := lux.AsError(err)
	if !ok || le.Code != lux.CodeUnknownPool {
		return ""
	}
	return fmt.Sprintf("Its machine size, %s, runs in a lux pool that no longer exists. Give %s another pool in Machines.", m.Name, m.Name)
}

type specRepo struct {
	Name, URL, Ref string
	// Cloned for context only: never pushed.
	ReadOnly bool
	// Where it is checked out; "" is RepoPath(Name).
	Path string
}

func (r specRepo) path() string {
	if r.Path != "" {
		return r.Path
	}
	return RepoPath(r.Name)
}

// workdir is where the agent starts: the one repository, or the directory
// holding them all (the prompt names each).
func workdir(repos []specRepo) string {
	if len(repos) == 1 {
		return repos[0].path()
	}
	return workspaceDir
}

// repoRefs is where each repository starts, by its name in the spec (what
// lux reports the checkout as, and what a live read of the diff names).
func repoRefs(repos []specRepo) map[string]string {
	refs := make(map[string]string, len(repos))
	for _, r := range repos {
		refs[lux.SpecName(r.Name)] = r.Ref
	}
	return refs
}

// repositoriesBySpecName maps lux's reported names back to dude's repositories.
func repositoriesBySpecName(repos []delivery.Repository) map[string]delivery.Repository {
	byName := make(map[string]delivery.Repository, len(repos))
	for _, repo := range repos {
		byName[lux.SpecName(repo.Name)] = repo
	}
	return byName
}

// RepoPath is where a repository is checked out in the container, given
// its name or its name in the spec (lux.SpecName leaves that as it is): a
// name lux would refuse is checked out under the one the spec gives it.
func RepoPath(name string) string { return workspaceDir + "/repos/" + lux.SpecName(name) }

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
	// What it is: its phase, or for a task's conductor (no phase) its role.
	step := in.Phase
	if step == "" {
		step = in.Role
	}
	owner := in.TaskID
	if owner == "" {
		owner = in.SessionID
	}
	spec := lux.Spec{
		Name: fmt.Sprintf("%s %s", step, owner),
		Labels: map[string]string{
			lux.AppLabel: lux.App, "dude.org": in.OrganizationID, "dude.task": in.TaskID,
			"dude.run": in.RunID, "dude.phase": step, "dude.role": in.Role,
			"dude.harness": harnessOpenCode, "dude.model": in.Model,
		},
		Image: lux.Image{Ref: in.Image},
		Workload: lux.Workload{
			Adapter: "opencode",
			Prompt:  in.Prompt,
			Workdir: workdir(in.Repos),
			// On every stop lux can see coming, the checkout's final diff
			// (livediff.go). A session's agent changes nothing: none.
			BeforeStop: beforeStop(repoRefs(in.Repos)),
		},
		Volumes: []lux.Volume{
			// The checkout, and the agent's session transcript: the two
			// things a resume on another host needs.
			{Name: "workspace", Path: workspaceDir, Kind: "state"},
			{Name: "home", Path: agentHome, Kind: "state"},
		},
		Timeout: cmp.Or(c.Timeout, DefaultTimeout),
	}
	// The conductor parks while idle, and its running time adds up across
	// the whole task: the hard limit is for phase Runs only.
	if in.Phase == "" {
		spec.Timeout = ""
	}
	if in.SessionID != "" {
		delete(spec.Labels, "dude.task")
		spec.Labels["dude.session"] = in.SessionID
		spec.Workload.BeforeStop = nil
	}
	if in.Effort != "" {
		spec.Labels["dude.effort"] = in.Effort
	}
	if in.ModelTier != "" {
		spec.Labels["dude.model_tier"] = in.ModelTier
	}
	in.Registry.Apply(&spec)
	MachineSpec(in.Machine, &spec)
	if len(in.Repos) > 0 || in.PushBranch != "" {
		spec.Git = &lux.Git{}
		for _, r := range in.Repos {
			// lux takes only some names (lux.NameRe); everything lux reports
			// back — checkouts, clones, pushes, syncs — carries this one.
			repo := lux.Repository{Name: lux.SpecName(r.Name), URL: r.URL, Ref: r.Ref, Path: r.path()}
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
	if in.NestedContainers {
		spec.Sandbox = &lux.Sandbox{NestedContainers: true}
	}

	// The scripted agent, for tests: lux-fake following the script
	// fakeagent writes for this phase, speaking ACP — or, for a role on
	// Claude Code or Codex, that harness's protocol, which lux-fake speaks
	// when its adapter runs it. The model is a label too, so a stand-in for
	// lux can play the same agent without parsing the script.
	if fakeagent.Is(in.Model) {
		spec.Labels["dude.harness"] = harnessScripted
		spec.Workload.Adapter = "acp"
		if in.Harness == delivery.HarnessClaudeCode || in.Harness == delivery.HarnessCodex {
			spec.Labels["dude.harness"] = in.Harness
			spec.Workload.Adapter = in.Harness
		}
		spec.Workload.Command = []string{"lux-fake"}
		spec.Workload.Prompt = fakeagent.Script(step, in.Model, in.RunID)
		if in.Phase == "" && (in.Role == fakeagent.Conductor || in.Role == fakeagent.Brainstorm) {
			spec.Workload.Prompt = fakeagent.ConductorScript(in.Prompt)
		}
		if len(spec.Workload.MCPServers) > 0 || len(in.Egress) > 0 {
			// Its tools must be reachable, and what it stands in for may
			// reach; it needs no model.
			spec.Network = egress(AgentConfig{ToolsURL: c.ToolsURL, Egress: c.Egress, toolsOnly: true}, in.Egress)
		}
		return spec
	}

	spec.Env = maps.Clone(colourEnv)
	switch in.Harness {
	case delivery.HarnessClaudeCode:
		claudeCodeWorkload(c, in, &spec)
	case delivery.HarnessCodex:
		codexWorkload(c, in, &spec)
	default:
		openCodeWorkload(c, in, &spec)
	}
	spec.Network = egress(c, in.Egress)
	return spec
}

// openCodeWorkload: the image's OpenCode config defines the two providers,
// reading the URL and key from DUDE_LLM_URL and DUDE_LLM_KEY; the Run adds
// its model, declared under the provider its name goes through with its
// tier's options and headers, all of which OpenCode deep-merges over that
// file (OPENCODE_CONFIG_CONTENT).
func openCodeWorkload(c AgentConfig, in specInput, spec *lux.Spec) {
	if c.LLMURL != "" {
		spec.Env["DUDE_LLM_URL"] = c.LLMURL
	}
	spec.Env["OPENCODE_CONFIG_CONTENT"] = openCodeConfig(in.Model, in.Effort, in.Options, in.Headers)
	if c.LLMKey != "" {
		spec.Secrets = append(spec.Secrets, lux.Secret{Name: "DUDE_LLM_KEY", Value: c.LLMKey, As: "env"})
	}
}

// claudeCodeWorkload runs Claude Code on the tier's model, against the
// proxy's Anthropic Messages API. Its session lives in $HOME/.claude, under
// the home state volume every Run has.
//
// Thinking is asked for summarized (--thinking-display, a flag Claude Code
// does not list): without it, thinking comes back with empty text, and no
// setting or variable does the same. Effort none turns thinking off; an
// unset effort leaves the model's own.
func claudeCodeWorkload(c AgentConfig, in specInput, spec *lux.Spec) {
	spec.Labels["dude.harness"] = delivery.HarnessClaudeCode
	spec.Workload.Adapter = delivery.HarnessClaudeCode
	cmd := []string{"claude", "--model", in.Model, "--permission-mode", "bypassPermissions", "--thinking-display", "summarized"}
	switch in.Effort {
	case "":
	case "none":
		cmd = append(cmd, "--thinking", "disabled")
	default:
		cmd = append(cmd, "--effort", in.Effort)
	}
	spec.Workload.Command = append(cmd, harnessArgs(in.Options)...)
	if c.LLMURL != "" {
		spec.Env["ANTHROPIC_BASE_URL"] = anthropicBaseURL(c.LLMURL)
	}
	// Shell commands stay in the foreground, where the turn waits for them
	// and lux sees them; nothing updates the binary or calls home.
	spec.Env["CLAUDE_CODE_DISABLE_BACKGROUND_TASKS"] = "1"
	spec.Env["DISABLE_AUTOUPDATER"] = "1"
	spec.Env["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"] = "1"
	if len(in.Headers) > 0 {
		var lines []string
		for _, name := range slices.Sorted(maps.Keys(in.Headers)) {
			lines = append(lines, name+": "+in.Headers[name])
		}
		spec.Env["ANTHROPIC_CUSTOM_HEADERS"] = strings.Join(lines, "\n")
	}
	if c.LLMKey != "" {
		spec.Secrets = append(spec.Secrets, lux.Secret{Name: "ANTHROPIC_API_KEY", Value: c.LLMKey, As: "env"})
	}
}

// anthropicBaseURL is the proxy's URL as Claude Code takes it: without the
// /v1 it adds to every path itself.
func anthropicBaseURL(llmURL string) string {
	return strings.TrimSuffix(strings.TrimRight(llmURL, "/"), "/v1")
}

// codexWorkload runs Codex on the tier's model through a provider of its
// own, "dude": the proxy's Responses API (the only one the proxy passes
// reasoning summaries on), keyed by OPENAI_API_KEY, which lux also writes
// as Codex's auth.json. Its session lives in $HOME/.codex, under the home
// state volume.
//
// Codex sends reasoning only for a model it knows does summaries, and it
// does not know the proxy's names: model_supports_reasoning_summaries says
// so. Effort none or unset sends no effort.
//
// The same settings are also Codex's config.toml, a file secret in
// $HOME/.codex: lux's adapter adds its MCP servers as -c overrides after
// app-server, and Codex 0.144 then drops every -c given before the
// subcommand, these included. The file is read either way. A tier's args
// go the same way (codexSettings): only its -c overrides, as lines of the
// file, since any other argument before app-server is dropped too.
func codexWorkload(c AgentConfig, in specInput, spec *lux.Spec) {
	spec.Labels["dude.harness"] = delivery.HarnessCodex
	spec.Workload.Adapter = delivery.HarnessCodex
	settings, _ := codexSettings(c, in)
	cmd := []string{"codex"}
	for _, s := range settings {
		cmd = append(cmd, "-c", s)
	}
	spec.Workload.Command = cmd
	spec.Secrets = append(spec.Secrets, lux.Secret{Name: "CODEX_CONFIG", Value: strings.Join(settings, "\n") + "\n",
		As: "file", Path: agentHome + "/.codex/config.toml"})
	if c.LLMKey != "" {
		spec.Secrets = append(spec.Secrets, lux.Secret{Name: "OPENAI_API_KEY", Value: c.LLMKey, As: "env"})
	}
}

// codexSettings are a Codex Run's config.toml lines (key=value), and the
// tier's args it drops. Each `-c key=value` (or --config) in the args
// replaces dude's line for that key or adds one; the value is a JSON
// string, number, boolean or array of those, or else a bare word, taken as
// a string, as Codex takes an unparseable value. An override inside or
// above a table dude sets (model_providers.dude.*) would rewrite it, and is
// dropped, as is every other argument.
func codexSettings(c AgentConfig, in specInput) (settings, dropped []string) {
	provider := "{name=" + tomlString("dude") + ", base_url=" + tomlString(c.LLMURL) +
		", env_key=" + tomlString("OPENAI_API_KEY") + ", wire_api=" + tomlString("responses")
	if len(in.Headers) > 0 {
		var kv []string
		for _, name := range slices.Sorted(maps.Keys(in.Headers)) {
			kv = append(kv, tomlString(name)+"="+tomlString(in.Headers[name]))
		}
		provider += ", http_headers={" + strings.Join(kv, ", ") + "}"
	}
	provider += "}"
	settings = []string{
		"approval_policy=" + tomlString("never"),
		"sandbox_mode=" + tomlString("danger-full-access"),
		"check_for_update_on_startup=false",
		"model=" + tomlString(in.Model),
		"model_provider=" + tomlString("dude"),
		"model_providers.dude=" + provider,
		"model_reasoning_summary=" + tomlString("auto"),
		"model_supports_reasoning_summaries=true",
	}
	if in.Effort != "" && in.Effort != "none" {
		settings = append(settings, "model_reasoning_effort="+tomlString(in.Effort))
	}
	args := harnessArgs(in.Options)
	for i := 0; i < len(args); i++ {
		arg, override := args[i], ""
		switch {
		case (arg == "-c" || arg == "--config") && i+1 < len(args):
			i++
			override = args[i]
		case strings.HasPrefix(arg, "--config="):
			override = strings.TrimPrefix(arg, "--config=")
		default:
			dropped = append(dropped, arg)
			continue
		}
		key, raw, _ := strings.Cut(override, "=")
		key = strings.TrimSpace(key)
		value, ok := codexValue(strings.TrimSpace(raw))
		at := slices.IndexFunc(settings, func(s string) bool { k, _, _ := strings.Cut(s, "="); return k == key })
		nested := slices.ContainsFunc(settings, func(s string) bool {
			k, _, _ := strings.Cut(s, "=")
			return key == "model_providers.dude" || strings.HasPrefix(key, k+".") || strings.HasPrefix(k, key+".")
		})
		if !ok || !codexKey.MatchString(key) || nested {
			dropped = append(dropped, arg+" "+override)
			continue
		}
		if at >= 0 {
			settings[at] = key + "=" + value
		} else {
			settings = append(settings, key+"="+value)
		}
	}
	return settings, dropped
}

// codexKey is a dotted TOML key of bare parts, as -c takes one.
var codexKey = regexp.MustCompile(`^[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+)*$`)

// codexValue is an override's value as TOML, and whether dude can write it.
func codexValue(raw string) (string, bool) {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		// Not JSON: a bare word is a string; TOML's own syntax is not read.
		if raw == "" || strings.ContainsAny(raw[:1], `"'[{`) {
			return "", false
		}
		return tomlString(raw), true
	}
	scalar := func(v any) (string, bool) {
		switch x := v.(type) {
		case string:
			return tomlString(x), true
		case bool, float64:
			b, _ := json.Marshal(x)
			return string(b), true
		}
		return "", false
	}
	list, isList := v.([]any)
	if !isList {
		return scalar(v)
	}
	items := make([]string, 0, len(list))
	for _, item := range list {
		s, ok := scalar(item)
		if !ok {
			return "", false
		}
		items = append(items, s)
	}
	return "[" + strings.Join(items, ", ") + "]", true
}

// tomlString is s as a TOML basic string: a JSON string is one, once DEL,
// which JSON leaves raw and TOML does not allow raw, is escaped too.
func tomlString(s string) string {
	b, _ := json.Marshal(s)
	return strings.ReplaceAll(string(b), "\x7f", `\u007f`)
}

// harnessArgs are a tier's extra command-line arguments for Claude Code or
// Codex: its options' "args", a list of strings. Claude Code's are appended
// to its command; Codex takes only their -c overrides (codexSettings).
// Anything else in options is OpenCode's and means nothing to them
// (ignoredOptions names it).
func harnessArgs(options map[string]any) []string {
	list, _ := options["args"].([]any)
	var out []string
	for _, a := range list {
		if s, ok := a.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// ignoredOptions are the keys of a tier's options a Run on harness does
// not use: every key but "args" on Claude Code or Codex, none on OpenCode.
func ignoredOptions(harness string, options map[string]any) []string {
	if harness != delivery.HarnessClaudeCode && harness != delivery.HarnessCodex {
		return nil
	}
	var out []string
	for _, k := range slices.Sorted(maps.Keys(options)) {
		if k != "args" {
			out = append(out, k)
		}
	}
	return out
}

// harnessMisfit is why the role's harness cannot run its tier's model, the
// reason its Run fails with; "" when it can. The scripted agent stands in
// for any harness.
func harnessMisfit(settings delivery.RoleSettings, tier delivery.Tier, role string) string {
	if fakeagent.Is(tier.Model) {
		return ""
	}
	return delivery.HarnessFits(settings.HarnessName(), tier.Model, tier.Name, delivery.RoleName(role),
		llm.Provider(tier.Model) == llm.ProviderAnthropic)
}

// submittedHarness is the harness a resumed Run goes on with: the one it
// was submitted on (runs.harness), whatever its role says now, since lux
// resumes the command the spec named. A scripted Run, or one from before
// the column was kept, takes the role's.
func submittedHarness(ranOn, role string) string {
	switch ranOn {
	case delivery.HarnessOpenCode, delivery.HarnessClaudeCode, delivery.HarnessCodex:
		return ranOn
	}
	return role
}

// logIgnoredOptions says which of the tier's options a Run on Claude Code
// or Codex leaves out (only "args" means anything to them), and which of
// its args Codex leaves out (all but its -c overrides).
func (s *Syncer) logIgnoredOptions(r phaseRun, in specInput) {
	if s.Log == nil {
		return
	}
	if ignored := ignoredOptions(in.Harness, in.Options); len(ignored) > 0 {
		s.Log.Info("the tier's options other than args are OpenCode's; ignored on this harness",
			"run", r.ID, "harness", in.Harness, "tier", in.ModelTier, "ignored", ignored)
	}
	if in.Harness == delivery.HarnessCodex {
		if _, dropped := codexSettings(s.Agent, in); len(dropped) > 0 {
			s.Log.Warn("Codex takes only -c key=value args from a tier, written to its config.toml; dropped the rest",
				"run", r.ID, "tier", in.ModelTier, "dropped", dropped)
		}
	}
}

// openCodeConfig is a Run's model and its tier's settings as OpenCode
// config.
//
// OpenCode refuses a model its provider does not declare (Model not found),
// so the model is declared here under the provider its name goes through
// (llm.Provider): any name the proxy serves works with no change to the
// image. OpenCode deep-merges this over the image's file, so a model the
// image already declares keeps its limit and reasoning flag; one it does
// not gets OpenCode's defaults (no context limit, so no automatic
// compaction; 32000 output tokens).
//
// The tier's effort and options go in the model's options, which OpenCode
// sends on every request (an agent's variant does not take effect, and the
// AI SDK drops keys it does not know without a word); its headers in the
// model's headers, likewise sent on every request.
func openCodeConfig(model, effort string, options map[string]any, headers map[string]string) string {
	provider := llm.Provider(model)
	entry := map[string]any{"options": llm.ModelOptions(model, effort, options)}
	if len(headers) > 0 {
		entry["headers"] = headers
	}
	config := map[string]any{
		"model":    provider + "/" + model,
		"provider": map[string]any{provider: map[string]any{"models": map[string]any{model: entry}}},
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

// egress is a Run's network: the operator's floor (agent.egress), the
// Run's own list (RunEgress), the LLM API's host and dude's tools, each
// rule once, in that order. "*" in either list turns filtering off. With
// nothing listed anywhere and no model's host, filtering is off rather than
// leaving an agent that cannot reach its own model; the tools alone
// restrict nothing. Every entry is one lux takes (LoadAgentConfig refuses
// others in the operator's list, the API in the Run's own); were one not,
// lux would refuse the Run rather than run it with less listed.
func egress(c AgentConfig, own []string) *lux.Network {
	n := &lux.Network{}
	seen := map[lux.EgressRule]bool{}
	add := func(entry string) {
		if r, _ := lux.ParseEgressRule(strings.ToLower(entry)); !seen[r] {
			seen[r] = true
			n.Egress = append(n.Egress, r)
		}
	}
	for _, e := range slices.Concat(c.Egress, own) {
		if e = strings.TrimSpace(e); e == "*" {
			return &lux.Network{Unrestricted: true}
		} else if e != "" {
			add(e)
		}
	}
	if u, err := url.Parse(c.LLMURL); err == nil && u.Hostname() != "" {
		add(u.Hostname())
	}
	if len(n.Egress) == 0 && !c.toolsOnly {
		return &lux.Network{Unrestricted: true}
	}
	if u, err := url.Parse(c.ToolsURL); err == nil && u.Hostname() != "" {
		add(u.Hostname())
	}
	return n
}

// RunEgress is the list a Run of a project gets on top of the operator's:
// its organisation's and its project's (mode "add"), or its project's
// alone (mode "only"), each entry once.
func RunEgress(org, project []string, mode string) []string {
	if mode == "only" {
		org = nil
	}
	var out []string
	for _, e := range slices.Concat(org, project) {
		if !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	return out
}
