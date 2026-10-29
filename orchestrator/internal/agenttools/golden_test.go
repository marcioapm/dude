package agenttools

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/marciomartins/dude/orchestrator/internal/capabilities"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// The tool goldens pin what an agent reads of dude's tools: each tool's
// description and input schema, with its parameters' descriptions, as the
// MCP server lists them. They live beside the prompt goldens, in
// delivery/agents/testdata; `go test ./internal/agenttools -run Golden
// -update` rewrites them from what the code says now.
var updateGoldens = flag.Bool("update", false, "rewrite delivery/agents/testdata/*.golden.md from the current code")

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("..", "delivery", "agents", "testdata", name+".golden.md")
	if *updateGoldens {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v (run with -update to create it)", name, err)
	}
	if got != string(want) {
		t.Errorf("%s differs from %s at %s", name, path, firstDifference(string(want), got))
	}
}

// firstDifference names the first line where got leaves want.
func firstDifference(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < max(len(w), len(g)); i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl || i >= len(w) || i >= len(g) {
			return fmt.Sprintf("line %d:\nwant %q\ngot  %q", i+1, wl, gl)
		}
	}
	return "the same lines (trailing bytes differ)"
}

// goldenToolName names a tool's golden: a session agent's tools share
// their names with the task agents', so theirs are tool-session-<name>.
func goldenToolName(t tool) string {
	if t.session {
		return "tool-session-" + t.name
	}
	return "tool-" + t.name
}

