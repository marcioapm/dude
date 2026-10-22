package agenttools

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/capabilities"
	"github.com/marciomartins/dude/orchestrator/internal/guide"
)

// guideIDs are every tool granted by a capability and every settings section.
func guideIDs() []string {
	var out []string
	for _, c := range capabilities.All {
		for _, t := range c.Tools {
			out = append(out, "tool:"+t)
		}
	}
	for _, s := range settingsSections {
		out = append(out, "settings:"+s)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func guideCoverageProblems(ids []string, covers map[string][]string) []string {
	var problems []string
	for _, id := range ids {
		if len(covers[id]) == 0 {
			problems = append(problems, id+" is in no guide page's covers")
		}
	}
	for id, pages := range covers {
		if !slices.Contains(ids, id) {
			problems = append(problems, fmt.Sprintf("%s (covered by %s) names no tool or settings section", id, strings.Join(pages, ", ")))
		}
	}
	sort.Strings(problems)
	return problems
}

func TestTheGuideCoversEveryToolAndSettingsSection(t *testing.T) {
	for _, p := range guideCoverageProblems(guideIDs(), guide.Covers()) {
		t.Error(p)
	}
}

func TestTheGuideCoverageGuardCatches(t *testing.T) {
	ids := []string{"settings:delivery", "tool:a", "tool:b"}
	for name, tc := range map[string]struct {
		covers map[string][]string
		want   []string
	}{
		"all covered": {map[string][]string{"tool:a": {"p"}, "tool:b": {"p"}, "settings:delivery": {"s"}}, nil},
		"an unlisted tool": {map[string][]string{"tool:a": {"p"}, "settings:delivery": {"s"}},
			[]string{"tool:b is in no guide page's covers"}},
		"a stale id": {map[string][]string{"tool:a": {"p"}, "tool:b": {"p"}, "settings:delivery": {"s"}, "tool:gone": {"p"}},
			[]string{"tool:gone (covered by p) names no tool or settings section"}},
	} {
		if got := guideCoverageProblems(ids, tc.covers); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}
