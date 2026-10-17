package agenttools

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
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
	if len(t.roles) == 1 && t.roles[0] == delivery.RoleBrainstorm {
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
		for _, tl := range allTools() {
			if tl.allowed(role) {
				b.WriteString(" " + tl.name)
			}
		}
		b.WriteString("\n")
	}
	golden(t, "tool-lists", b.String())
	golden(t, "tool-server-instructions", serverInstructions(t))
}

// serverInstructions is what the MCP server tells a client on initialize.
func serverInstructions(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := (&Server{}).serverFor(Caller{Role: "implementer"}).Connect(ctx, st, nil)
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
