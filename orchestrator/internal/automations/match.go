package automations

// Matching a GitHub delivery against an automation's trigger. Every event is
// read the same generic way (its action, repository, sender), plus the
// branch and labels of the events that carry them; an event this code has
// never seen still matches on event, action, repository and author.

import (
	"encoding/json"
	"path"
	"slices"
	"strings"
)

// Trigger is an automation's trigger as stored (automations.trigger).
type Trigger struct {
	Kind    string   `json:"kind"`
	Cron    string   `json:"cron,omitempty"`
	TZ      string   `json:"tz,omitempty"`
	Event   string   `json:"event,omitempty"`
	Actions []string `json:"actions,omitempty"`
	Filters Filters  `json:"filters"`
}

// Filters: values within one are OR'd, filters AND'd.
type Filters struct {
	Branch []string `json:"branch,omitempty"`
	Label  []string `json:"label,omitempty"`
	Paths  []string `json:"paths,omitempty"`
	Author []string `json:"author,omitempty"`
}

// RepoFilter is an organisation automation's repositories.
type RepoFilter struct {
	Mode  string   `json:"mode"`
	Repos []string `json:"repos"`
}

// Event is what matching reads of a delivery.
type Event struct {
	Name, Action, Repository, Sender string
	// The branch the event is about, "" when it names none.
	Branch string
	Labels []string
	// A pull request's number, for its files (paths).
	PullNumber int
}

