package automations

import (
	"encoding/json"
	"errors"
	"testing"
)

func body(t *testing.T, v map[string]any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func pr(action, repo, sender, branch string, labels ...string) map[string]any {
	ls := []any{}
	for _, l := range labels {
		ls = append(ls, map[string]any{"name": l})
	}
	return map[string]any{"action": action, "repository": map[string]any{"full_name": repo}, "sender": map[string]any{"login": sender},
		"pull_request": map[string]any{"number": 7, "head": map[string]any{"ref": branch}, "labels": ls}}
}

func TestReadEventFindsBranchAndLabelsPerEvent(t *testing.T) {
	for _, c := range []struct {
		event  string
		body   map[string]any
		branch string
		labels []string
	}{
		{"pull_request", pr("opened", "acme/web", "ana", "feat/x", "bug"), "feat/x", []string{"bug"}},
		{"pull_request_review", pr("submitted", "acme/web", "ana", "fix/y"), "fix/y", nil},
		{"push", map[string]any{"ref": "refs/heads/main"}, "main", nil},
		{"push", map[string]any{"ref": "refs/tags/v1"}, "", nil},
		{"create", map[string]any{"ref": "release-2", "ref_type": "branch"}, "release-2", nil},
		{"check_suite", map[string]any{"action": "completed", "check_suite": map[string]any{"head_branch": "main"}}, "main", nil},
		{"check_run", map[string]any{"check_run": map[string]any{"check_suite": map[string]any{"head_branch": "dev"}}}, "dev", nil},
		{"workflow_run", map[string]any{"workflow_run": map[string]any{"head_branch": "ci"}}, "ci", nil},
		{"status", map[string]any{"branches": []any{map[string]any{"name": "main"}}}, "main", nil},
		{"issues", map[string]any{"action": "labeled", "issue": map[string]any{"labels": []any{map[string]any{"name": "triage"}}},
			"label": map[string]any{"name": "triage"}}, "", []string{"triage"}},
		{"issue_comment", map[string]any{"issue": map[string]any{"labels": []any{map[string]any{"name": "p1"}}}}, "", []string{"p1"}},
		{"merge_group", map[string]any{"action": "checks_requested"}, "", nil},
	} {
		e := ReadEvent(c.event, body(t, c.body))
		if e.Branch != c.branch || len(e.Labels) != len(c.labels) || (len(c.labels) > 0 && e.Labels[0] != c.labels[0]) {
			t.Errorf("%s: branch %q labels %v, want %q %v", c.event, e.Branch, e.Labels, c.branch, c.labels)
		}
	}
}

func TestMatchFiltersInOrder(t *testing.T) {
	trigger := Trigger{Kind: "github", Event: "pull_request", Actions: []string{"opened", "synchronize"},
		Filters: Filters{Branch: []string{"feat/*", "main"}, Label: []string{"needs-triage"}, Author: []string{"!dependabot[bot]"}}}
	files := func() ([]string, error) { return []string{"apps/web/src/a.ts"}, nil }
	for _, c := range []struct {
		name   string
		event  string
		body   map[string]any
		filter *RepoFilter
		repos  []string
		want   Verdict
	}{
		{"matches", "pull_request", pr("opened", "acme/web", "ana", "feat/x", "needs-triage"), nil, nil, Verdict{Matches: true}},
		{"another event", "issues", pr("opened", "acme/web", "ana", "feat/x"), nil, nil,
			Verdict{Filter: "event", Because: "it is a issues event, not pull_request"}},
		{"action", "pull_request", pr("closed", "acme/web", "ana", "feat/x", "needs-triage"), nil, nil,
			Verdict{Filter: "actions", Because: "its action is closed, not opened or synchronize"}},
		{"org filter, only", "pull_request", pr("opened", "acme/web", "ana", "feat/x", "needs-triage"), &RepoFilter{Mode: "only", Repos: []string{"acme/svc-*"}}, nil,
			Verdict{Filter: "repositories", Because: "acme/web is outside the repositories it covers"}},
		{"org filter, except", "pull_request", pr("opened", "acme/sandbox-1", "ana", "feat/x", "needs-triage"), &RepoFilter{Mode: "except", Repos: []string{"acme/sandbox-*"}}, nil,
			Verdict{Filter: "repositories", Because: "acme/sandbox-1 is outside the repositories it covers"}},
		{"org filter, except lets others in", "pull_request", pr("opened", "acme/web", "ana", "feat/x", "needs-triage"), &RepoFilter{Mode: "except", Repos: []string{"acme/sandbox-*"}}, nil,
			Verdict{Matches: true}},
		{"project's repositories", "pull_request", pr("opened", "other/web", "ana", "feat/x", "needs-triage"), nil, []string{"acme/web"},
			Verdict{Filter: "repositories", Because: "other/web is not one of the project's repositories"}},
		{"branch", "pull_request", pr("opened", "acme/web", "ana", "fix/y", "needs-triage"), nil, nil,
			Verdict{Filter: "branch", Because: "its branch is fix/y"}},
		{"label", "pull_request", pr("opened", "acme/web", "ana", "main"), nil, nil,
			Verdict{Filter: "label", Because: "it has none of the labels needs-triage"}},
		{"author excluded", "pull_request", pr("opened", "acme/web", "dependabot[bot]", "main", "needs-triage"), nil, nil,
			Verdict{Filter: "author", Because: "it is by dependabot[bot], who is excluded"}},
	} {
		got, err := Match(trigger, c.filter, c.repos, ReadEvent(c.event, body(t, c.body)), files)
		if err != nil || got != c.want {
			t.Errorf("%s: got %+v %v, want %+v", c.name, got, err, c.want)
		}
	}
}

func TestMatchAuthorsAndPaths(t *testing.T) {
	ev := ReadEvent("pull_request", body(t, pr("opened", "acme/web", "Rui", "main")))
	only := Trigger{Kind: "github", Event: "pull_request", Filters: Filters{Author: []string{"ana", "rui"}}}
	if v, _ := Match(only, nil, nil, ev, nil); !v.Matches {
		t.Errorf("a listed author (any case) matches: %+v", v)
	}
	other := Trigger{Kind: "github", Event: "pull_request", Filters: Filters{Author: []string{"ana"}}}
	if v, _ := Match(other, nil, nil, ev, nil); v.Matches || v.Because != "it is by Rui, not ana" {
		t.Errorf("an unlisted author does not: %+v", v)
	}

	paths := Trigger{Kind: "github", Event: "pull_request", Filters: Filters{Paths: []string{"docs/", "**/*.sql"}}}
	read := 0
	files := func(list ...string) Files {
		return func() ([]string, error) { read++; return list, nil }
	}
	for _, c := range []struct {
		files []string
		want  bool
	}{
		{[]string{"docs/guide/intro.md"}, true},
		{[]string{"migrations/180_automations.sql"}, true},
		{[]string{"a/b/c/d.sql"}, true},
		{[]string{"apps/web/x.ts", "README.md"}, false},
	} {
		v, err := Match(paths, nil, nil, ev, files(c.files...))
		if err != nil || v.Matches != c.want {
			t.Errorf("%v: %+v %v", c.files, v, err)
		}
	}
	if read != 4 {
		t.Errorf("the files were read %d times, want once per match", read)
	}
	// Paths are read last: an event another filter stops costs no GitHub read.
	read = 0
	stopped := paths
	stopped.Actions = []string{"closed"}
	_, _ = Match(stopped, nil, nil, ev, files("docs/x"))
	if read != 0 {
		t.Errorf("a stopped event's files were read")
	}
	if _, err := Match(paths, nil, nil, ev, func() ([]string, error) { return nil, errors.New("rate limited") }); err == nil {
		t.Error("a failed read of the files is an error, not a skip")
	}
	push := ReadEvent("push", body(t, map[string]any{"ref": "refs/heads/main"}))
	if v, _ := Match(Trigger{Kind: "github", Event: "push", Filters: Filters{Paths: []string{"docs/"}}}, nil, nil, push, nil); v.Matches || v.Filter != "paths" {
		t.Errorf("paths on an event with no pull request: %+v", v)
	}
}

// An event dude has no code for still matches on event, action, repository and author.
func TestMatchAnUnknownEvent(t *testing.T) {
	ev := ReadEvent("deployment_protection_rule", body(t, map[string]any{"action": "requested",
		"repository": map[string]any{"full_name": "acme/web"}, "sender": map[string]any{"login": "ana"}}))
	tr := Trigger{Kind: "github", Event: "deployment_protection_rule", Actions: []string{"requested"}, Filters: Filters{Author: []string{"ana"}}}
	if v, _ := Match(tr, &RepoFilter{Mode: "only", Repos: []string{"acme/*"}}, nil, ev, nil); !v.Matches {
		t.Errorf("got %+v", v)
	}
	tr.Filters.Branch = []string{"main"}
	if v, _ := Match(tr, nil, nil, ev, nil); v.Matches || v.Filter != "branch" {
		t.Errorf("a branch filter on an event with no branch: %+v", v)
	}
}

func TestRepoGlobs(t *testing.T) {
	for _, c := range []struct {
		pattern, name string
		want          bool
	}{
		{"acme/svc-*", "acme/svc-billing", true},
		{"acme/svc-*", "ACME/Svc-Billing", true},
		{"acme/svc-*", "acme/web", false},
		{"*/docs", "other/docs", true},
		{"acme/*", "acme/a/b", false},
		{"acme", "acme/web", false},
	} {
		if RepoGlob(c.pattern, c.name) != c.want {
			t.Errorf("%s ~ %s: want %v", c.pattern, c.name, c.want)
		}
	}
}
