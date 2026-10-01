package forge

// Who can be asked for a review, as GitHub's own picker offers them: before
// any words, the people GitHub suggests for the pull request (they changed
// these lines, or commented); with words, anyone who can be assigned in the
// repository, and the owner's teams. One GraphQL query either way — a
// point or two of the token owner's 5,000 an hour.

import (
	"context"
	"strings"
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
	// Already asked on this pull request, and not yet answered.
	Requested bool `json:"requested,omitempty"`
}

// How many a search returns of each kind.
const candidateLimit = 20

// ReviewerCandidates lists who could be asked to review pull request
// `number` in `slug`: GitHub's suggestions when `words` is empty, else the
// repository's assignable users and the owner's teams matching them. With
// `number` 0 there is no pull request: only a search, for a setting that
// names reviewers for every pull request. The pull request's author is
// never offered. A team list GitHub will not give (a user-owned
// repository, a token without read:org) is no teams, not a failure.
func (g *GitHub) ReviewerCandidates(ctx context.Context, slug string, number int, words string) ([]Candidate, error) {
	owner, name, _ := strings.Cut(slug, "/")
	words = strings.TrimSpace(words)
	search := words != ""
	if !search && number == 0 {
		return []Candidate{}, nil
	}
	const query = `query($owner:String!,$name:String!,$number:Int!,$pr:Boolean!,$search:Boolean!,$teams:Boolean!,$q:String!){` +
		`repository(owner:$owner,name:$name){` +
		`pullRequest(number:$number)@include(if:$pr){author{login}` +
		`suggestedReviewers{isAuthor isCommenter reviewer{login name avatarUrl}}` +
		`reviewRequests(first:100){nodes{requestedReviewer{__typename ...on User{login} ...on Team{slug}}}}}` +
		`assignableUsers(first:20,query:$q)@include(if:$search){nodes{login name avatarUrl}}}` +
		`organization(login:$owner)@include(if:$teams){teams(first:20,query:$q){nodes{slug name avatarUrl members{totalCount}}}}}`
	type user struct {
		Login     string `json:"login"`
		Name      string `json:"name"`
		AvatarURL string `json:"avatarUrl"`
	}
	type answer struct {
		Data *struct {
			Repository *struct {
				PullRequest *struct {
					Author *struct {
						Login string `json:"login"`
					} `json:"author"`
					SuggestedReviewers []struct {
						IsAuthor    bool  `json:"isAuthor"`
						IsCommenter bool  `json:"isCommenter"`
						Reviewer    *user `json:"reviewer"`
					} `json:"suggestedReviewers"`
					ReviewRequests struct {
						Nodes []struct {
							RequestedReviewer *struct {
								Typename string `json:"__typename"`
								Login    string `json:"login"`
								Slug     string `json:"slug"`
							} `json:"requestedReviewer"`
						} `json:"nodes"`
					} `json:"reviewRequests"`
				} `json:"pullRequest"`
				AssignableUsers *struct {
					Nodes []user `json:"nodes"`
				} `json:"assignableUsers"`
			} `json:"repository"`
			Organization *struct {
				Teams struct {
					Nodes []struct {
						Slug      string `json:"slug"`
						Name      string `json:"name"`
						AvatarURL string `json:"avatarUrl"`
						Members   struct {
							TotalCount int `json:"totalCount"`
						} `json:"members"`
					} `json:"nodes"`
				} `json:"teams"`
			} `json:"organization"`
		} `json:"data"`
		Errors []struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Path    []any  `json:"path"`
		} `json:"errors"`
	}
	ask := func(teams bool) (answer, error) {
		var out answer
		err := g.doURL(ctx, "POST", g.graphqlURL(), map[string]any{"query": query, "variables": map[string]any{
			"owner": owner, "name": name, "number": number, "pr": number > 0, "search": search, "teams": search && teams, "q": words,
		}}, &out)
		return out, err
	}
	out, err := ask(true)
	if err != nil {
		return nil, err
	}
	// A token without read:org has the whole query refused for its teams,
	// with no path to say so: ask again without them — people, no teams.
	if search && out.Data == nil {
		for _, e := range out.Errors {
			if e.Type == "INSUFFICIENT_SCOPES" {
				if out, err = ask(false); err != nil {
					return nil, err
				}
				break
			}
		}
	}
	// GraphQL answers 200 with "errors": a rate limit is GitHub's to say
	// again later; an error about the organization only (a user owns the
	// repository, or the token cannot read teams) leaves the rest standing.
	for _, e := range out.Errors {
		if e.Type == "RATE_LIMITED" {
			return nil, &Error{Status: 429, Message: "GraphQL: " + e.Message}
		}
		if len(e.Path) == 0 || e.Path[0] != any("organization") {
			return nil, &Error{Status: 422, Message: "GitHub would not list reviewers: " + e.Message}
		}
	}
	if out.Data == nil || out.Data.Repository == nil {
		return nil, &Error{Status: 404, Message: "GitHub could not find " + slug}
	}
	repo := out.Data.Repository
	author, requested := "", map[string]bool{}
	if pr := repo.PullRequest; pr != nil {
		if pr.Author != nil {
			author = pr.Author.Login
		}
		for _, n := range pr.ReviewRequests.Nodes {
			if r := n.RequestedReviewer; r != nil {
				if r.Typename == "Team" {
					requested[strings.ToLower(owner+"/"+r.Slug)] = true
				} else {
					requested[strings.ToLower(r.Login)] = true
				}
			}
		}
	}
	list := []Candidate{}
	add := func(c Candidate) {
		if c.Login == "" || strings.EqualFold(c.Login, author) {
			return
		}
		for _, have := range list {
			if strings.EqualFold(have.Login, c.Login) {
				return
			}
		}
		c.Requested = requested[strings.ToLower(c.Login)]
		list = append(list, c)
	}
	if !search {
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
		return list, nil
	}
	if repo.AssignableUsers != nil {
		for _, u := range repo.AssignableUsers.Nodes {
			add(Candidate{Kind: "user", Login: u.Login, Name: u.Name, AvatarURL: u.AvatarURL})
		}
	}
	if org := out.Data.Organization; org != nil {
		for _, t := range org.Teams.Nodes {
			// A team GitHub would not show the token comes back null: no team.
			if t.Slug == "" {
				continue
			}
			add(Candidate{Kind: "team", Login: owner + "/" + t.Slug, Name: t.Name, AvatarURL: t.AvatarURL, Members: t.Members.TotalCount})
		}
	}
	return list, nil
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