// ReadEvent reads a delivery's body generically.
func ReadEvent(name string, body json.RawMessage) Event {
	var p struct {
		Action     string `json:"action"`
		Ref        string `json:"ref"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		Sender struct {
			Login string `json:"login"`
		} `json:"sender"`
		Number      int `json:"number"`
		PullRequest *struct {
			Number int `json:"number"`
			Head   struct {
				Ref string `json:"ref"`
			} `json:"head"`
			Labels []struct {
				Name string `json:"name"`
			} `json:"labels"`
		} `json:"pull_request"`
		Issue *struct {
			Labels []struct {
				Name string `json:"name"`
			} `json:"labels"`
		} `json:"issue"`
		Label *struct {
			Name string `json:"name"`
		} `json:"label"`
		CheckSuite *struct {
			HeadBranch string `json:"head_branch"`
		} `json:"check_suite"`
		CheckRun *struct {
			CheckSuite struct {
				HeadBranch string `json:"head_branch"`
			} `json:"check_suite"`
		} `json:"check_run"`
		WorkflowRun *struct {
			HeadBranch string `json:"head_branch"`
		} `json:"workflow_run"`
		WorkflowJob *struct {
			HeadBranch string `json:"head_branch"`
		} `json:"workflow_job"`
		Branches []struct {
			Name string `json:"name"`
		} `json:"branches"`
	}
	_ = json.Unmarshal(body, &p)
	e := Event{Name: name, Action: p.Action, Repository: p.Repository.FullName, Sender: p.Sender.Login}
	switch {
	case p.PullRequest != nil:
		e.Branch, e.PullNumber = p.PullRequest.Head.Ref, p.PullRequest.Number
		for _, l := range p.PullRequest.Labels {
			e.Labels = append(e.Labels, l.Name)
		}
	case p.CheckSuite != nil:
		e.Branch = p.CheckSuite.HeadBranch
	case p.CheckRun != nil:
		e.Branch = p.CheckRun.CheckSuite.HeadBranch
	case p.WorkflowRun != nil:
		e.Branch = p.WorkflowRun.HeadBranch
	case p.WorkflowJob != nil:
		e.Branch = p.WorkflowJob.HeadBranch
	case strings.HasPrefix(p.Ref, "refs/heads/"):
		e.Branch = strings.TrimPrefix(p.Ref, "refs/heads/")
	case name == "create" || name == "delete":
		e.Branch = p.Ref
	case len(p.Branches) > 0:
		e.Branch = p.Branches[0].Name
	}
	if p.Issue != nil {
		for _, l := range p.Issue.Labels {
			e.Labels = append(e.Labels, l.Name)
		}
	}
	if p.Label != nil && !slices.Contains(e.Labels, p.Label.Name) {
		e.Labels = append(e.Labels, p.Label.Name)
	}
	return e
}

// Verdict is whether an event fires an automation; when not, the filter
// that stopped it and why, as the test-event dialog says it.
type Verdict struct {
	Matches bool   `json:"matches"`
	Filter  string `json:"filter,omitempty"`
	Because string `json:"because,omitempty"`
}

// Files reads a pull request's changed paths, for the paths filter; nil
// when the event is not about one.
type Files func() ([]string, error)

// Match checks an event against a trigger and, for an organisation
// automation, its repository filter; for a project automation, repos are
// the project's repositories (owner/name) and an event from elsewhere does
// not match. Filters are checked in the dialog's order: actions,
// repositories, branch, label, author, then paths, which alone costs a
// GitHub read.
func Match(t Trigger, filter *RepoFilter, projectRepos []string, e Event, files Files) (Verdict, error) {
	if t.Kind != "github" || t.Event != e.Name {
		return Verdict{Filter: "event", Because: "it is a " + e.Name + " event, not " + t.Event}, nil
	}
	if len(t.Actions) > 0 && !slices.Contains(t.Actions, e.Action) {
		return Verdict{Filter: "actions", Because: "its action is " + quoted(e.Action) + ", not " + strings.Join(t.Actions, " or ")}, nil
	}
	if projectRepos != nil && !slices.ContainsFunc(projectRepos, func(r string) bool { return strings.EqualFold(r, e.Repository) }) {
		return Verdict{Filter: "repositories", Because: quoted(e.Repository) + " is not one of the project's repositories"}, nil
	}
	if !RepoAllowed(filter, e.Repository) {
		return Verdict{Filter: "repositories", Because: quoted(e.Repository) + " is outside the repositories it covers"}, nil
	}
	if len(t.Filters.Branch) > 0 && !slices.ContainsFunc(t.Filters.Branch, func(g string) bool { return globMatch(g, e.Branch) }) {
		return Verdict{Filter: "branch", Because: "its branch is " + quoted(e.Branch)}, nil
	}
	if len(t.Filters.Label) > 0 && !slices.ContainsFunc(t.Filters.Label, func(l string) bool {
		return slices.ContainsFunc(e.Labels, func(have string) bool { return strings.EqualFold(have, l) })
	}) {
		return Verdict{Filter: "label", Because: "it has none of the labels " + strings.Join(t.Filters.Label, ", ")}, nil
	}
	if ok, why := authorAllowed(t.Filters.Author, e.Sender); !ok {
		return Verdict{Filter: "author", Because: why}, nil
	}
	if len(t.Filters.Paths) > 0 {
		if files == nil || e.PullNumber == 0 {
			return Verdict{Filter: "paths", Because: "it is not about a pull request's files"}, nil
		}
		changed, err := files()
		if err != nil {
			return Verdict{}, err
		}
		if !slices.ContainsFunc(changed, func(f string) bool {
			return slices.ContainsFunc(t.Filters.Paths, func(g string) bool { return pathMatch(g, f) })
		}) {
			return Verdict{Filter: "paths", Because: "it changes no file under " + strings.Join(t.Filters.Paths, ", ")}, nil
		}
	}
	return Verdict{Matches: true}, nil
}

func quoted(s string) string {
	if s == "" {
		return "empty"
	}
	return s
}

// authorAllowed: a `!login` excludes that author whatever else is listed;
// plain logins, when any, are the only ones taken.
func authorAllowed(values []string, sender string) (bool, string) {
	var include []string
	for _, v := range values {
		if login, ok := strings.CutPrefix(v, "!"); ok {
			if strings.EqualFold(login, sender) {
				return false, "it is by " + quoted(sender) + ", who is excluded"
			}
			continue
		}
		include = append(include, v)
	}
	if len(include) > 0 && !slices.ContainsFunc(include, func(l string) bool { return strings.EqualFold(l, sender) }) {
		return false, "it is by " + quoted(sender) + ", not " + strings.Join(include, " or ")
	}
	return true, ""
}

// RepoAllowed says an organisation automation's filter covers owner/name;
// nil or "all" covers every repository.
func RepoAllowed(f *RepoFilter, fullName string) bool {
	if f == nil || f.Mode == "all" || f.Mode == "" {
		return true
	}
	named := slices.ContainsFunc(f.Repos, func(p string) bool { return RepoGlob(p, fullName) })
	if f.Mode == "only" {
		return named
	}
	return !named
}

// RepoGlob matches owner/name against a pattern whose `*` matches within a
// segment, ignoring case as GitHub does.
func RepoGlob(pattern, fullName string) bool {
	po, pn, ok1 := strings.Cut(strings.ToLower(pattern), "/")
	fo, fn, ok2 := strings.Cut(strings.ToLower(fullName), "/")
	if !ok1 || !ok2 {
		return false
	}
	a, _ := path.Match(po, fo)
	b, _ := path.Match(pn, fn)
	return a && b
}

// globMatch is a branch glob: `*` within a segment, `**` across them.
func globMatch(pattern, s string) bool {
	if strings.Contains(pattern, "**") {
		return pathMatch(pattern, s)
	}
	ok, _ := path.Match(pattern, s)
	return ok
}

// pathMatch is a paths glob: `*` within a segment, `**` any number of
// segments; a pattern ending in `/` names everything under it.
func pathMatch(pattern, file string) bool {
	if strings.HasSuffix(pattern, "/") {
		pattern += "**"
	}
	return matchSegments(strings.Split(pattern, "/"), strings.Split(file, "/"))
}

func matchSegments(p, f []string) bool {
	for len(p) > 0 {
		if p[0] == "**" {
			if len(p) == 1 {
				return true
			}
			for i := 0; i <= len(f); i++ {
				if matchSegments(p[1:], f[i:]) {
					return true
				}
			}
			return false
		}
		if len(f) == 0 {
			return false
		}
		if ok, _ := path.Match(p[0], f[0]); !ok {
			return false
		}
		p, f = p[1:], f[1:]
	}
	return len(f) == 0
}