func TestGoldenToolDefinitions(t *testing.T) {
	seen := map[string]bool{}
	for _, tl := range allTools() {
		name := goldenToolName(tl)
		if seen[name] {
			t.Fatalf("two tools would write %s", name)
		}
		seen[name] = true
		schema, err := json.MarshalIndent(tl.schema, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		golden(t, name, fmt.Sprintf("# %s\n\n%s\n\n```json\n%s\n```\n", tl.name, tl.description, schema))
	}
}

// Which tools each role is listed, in order, and what the MCP server
// tells every agent of them all.
func TestGoldenToolLists(t *testing.T) {
	var b strings.Builder
	roles := append(append([]string{}, delivery.PromptRoles...), "")
	for _, role := range roles {
		fmt.Fprintf(&b, "%q:", role)
		for _, tl := range granted(role, capabilities.Default(role)) {
			b.WriteString(" " + tl.name)
		}
		b.WriteString("\n")
	}
	fmt.Fprint(&b, "\"brainstorm-from-chat\":")
	chat := capabilities.Of(slices.Concat(capabilities.Defaults[delivery.RoleBrainstorm], capabilities.FromChat[delivery.RoleBrainstorm])...)
	for _, tl := range granted(delivery.RoleBrainstorm, chat) {
		b.WriteString(" " + tl.name)
	}
	b.WriteString("\n")
	golden(t, "tool-lists", b.String())
	golden(t, "tool-server-instructions", serverInstructionsGolden(t))
}

// serverInstructionsGolden is the instructions an MCP client receives on
// initialize, read through a real connection to the server.
func serverInstructionsGolden(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := (&Server{}).serverFor(Caller{Role: "implementer", grants: capabilities.Default("implementer")}).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "golden", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	return cs.InitializeResult().Instructions + "\n"
}

// A parameter description whose parameter is gone stops the server at
// startup, naming the file it is in.
func TestAMissingToolParameterNamesItsFile(t *testing.T) {
	defer func() {
		msg := fmt.Sprint(recover())
		want := "agents/tools/ask_person.md describes actions, which the tool has no parameter called"
		if msg != want {
			t.Errorf("panic %q, want %q", msg, want)
		}
	}()
	schema, err := jsonschema.For[askIn](nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := schema.Properties["actions"]; !ok {
		t.Fatal("askIn has no actions parameter to remove")
	}
	delete(schema.Properties, "actions")
	describeParameters("ask_person", schema)
}

// goldenLines renders named texts as one golden, each under its name.
func goldenLines(t *testing.T, name string, lines [][2]string) {
	t.Helper()
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("## " + l[0] + "\n\n" + l[1] + "\n\n")
	}
	golden(t, name, b.String())
}

// What the tools tell an agent to do next, past a refusal.
func TestGoldenToolResults(t *testing.T) {
	pool := "highmem"
	poolID := "pool_hm"
	xl := delivery.Machine{Name: "XL", CPUs: 8, MemoryMiB: 32768, DiskGiB: 80, PoolID: &poolID, Pool: &pool}
	large := delivery.Machine{Name: "Large", CPUs: 4, MemoryMiB: 16384, DiskGiB: 40}
	none := delivery.MachineChoices{}
	sync := lux.SyncResult{Status: "fetched", To: "aaa111", Behind: 3}
	proposal := proposeOut{ProposalID: "prp_example", Next: proposedNext}
	withoutURL, err := json.Marshal(proposal)
	if err != nil {
		t.Fatal(err)
	}
	proposal.SessionURL = "https://dude.example/#/o/acme/sessions/ssn_example"
	withURL, err := json.Marshal(proposal)
	if err != nil {
		t.Fatal(err)
	}
	goldenLines(t, "results-tools", [][2]string{
		{"ask-person", askNext(false)},
		{"ask-person-escalation", askNext(true)},
		{"session-ask", sessionAskNext("")},
		{"session-ask-to", sessionAskNext("Bo Smith")},
		{"request-repository", requestRepositoryNext("docs", false)},
		{"request-repository-wait", requestRepositoryNext("docs", true)},
		{"wait-for-reply", waitNext("GLD-8")},
		{"wait-for-reply-came", replyCameNext},
		{"steer", steerNext},
		{"restart-run", restartNext},
		{"publish", publishNext},
		{"update-task-relations", relationsWrittenNext},
		{"update-task", taskUpdatedNext},
		{"update-task-present", relationsPresent.Render([]string{"blocks GLD-9", "related GLD-4"})},
		{"propose", proposedNext},
		{"propose-json-without-public-url", string(withoutURL)},
		{"propose-json-with-session-url", string(withURL)},
		{"relate", relationProposedNext},
		{"feedback-resolve", feedbackResolvedNext},
		{"settled-fixed-by", settledAs("resolved", "run_fix", 1, "")},
		{"settled-fixed", settledAs("resolved", "", 1, "")},
		{"settled-accepted", settledAs("accepted", "", 0, "person says fine")},
		{"settled-superseded", settledAs("superseded", "", 0, "")},
		{"settled-open-after", settledAs("open", "", 2, "")},
		{"settled-open", settledAs("open", "", 0, "")},
		{"moved-session", movedAnswer(true, "brainstorm", none, xl)},
		{"moved-implementer", movedAnswer(false, "implementer", none, large)},
		{"moved-conductor", movedAnswer(false, delivery.RoleConductor, none, large)},
		{"moved-reviewer", movedAnswer(false, "reviewer", none, xl)},
		{"moved-unlabelled", movedAnswer(false, "deployer", none, large)},
		{"sync-asked-recorded", baseLine("api", "main", "abc123", lux.SyncResult{}, false).Next},
		{"sync-asked", baseLine("api", "main", "", lux.SyncResult{}, false).Next},
		{"sync-earlier", baseLine("api", "main", "", sync, true).Next},
		{"sync-failed", baseLine("api", "main", "", lux.SyncResult{Status: "failed"}, false).Next},
		{"sync-up-to-date", baseLine("api", "main", "", lux.SyncResult{Status: "up-to-date"}, false).Next},
		{"sync-behind", baseLine("api", "main", "", sync, false).Next},
		{"refresh-checkout-requested", checkoutText("next", struct{ Requested, Pending, Unlinked string }{"WC/web", "", ""})},
		{"refresh-checkout-all", checkoutText("next", struct{ Requested, Pending, Unlinked string }{"WC/web", "WC/api", "WC/docs"})},
		{"refresh-checkout-none", checkoutText("next", struct{ Requested, Pending, Unlinked string }{})},
		{"refresh-checkout-git-refused", cloneFailureReason("git said: ref not found.")},
		{"refresh-checkout-external", checkoutText("external", delivery.ExternalText("abcd1234", "…"))},
	})
}
