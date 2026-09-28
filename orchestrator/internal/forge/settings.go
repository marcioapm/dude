package forge

import (
	"encoding/json"
	"slices"
	"time"
)

// Settings is how dude behaves on GitHub for an organization
// (forge_credentials.settings). A key left out takes its default, so a
// setting added later needs no migration of what is stored.
type Settings struct {
	// Whose comments may wake a fixer: "collaborators" with write access to
	// the repository, "members" of its GitHub organization, or "anyone" who
	// can comment. Anyone else's are recorded and shown, never acted on: on
	// a public repository, a stranger's comment must not spend the
	// organization's money.
	WhoCanWake string `json:"whoCanWake"`
	// Open pull requests "ready" for review, or as a "draft".
	OpenAs string `json:"openAs"`
	// Who is asked for a review when a pull request opens: "nobody",
	// "codeowners" (GitHub asks them itself), or "logins": ReviewLogins.
	RequestReviewFrom string   `json:"requestReviewFrom"`
	ReviewLogins      []string `json:"reviewLogins"`
	// How dude's Merge button merges: squash, merge or rebase.
	MergeMethod string `json:"mergeMethod"`
	// When main moves ahead of a pull request: "update" its branch when it
	// merges cleanly (else ask), or only "tell" the task's people.
	WhenBehind string `json:"whenBehind"`
	// Fix rounds a pull request may have in all, across review rounds,
	// before its owner decides how it goes on.
	FixRoundsPerPR int `json:"fixRoundsPerPr"`
	// Minutes checks may stay pending on a head before a person is asked.
	CIStuckMinutes int `json:"ciStuckMinutes"`
}

func DefaultSettings() Settings {
	return Settings{
		WhoCanWake:        "collaborators",
		OpenAs:            "ready",
		RequestReviewFrom: "codeowners",
		ReviewLogins:      []string{},
		MergeMethod:       "squash",
		WhenBehind:        "update",
		FixRoundsPerPR:    5,
		CIStuckMinutes:    60,
	}
}

// ReadSettings layers stored settings over the defaults. Values it does
// not know are the defaults: a setting written by a newer dude, or by hand,
// must not stop an older one from working.
func ReadSettings(raw []byte) Settings {
	s := DefaultSettings()
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &s)
	}
	d := DefaultSettings()
	pick := func(v *string, allowed ...string) {
		if !slices.Contains(allowed, *v) {
			*v = allowed[0]
		}
	}
	pick(&s.WhoCanWake, d.WhoCanWake, "members", "anyone")
	pick(&s.OpenAs, d.OpenAs, "draft")
	pick(&s.RequestReviewFrom, d.RequestReviewFrom, "nobody", "logins")
	pick(&s.MergeMethod, MergeMethods...)
	pick(&s.WhenBehind, d.WhenBehind, "tell")
	if s.FixRoundsPerPR < 0 || s.FixRoundsPerPR > 50 {
		s.FixRoundsPerPR = d.FixRoundsPerPR
	}
	if s.CIStuckMinutes <= 0 {
		s.CIStuckMinutes = d.CIStuckMinutes
	}
	if s.ReviewLogins == nil {
		s.ReviewLogins = []string{}
	}
	return s
}

// CIStuck is how long checks may stay pending on a head.
func (s Settings) CIStuck() time.Duration { return time.Duration(s.CIStuckMinutes) * time.Minute }
