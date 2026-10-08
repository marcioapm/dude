package fakelux

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// specNameProblems is what lux's spec.Normalize refuses among a spec's
// names, in its words: the volumes, repositories, secrets, services, MCP
// servers and servers dude names. A refusal is the whole 422 invalid_spec
// message, "invalid spec: " and every problem joined by "; ", as lux's
// ValidationError reads; "" when every name is one lux takes.
func specNameProblems(raw json.RawMessage) string {
	var spec lux.Spec
	_ = json.Unmarshal(raw, &spec)
	var errs []string
	fail := func(f string, a ...any) { errs = append(errs, fmt.Sprintf(f, a...)) }
	vols := map[string]bool{}
	for i, v := range spec.Volumes {
		if !lux.NameRe.MatchString(v.Name) {
			fail("volumes[%d]: invalid name %q (lowercase, digits, - and _)", i, v.Name)
		}
		if vols[v.Name] {
			fail("volumes: duplicate %q", v.Name)
		}
		vols[v.Name] = true
	}
	if spec.Git != nil {
		repoProblems(spec.Git.Repositories, map[string]bool{}, 0, fail)
	}
	seen := map[string]bool{}
	for i, sec := range spec.Secrets {
		if !lux.SecretNameRe.MatchString(sec.Name) {
			fail("secrets[%d]: invalid name %q", i, sec.Name)
		}
		if seen[sec.Name] {
			fail("secrets: duplicate %q", sec.Name)
		}
		seen[sec.Name] = true
	}
	for i, m := range spec.Workload.MCPServers {
		if !lux.NameRe.MatchString(m.Name) {
			fail("workload.mcpServers[%d]: invalid name %q (lowercase, digits, - and _)", i, m.Name)
		}
	}
	for i, v := range spec.Workload.Services {
		if !lux.NameRe.MatchString(v.Name) {
			fail("workload.services[%d]: invalid name %q (lowercase, digits, - and _)", i, v.Name)
		}
	}
	for i, sv := range spec.Workload.Servers {
		if !lux.ServerNameRe.MatchString(sv.Name) {
			fail("workload.servers[%d]: invalid name %q (1-30 of a-z, 0-9 and -, starting with a letter, not ending in -)", i, sv.Name)
		}
	}
	return invalidSpec(errs)
}

// addedRepoProblems is lux's refusal of a resume's added repositories
// whose names it would not take, or that a Run already holds: lux appends
// them to the stored spec and normalizes it again (AddRepositories).
func addedRepoProblems(raw json.RawMessage, added []lux.Repository) string {
	var spec lux.Spec
	_ = json.Unmarshal(raw, &spec)
	names := map[string]bool{}
	var have []lux.Repository
	if spec.Git != nil {
		have = spec.Git.Repositories
	}
	var errs []string
	fail := func(f string, a ...any) { errs = append(errs, fmt.Sprintf(f, a...)) }
	repoProblems(have, names, 0, func(string, ...any) {})
	repoProblems(added, names, len(have), fail)
	return invalidSpec(errs)
}

func repoProblems(repos []lux.Repository, names map[string]bool, from int, fail func(string, ...any)) {
	for i, r := range repos {
		if !lux.NameRe.MatchString(r.Name) || names[r.Name] {
			fail("git.repositories[%d]: invalid or duplicate name %q", from+i, r.Name)
		}
		names[r.Name] = true
	}
}

func invalidSpec(errs []string) string {
	if len(errs) == 0 {
		return ""
	}
	return "invalid spec: " + strings.Join(errs, "; ")
}
