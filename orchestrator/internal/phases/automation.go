package phases

// An automation's lux Run (internal/automations): an agent, or a container
// running a command or a server. Built here so it shares the phase Runs'
// harness workloads, network, checkouts, setup and credentials; it has no
// task or session; agent tools are scoped to the automation firing.

import (
	"context"
	"maps"
	"slices"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/registry"
)

// AutomationRepo is one repository an automation's Run checks out, read-only.
type AutomationRepo struct {
	Name, URL, Ref, Setup string
}

// AutomationSpecInput is everything an automation Run's spec is built from.
type AutomationSpecInput struct {
	OrganizationID, AutomationID, RunID string
	// "agent", "command" or "server".
	Kind string
	// An agent's role (a preset's) or "agent", and its harness, model,
	// effort and options as its tier names them.
	Role, Harness, Model, ModelTier, Effort string
	Options                                 map[string]any
	Headers                                 map[string]string
	Prompt                                  string
	ToolsToken                              string
	// A command's or server's shell line, and the server's port.
	Command string
	Port    int
	Image   string
	Nested  bool
	Machine *delivery.Machine
	Repos   []AutomationRepo
	// The automation's chosen project secrets and the event's DUDE_* env.
	Secrets []lux.Secret
	Env     map[string]string
	// Files the workload reads (the event as JSON): path → content.
	Files map[string]string
	// Egress beyond the operator's floor.
	Egress []string
	// The no-progress limit, as lux's timeout ("2h"); "" for the default.
	Timeout string
}

// AutomationSpec builds an automation's lux spec. gh gives its checkouts
// their tokens and an App's bot identity (Q5: an organisation's agent acts
// as the App); nil checks out nothing private.
func AutomationSpec(ctx context.Context, c AgentConfig, reg registry.Provider, gh *forge.GitHub, in AutomationSpecInput) (lux.Spec, error) {
	var spec lux.Spec
	labels := map[string]string{lux.AppLabel: lux.App, "dude.org": in.OrganizationID, "dude.kind": "automation",
		"dude.automation": in.AutomationID, "dude.automation_run": in.RunID}
	if in.Kind == "agent" {
		si := automationAgentInput(in)
		if gh != nil && len(si.Repos) > 0 {
			var err error
			if si.ForgeToken, si.RepoTokens, err = forgeTokens(ctx, gh, si.Repos, nil); err != nil {
				return lux.Spec{}, err
			}
			if si.Bot, err = botOf(ctx, gh); err != nil {
				return lux.Spec{}, err
			}
		}
		spec = buildSpec(c, si)
		delete(spec.Labels, "dude.task")
		spec.Name = "automation " + in.AutomationID
		spec.Timeout = in.Timeout
		spec.Workload.BeforeStop = nil
	} else {
		command := []string{"sh", "-c", in.Command}
		if in.Kind == "server" {
			// The container lives as long as dude keeps it; the server is a
			// lux server on it, so lux reports when it is ready.
			command = []string{"sleep", "infinity"}
		}
		spec = lux.Spec{
			Name:     "automation " + in.AutomationID,
			Image:    lux.Image{Ref: in.Image},
			Workload: lux.Workload{Adapter: "generic", Command: command, Workdir: workspaceDir},
			Volumes:  []lux.Volume{{Name: "workspace", Path: workspaceDir, Kind: "state"}},
			Timeout:  in.Timeout,
		}
		if in.Nested {
			spec.Sandbox = &lux.Sandbox{NestedContainers: true}
		}
		MachineSpec(in.Machine, &spec)
		spec.Network = egress(c, in.Egress, nil)
		if len(in.Repos) > 0 {
			spec.Workload.Workdir = RepoPath(in.Repos[0].Name)
			creds := make([]CredentialRepo, 0, len(in.Repos))
			for _, r := range in.Repos {
				creds = append(creds, CredentialRepo{Name: r.Name, URL: r.URL})
			}
			names, tokens, err := RepoCredentials(ctx, gh, creds)
			if err != nil {
				return lux.Spec{}, err
			}
			spec.Git = &lux.Git{}
			var setups []Setup
			for _, r := range in.Repos {
				lr := lux.Repository{Name: lux.SpecName(r.Name), URL: r.URL, Ref: r.Ref, Path: RepoPath(r.Name), Push: new(bool)}
				lr.Credential = names[lr.Name]
				spec.Git.Repositories = append(spec.Git.Repositories, lr)
				setups = append(setups, Setup{Name: lr.Name, Path: RepoPath(r.Name), Script: r.Setup})
			}
			spec.Secrets = append(spec.Secrets, tokens...)
			SetupSpec(&spec, setups)
		}
		if in.Kind == "server" {
			spec.Workload.Servers = []lux.ServerInput{{Name: "automation", Port: in.Port, Command: []string{"sh", "-c", in.Command},
				Workdir: spec.Workload.Workdir}}
		}
	}
	for k, v := range labels {
		if spec.Labels == nil {
			spec.Labels = map[string]string{}
		}
		spec.Labels[k] = v
	}
	if spec.Env == nil {
		spec.Env = map[string]string{}
	}
	for k, v := range in.Env {
		spec.Env[k] = v
	}
	for _, path := range slices.Sorted(maps.Keys(in.Files)) {
		spec.Secrets = append(spec.Secrets, lux.Secret{Name: fileSecretName(path), Value: in.Files[path], As: "file", Path: path})
	}
	spec.Secrets = append(spec.Secrets, in.Secrets...)
	login, err := LoginFor(ctx, reg, in.Image, nil)
	if err != nil {
		return lux.Spec{}, err
	}
	login.Apply(&spec)
	return spec, nil
}

