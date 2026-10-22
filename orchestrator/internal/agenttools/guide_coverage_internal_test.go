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

// guidePending are the ids no guide page covers yet, each with why. The
// guide's pages are being written; this list only shrinks: an id here that
// a page covers fails the test, so it is taken off when its page lands.
var guidePending = map[string]string{}

// guideIDs are what the guide must cover: every dude tool a capability
// grants, and every section the settings tool answers.
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

// guideCoverageProblems: an id neither covered nor pending, a covered id
// that names no tool or section, and a pending id that is covered or
// names nothing.
func guideCoverageProblems(ids []string, covers map[string][]string, pending map[string]string) []string {
	var problems []string
	for _, id := range ids {
		_, isPending := pending[id]
		switch pages, covered := covers[id]; {
		case covered && isPending:
			problems = append(problems, fmt.Sprintf("%s is covered by %s: take it off guidePending", id, strings.Join(pages, ", ")))
		case !covered && !isPending:
			problems = append(problems, id+" is in no guide page's covers")
		}
	}
	for id, pages := range covers {
		if !slices.Contains(ids, id) {
			problems = append(problems, fmt.Sprintf("%s (covered by %s) names no tool or settings section", id, strings.Join(pages, ", ")))
		}
	}
	for id := range pending {
		if !slices.Contains(ids, id) {
			problems = append(problems, id+" is pending but names no tool or settings section")
		}
	}
	sort.Strings(problems)
	return problems
}

func TestTheGuideCoversEveryToolAndSettingsSection(t *testing.T) {
	for _, p := range guideCoverageProblems(guideIDs(), guide.Covers(), guidePending) {
		t.Error(p)
	}
	for id, why := range guidePending {
		if strings.TrimSpace(why) == "" {
			t.Errorf("%s is pending with no reason", id)
		}
	}
}

func TestTheGuideCoverageGuardCatches(t *testing.T) {
	ids := []string{"settings:delivery", "tool:a", "tool:b"}
	for name, tc := range map[string]struct {
		covers  map[string][]string
		pending map[string]string
		want    []string
	}{
		"all covered": {map[string][]string{"tool:a": {"p"}, "tool:b": {"p"}, "settings:delivery": {"s"}}, nil, nil},
		"one pending": {map[string][]string{"tool:a": {"p"}, "settings:delivery": {"s"}}, map[string]string{"tool:b": "pending"}, nil},
		"an unlisted tool": {map[string][]string{"tool:a": {"p"}, "settings:delivery": {"s"}}, nil,
			[]string{"tool:b is in no guide page's covers"}},
		"a stale id": {map[string][]string{"tool:a": {"p"}, "tool:b": {"p"}, "settings:delivery": {"s"}, "tool:gone": {"p"}}, nil,
			[]string{"tool:gone (covered by p) names no tool or settings section"}},
		"pending but covered": {map[string][]string{"tool:a": {"p"}, "tool:b": {"q"}, "settings:delivery": {"s"}}, map[string]string{"tool:b": "pending"},
			[]string{"tool:b is covered by q: take it off guidePending"}},
		"pending but nothing": {map[string][]string{"tool:a": {"p"}, "tool:b": {"p"}, "settings:delivery": {"s"}}, map[string]string{"tool:gone": "pending"},
			[]string{"tool:gone is pending but names no tool or settings section"}},
	} {
		if got := guideCoverageProblems(ids, tc.covers, tc.pending); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}
