package phases

import (
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/capabilities"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// brainstormWeb is what a brainstorm's instruction file says of the web,
// built as brainstormSpec builds it: from the web its spec configures.
func brainstormWeb(harness string, anywhere bool, grants capabilities.Grants) string {
	c, in := goldenInput("claude-sonnet-5")
	everyRole[delivery.RoleBrainstorm](&in)
	in.Harness, in.Anywhere, in.Egress, in.Grants = harness, anywhere, []string{"pypi.org", "*.github.com"}, &grants
	web := runWeb(c, in)
	file := delivery.Instructions(delivery.RoleBrainstorm, "", delivery.InstructionsInput{Tools: true, Grants: &grants, Web: &web})
	_, section, _ := strings.Cut(file, "## The web\n")
	section, _, _ = strings.Cut(section, "\n\n")
	return section
}

// A brainstorm's instruction file says exactly what its harness reaches of
// the web on its network with its grants: search only where it is granted
// and the harness has it on, fetch of any host only when unrestricted, the
// list's hosts when restricted (never the model's, dude's tools' or Exa's),
// no fetch on Codex, which has no fetch tool; and with neither, one plain
// sentence that the session has no web access.
func TestABrainstormIsToldExactlyTheWebItsHarnessHas(t *testing.T) {
	const (
		anyHost  = "You can read the web: search for and fetch public pages when the conversation needs facts from outside the linked repositories. Say where each fact came from, with its URL."
		listed   = "You can search the web. You can fetch pages only from these hosts: pypi.org and *.github.com; a fetch of any other host is refused. Use the web when the conversation needs facts from outside the linked repositories, and say where each fact came from, with its URL."
		noFetch  = "You can search the web. You cannot fetch pages; when you need a page's text, ask the person to paste it. Use the web when the conversation needs facts from outside the linked repositories, and say where each fact came from, with its URL."
		onlyList = "You cannot search the web. You can fetch pages only from these hosts: pypi.org and *.github.com; a fetch of any other host is refused. Use the web when the conversation needs facts from outside the linked repositories, and say where each fact came from, with its URL."
		onlyAny  = "You cannot search the web. You can fetch any public page. Use the web when the conversation needs facts from outside the linked repositories, and say where each fact came from, with its URL."
		none     = "This session has no web access: you cannot search the web or fetch pages. When the conversation needs something from the web, say so and ask the person to paste the text."
	)
	defaults := capabilities.Default(delivery.RoleBrainstorm)
	noSearch := defaults.Without(capabilities.WebSearch)
	for _, c := range []struct {
		harness  string
		anywhere bool
		grants   capabilities.Grants
		want     string
	}{
		{delivery.HarnessOpenCode, true, defaults, anyHost},
		{delivery.HarnessOpenCode, false, defaults, listed},
		{delivery.HarnessOpenCode, false, noSearch, onlyList},
		{delivery.HarnessOpenCode, true, noSearch, onlyAny},
		{delivery.HarnessClaudeCode, true, defaults, anyHost},
		{delivery.HarnessClaudeCode, false, defaults, listed},
		{delivery.HarnessClaudeCode, false, noSearch, onlyList},
		{delivery.HarnessCodex, true, defaults, noFetch},
		{delivery.HarnessCodex, false, defaults, noFetch},
		{delivery.HarnessCodex, false, noSearch, none},
		{delivery.HarnessCodex, true, noSearch, none},
		{delivery.HarnessOpenCode, true, defaults.Without(capabilities.WebSearch, capabilities.WebFetch), none},
	} {
		if got := brainstormWeb(c.harness, c.anywhere, c.grants); got != c.want {
			t.Errorf("%s, anywhere %v, web_search granted %v:\n got %q\nwant %q", c.harness, c.anywhere,
				c.grants.Has(capabilities.WebSearch), got, c.want)
		}
	}
}

// A Codex tier's -c web_search=disabled on a restricted brainstorm turns
// its search off, and the file says it cannot search; unrestricted, dude
// keeps search live over the tier and the file says it can.
func TestACodexTiersSearchOffIsWhatTheBrainstormIsTold(t *testing.T) {
	for _, anywhere := range []bool{false, true} {
		c, in := goldenInput("gpt-6-sol")
		everyRole[delivery.RoleBrainstorm](&in)
		in.Harness, in.Anywhere, in.Egress = delivery.HarnessCodex, anywhere, []string{"pypi.org"}
		in.Options = map[string]any{"args": []any{"-c", "web_search=disabled"}}
		if web := runWeb(c, in); web.Search != anywhere {
			t.Errorf("anywhere %v: search %v", anywhere, web.Search)
		}
	}
}