// automationAgentInput is the phase spec input an automation's agent Run
// is built from, with no forge tokens or bot.
func automationAgentInput(in AutomationSpecInput) specInput {
	si := specInput{RunID: in.RunID, OrganizationID: in.OrganizationID, Role: in.Role, Phase: "automation",
		Image: in.Image, NestedContainers: in.Nested, Model: in.Model, ModelTier: in.ModelTier, Effort: in.Effort,
		Options: in.Options, Headers: in.Headers, Harness: in.Harness, Prompt: in.Prompt, ToolsToken: in.ToolsToken, Machine: in.Machine, Egress: in.Egress}
	for _, r := range in.Repos {
		si.Repos = append(si.Repos, specRepo{Name: r.Name, URL: r.URL, Ref: r.Ref, ReadOnly: true, Setup: r.Setup})
	}
	return si
}

// AutomationResumeSecrets supplies current file, project, harness and setup
// values and the stored registry login. ResumeCredentials mints forge tokens
// separately from the stored spec.
func AutomationResumeSecrets(ctx context.Context, c AgentConfig, reg registry.Provider, in AutomationSpecInput, stored lux.StoredSpec) ([]lux.Secret, error) {
	var out []lux.Secret
	for _, path := range slices.Sorted(maps.Keys(in.Files)) {
		out = append(out, lux.Secret{Name: fileSecretName(path), Value: in.Files[path], As: "file", Path: path})
	}
	out = append(out, in.Secrets...)
	if in.Kind == "agent" {
		out = append(out, harnessSecrets(c, automationAgentInput(in))...)
	}
	if DeclaresSetup(stored) {
		setups := make([]Setup, 0, len(in.Repos))
		for _, r := range in.Repos {
			setups = append(setups, Setup{Name: lux.SpecName(r.Name), Path: RepoPath(r.Name), Script: r.Setup})
		}
		out = append(out, SetupSecret(setups))
	}
	login, err := LoginFor(ctx, reg, in.Image, &stored)
	if err != nil {
		return nil, err
	}
	if login != nil {
		out = append(out, lux.Secret{Name: registrySecret, Value: login.Credential})
	}
	return out, nil
}

// fileSecretName names a file the spec places by its path: lux's secret
// names are letters, digits and underscores.
func fileSecretName(path string) string {
	out := []byte("DUDE_FILE_")
	for i := 0; i < len(path); i++ {
		c := path[i]
		switch {
		case c >= 'a' && c <= 'z':
			out = append(out, c-'a'+'A')
		case c >= 'A' && c <= 'Z' || c >= '0' && c <= '9':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}
