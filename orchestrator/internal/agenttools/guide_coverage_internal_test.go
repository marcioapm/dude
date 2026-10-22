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
var guidePending = map[string]string{
	"tool:ask_person":            "guide page pending",
	"tool:create_task":           "guide page pending",
	"tool:decide":                "guide page pending",
	"tool:decide_escalation":     "guide page pending",
	"tool:dismiss_finding":       "guide page pending",
	"tool:emit_event":            "guide page pending",
	"tool:feedback":              "guide page pending",
	"tool:feedback_reply":        "guide page pending",
	"tool:feedback_resolve":      "guide page pending",
	"tool:findings":              "guide page pending",
	"tool:github_check_log":      "guide page pending",
	"tool:github_issue":          "guide page pending",
	"tool:github_pull_request":   "guide page pending",
	"tool:github_search":         "guide page pending",
	"tool:limits":                "guide page pending",
	"tool:list_epics":            "guide page pending",
	"tool:list_machine_sizes":    "guide page pending",
	"tool:list_repositories":     "guide page pending",
	"tool:list_tasks":            "guide page pending",
	"tool:mark_ready":            "guide page pending",
	"tool:message":               "guide page pending",
	"tool:name_session":          "guide page pending",
	"tool:note":                  "guide page pending",
	"tool:propose":               "guide page pending",
	"tool:publish":               "guide page pending",
	"tool:pull_requests":         "guide page pending",
	"tool:refresh_checkout":      "guide page pending",
	"tool:refresh_pull_requests": "guide page pending",
	"tool:relate":                "guide page pending",
	"tool:reply_on_pull_request": "guide page pending",
	"tool:request_limit":         "guide page pending",
	"tool:request_machine":       "guide page pending",
	"tool:request_repository":    "guide page pending",
	"tool:request_review":        "guide page pending",
	"tool:rerun_failed_checks":   "guide page pending",
	"tool:resolve_thread":        "guide page pending",
	"tool:restart_run":           "guide page pending",
	"tool:run_chat":              "guide page pending",
	"tool:run_diff":              "guide page pending",
	"tool:start_agent":           "guide page pending",
	"tool:start_phase":           "guide page pending",
	"tool:steer":                 "guide page pending",
	"tool:sync_base":             "guide page pending",
	"tool:update_task":           "guide page pending",
	"tool:verify":                "guide page pending",
	"tool:wait_for_reply":        "guide page pending",
}

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
