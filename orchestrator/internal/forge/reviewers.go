package forge

// Who can be asked for a review, as GitHub's own picker offers them: before
// any words, the people GitHub suggests for the pull request (they changed
// these lines, or commented); with words, anyone who can be assigned in the
// repository, and the owner's teams — a GraphQL point or two of the token
// owner's 5,000 an hour.

import (
	"context"
	"strings"
	"sync"
)

// Candidate is someone (or a team) a review can be asked of.
type Candidate struct {
	Kind string `json:"kind"` // user | team
	// A user's login, or a team's "org/slug": what RequestReviewers takes.
	Login     string `json:"login"`
	Name      string `json:"name,omitempty"`
	AvatarURL string `json:"avatarUrl,omitempty"`
	// Why GitHub suggests them: "commented" or "changed" (these files,
	// recently). Empty for one found by words.
	Reason string `json:"reason,omitempty"`
	// A team's size, when GitHub says.
	Members int `json:"members,omitempty"`
}

type gqlUser struct {
	Login     string `json:"login"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatarUrl"`
}

// ReviewerCandidates lists who could be asked to review pull request
// `number` in `slug`: GitHub's suggestions when `words` is empty, else the
// repository's assignable users and the owner's teams matching them. With
// `number` 0 there is no pull request: only a search, for a setting that
// names reviewers for every pull request. The pull request's author is
// never offered.
func (g *GitHub) ReviewerCandidates(ctx context.Context, slug string, number int, words string) ([]Candidate, error) {
	owner, name, _ := strings.Cut(slug, "/")
	words = strings.TrimSpace(words)
	search := words != ""
	if !search && number == 0 {
		return []Candidate{}, nil
	}
	// The teams, beside the people: their own query, so whatever stops
	// GitHub giving them (a user owns the repository, a token without
	// read:org, a team hidden by SSO) is no teams, never no people.
	var teams []Candidate
	var wg sync.WaitGroup
	if search {
		wg.Go(func() { teams = g.teamCandidates(ctx, owner, words) })
	}
	const query = `query($owner:String!,$name:String!,$number:Int!,$pr:Boolean!,$suggest:Boolean!,$search:Boolean!,$q:String!){` +
		`repository(owner:$owner,name:$name){` +
		`pullRequest(number:$number)@include(if:$pr){author{login}` +
		`suggestedReviewers@include(if:$suggest){isCommenter reviewer{login name avatarUrl}}}` +
		`assignableUsers(first:20,query:$q)@include(if:$search){nodes{login name avatarUrl}}}}`
	var data struct {
		Repository *struct {
			PullRequest *struct {
				Author *struct {
					Login string `json:"login"`
				} `json:"author"`
				SuggestedReviewers []struct {
					IsCommenter bool     `json:"isCommenter"`
					Reviewer    *gqlUser `json:"reviewer"`
				} `json:"suggestedReviewers"`
			} `json:"pullRequest"`
			AssignableUsers *struct {
				Nodes []gqlUser `json:"nodes"`
			} `json:"assignableUsers"`
		} `json:"repository"`
	}
	errs, err := g.graphql(ctx, query, map[string]any{
		"owner": owner, "name": name, "number": number, "pr": number > 0, "suggest": !search, "search": search, "q": words,
	}, &data)
	wg.Wait()
	if err != nil {
		return nil, err
	}
	if len(errs) > 0 {
		return nil, &Error{Status: 422, Message: "GitHub would not list reviewers: " + errs[0].Message}
	}
	repo := data.Repository
	if repo == nil {
		return nil, &Error{Status: 404, Message: "GitHub could not find " + slug}
	}
	author := ""
	if pr := repo.PullRequest; pr != nil && pr.Author != nil {
		author = pr.Author.Login
	}
	list, seen := []Candidate{}, map[string]bool{}
	add := func(c Candidate) {
		key := strings.ToLower(c.Login)
		if c.Login == "" || seen[key] || strings.EqualFold(c.Login, author) {
			return
		}
		seen[key] = true
		list = append(list, c)
	}
	if pr := repo.PullRequest; pr != nil {
		for _, s := range pr.SuggestedReviewers {
			if s.Reviewer == nil {
				continue
			}
			reason := "changed"
			if s.IsCommenter {
				reason = "commented"
			}
			add(Candidate{Kind: "user", Login: s.Reviewer.Login, Name: s.Reviewer.Name, AvatarURL: s.Reviewer.AvatarURL, Reason: reason})
		}
	}
	if repo.AssignableUsers != nil {
		for _, u := range repo.AssignableUsers.Nodes {
			add(Candidate{Kind: "user", Login: u.Login, Name: u.Name, AvatarURL: u.AvatarURL})
		}
	}
	for _, t := range teams {
		add(t)
	}
	return list, nil
}

// teamCandidates is the owner's teams matching `words`, as "org/slug";
// none when GitHub will not say, whatever the reason.
func (g *GitHub) teamCandidates(ctx context.Context, owner, words string) []Candidate {
	const query = `query($owner:String!,$q:String!){organization(login:$owner){` +
		`teams(first:20,query:$q){nodes{slug name avatarUrl members{totalCount}}}}}`
	var data struct {
		Organization *struct {
			Teams struct {
				Nodes []*struct {
					Slug      string `json:"slug"`
					Name      string `json:"name"`
					AvatarURL string `json:"avatarUrl"`
					Members   struct {
						TotalCount int `json:"totalCount"`
					} `json:"members"`
				} `json:"nodes"`
			} `json:"teams"`
		} `json:"organization"`
	}
	if _, err := g.graphql(ctx, query, map[string]any{"owner": owner, "q": words}, &data); err != nil || data.Organization == nil {
		return nil
	}
	out := []Candidate{}
	for _, t := range data.Organization.Teams.Nodes {
		// A team GitHub would not show the token comes back null.
		if t != nil && t.Slug != "" {
			out = append(out, Candidate{Kind: "team", Login: owner + "/" + t.Slug, Name: t.Name, AvatarURL: t.AvatarURL, Members: t.Members.TotalCount})
		}
	}
	return out
}

// reviewerSlugs splits what RequestReviewers was given into users and the
// slugs of teams ("org/slug", as Candidate names a team). Only the
// repository owner's teams can review its pull requests: another owner's
// team (one saved in the settings, from another repository) is left out,
// rather than asking a same-named team here or failing the whole request.
func reviewerSlugs(slug string, logins []string) (users, teams []string) {
	owner, _, _ := strings.Cut(slug, "/")
	users, teams = []string{}, []string{}
	for _, l := range logins {
		if org, team, ok := strings.Cut(l, "/"); ok {
			if strings.EqualFold(org, owner) {
				teams = append(teams, team)
			}
		} else {
			users = append(users, l)
		}
	}
	return users, teams
}
