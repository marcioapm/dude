package phases

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

func TestGoldenSubmittedNetworkInstructions(t *testing.T) {
	for _, role := range []string{"implementer", delivery.RoleConductor, "investigator"} {
		for _, mode := range []string{"builtin", "add", "replace"} {
			for _, unrestricted := range []bool{false, true} {
				name := role + "-" + mode
				if unrestricted {
					name += "-unrestricted"
				}
				t.Run(name, func(t *testing.T) {
					c, in := goldenInput("real-model")
					c.Egress = []string{"mandatory.example"}
					c.LLMURL = "https://user:secret@model.example/v1?key=secret"
					c.ToolsURL = "https://tools.example/mcp?token=secret"
					in.Role, in.Phase = role, delivery.PhaseForPromptRole(role)
					if role == delivery.RoleConductor {
						in.Phase = ""
					}
					in.Repos = nil
					in.Egress = RunEgress([]string{"org.example"}, []string{"project.example"}, "add")
					if unrestricted {
						in.Egress = []string{"*"}
					}
					instructions := delivery.InstructionsInput{Child: role == "investigator"}
					in.NetworkContext = true
					if mode != "builtin" {
						org := "Inspect carefully."
						instructions.OrgPrompt = &org
						instructions.ProjectPrompt, instructions.ProjectPromptMode = "Report evidence.", mode
					}
					in.Instructions = delivery.Instructions(role, "", instructions)
					spec, err := runSpec(c, in)
					if err != nil {
						t.Fatal(err)
					}
					want := delivery.NetworkInstructions(spec.Network.Unrestricted, networkHosts(spec.Network)) + "\n\n" + in.Instructions
					if got := submittedInstructions(t, spec); got != want {
						t.Fatalf("submitted init differs from resolved network rendering")
					}
					agentGolden(t, "network-"+name, [][2]string{{"Submitted instructions", want}})
				})
			}
		}
	}
}

func TestSubmittedNetworkRulesUseOnlyAddAndMandatoryFloor(t *testing.T) {
	for _, mode := range []string{"only", "add"} {
		c, in := goldenInput("real-model")
		c.Egress, c.LLMURL, c.ToolsURL = []string{"mandatory.example"}, "https://model.example/v1", "https://tools.example/mcp"
		in.Egress = RunEgress([]string{"org.example"}, []string{"project.example"}, mode)
		in.Instructions = "rules\n"
		in.NetworkContext = true
		spec, err := runSpec(c, in)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"mandatory.example"}
		if mode == "add" {
			want = append(want, "org.example")
		}
		want = append(want, "project.example", "model.example", "mcp.exa.ai", "tools.example")
		if !reflect.DeepEqual(networkHosts(spec.Network), want) {
			t.Fatalf("%s: got %v want %v", mode, networkHosts(spec.Network), want)
		}
		if got := submittedInstructions(t, spec); got != delivery.NetworkInstructions(false, want)+"\n\nrules\n" {
			t.Fatalf("%s: init did not describe submitted rules", mode)
		}
	}
}

func submittedInstructions(t *testing.T, spec lux.Spec) string {
	t.Helper()
	if spec.Init == nil {
		t.Fatal("no submitted init")
	}
	_, rest, ok := strings.Cut(spec.Init.Script, "<<'")
	if !ok {
		t.Fatal("no instruction heredoc")
	}
	delimiter, rest, ok := strings.Cut(rest, "' &&\n")
	if !ok {
		t.Fatal("no instruction heredoc body")
	}
	body, _, ok := strings.Cut(rest, "\n"+delimiter+"\n")
	if !ok {
		t.Fatal("no instruction heredoc end")
	}
	return body + "\n"
}

func networkHosts(n *lux.Network) (out []string) {
	for _, r := range n.Egress {
		out = append(out, r.Host+r.CIDR)
	}
	return out
}

func TestProductionNetworkInstructionsAndResumeSnapshot(t *testing.T) {
	for _, role := range []string{"implementer", "conductor", "investigator"} {
		t.Run(role, func(t *testing.T) {
			if err := dbtest.Reachable(context.Background()); err != nil {
				t.Fatal(err)
			}
			w := newResumeWorld(t)
			w.s.Forges = forgeFor(func(context.Context, string) (*forge.GitHub, error) { return nil, nil })
			w.s.Agent.Egress = []string{"floor.example"}
			w.s.Agent.LLMURL = "https://model.example/v1?secret=hidden"
			w.exec(`UPDATE model_tiers SET model='real-model' WHERE organization_id=$1`, w.run.Org)
			w.exec(`UPDATE organizations SET agent_egress='{org.example}' WHERE id=$1`, w.run.Org)
			w.exec(`UPDATE projects SET agent_egress='{project.example}',agent_egress_mode='only' WHERE id=$1`, w.run.ProjectID)
			w.exec(`UPDATE organizations SET default_agent_models=jsonb_build_object($2::text,jsonb_build_object('tier',(SELECT id FROM model_tiers WHERE organization_id=$1 LIMIT 1))) WHERE id=$1`, w.run.Org, role)
			r := w.run
			r.Role, r.Phase = role, delivery.PhaseForPromptRole(role)
			r.LuxRunID = ""
			if role == "conductor" {
				r.Phase = ""
			}
			if role == "investigator" {
				r.SourceKind = "task_conductor"
				w.exec(`UPDATE runs SET source_kind='task_conductor',parent_run_id=id,child_profile='investigator',child_capabilities='{}',child_prompt='Inspect source.' WHERE id=$1`, r.ID)
			}
			w.exec(`UPDATE runs SET phase=NULLIF($2,'')::run_phase,role=$3::agent_role,lux_run_id=NULL WHERE id=$1`, r.ID, r.Phase, role)
			original, _, _, _, err := w.s.spec(w.ctx, r, nil, chosenImage{Ref: "test/image:latest"})
			if err != nil {
				t.Fatal(err)
			}
			text := submittedInstructions(t, original)
			if got, want := networkHosts(original.Network), []string{"floor.example", "project.example", "model.example", "mcp.exa.ai"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("production only/floor resolution: got %v want %v", got, want)
			}
			prefix := delivery.NetworkInstructions(original.Network.Unrestricted, networkHosts(original.Network)) + "\n\n"
			if !strings.HasPrefix(text, prefix) {
				t.Fatal("production init does not describe final submitted network")
			}
			w.exec(`UPDATE projects SET agent_egress='{"*"}',agent_egress_mode='add' WHERE id=$1`, w.run.ProjectID)
			stored := lux.StoredSpec{Image: original.Image}
			resumed, _, _, _, err := w.s.spec(w.ctx, r, &stored, chosenImage{})
			if err != nil {
				t.Fatal(err)
			}
			if resumed.Init != nil {
				t.Fatal("resume replaced original instruction snapshot")
			}
			fresh, _, _, _, err := w.s.spec(w.ctx, r, nil, chosenImage{Ref: "test/image:latest"})
			if err != nil {
				t.Fatal(err)
			}
			if !fresh.Network.Unrestricted || !strings.HasPrefix(submittedInstructions(t, fresh), delivery.NetworkInstructions(true, nil)+"\n\n") {
				t.Fatal("fresh submission did not use current network")
			}
			if submittedInstructions(t, original) != text || original.Network.Unrestricted {
				t.Fatal("original submission mutated")
			}
		})
	}
}
