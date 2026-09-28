package forge

import (
	"encoding/json"
	"fmt"
	"reflect"
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
	// before its owner decides how it goes on; 0, no limit but each
	// round's.
	FixRoundsPerPR int `json:"fixRoundsPerPr"`
	// Minutes checks may stay pending on a head before a person is asked.
	CIStuckMinutes int `json:"ciStuckMinutes"`
}

func DefaultSettings() Settings {
	return Settings{
		WhoCanWake:        WakeCollaborators,
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
	pick(&s.WhoCanWake, WakeCollaborators, WakeMembers, WakeAnyone)
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

// SettingKeys are the settings' names as stored.
func SettingKeys() []string {
	t := reflect.TypeFor[Settings]()
	out := make([]string, t.NumField())
	for i := range out {
		out[i] = t.Field(i).Tag.Get("json")
	}
	return out
}

// ParseSettings reads settings a person is saving: strictly, so a value
// dude does not know is an error rather than, as ReadSettings would have
// it, the default.
func ParseSettings(raw []byte) (Settings, error) {
	var s Settings
	if err := json.Unmarshal(raw, &s); err != nil {
		return Settings{}, fmt.Errorf("settings: %v", err)
	}
	read := ReadSettings(raw)
	for _, c := range []struct {
		name, got, kept string
	}{
		{"whoCanWake", s.WhoCanWake, read.WhoCanWake}, {"openAs", s.OpenAs, read.OpenAs},
		{"requestReviewFrom", s.RequestReviewFrom, read.RequestReviewFrom},
		{"mergeMethod", s.MergeMethod, read.MergeMethod}, {"whenBehind", s.WhenBehind, read.WhenBehind},
	} {
		if c.got != "" && c.got != c.kept {
			return Settings{}, fmt.Errorf("%s cannot be %q", c.name, c.got)
		}
	}
	if s.FixRoundsPerPR < 0 || s.FixRoundsPerPR > 50 {
		return Settings{}, fmt.Errorf("fixRoundsPerPr must be from 0 (no limit) to 50")
	}
	// Absent is the default; given, it is what the person chose, and 0
	// would be read back as the default: refused, not saved as another.
	var given struct {
		CIStuckMinutes *int `json:"ciStuckMinutes"`
	}
	_ = json.Unmarshal(raw, &given)
	if given.CIStuckMinutes != nil && *given.CIStuckMinutes < 1 {
		return Settings{}, fmt.Errorf("ciStuckMinutes must be at least 1")
	}
	return read, nil
}

// CIStuck is how long checks may stay pending on a head.
func (s Settings) CIStuck() time.Duration { return time.Duration(s.CIStuckMinutes) * time.Minute }

// Who may wake a fixer, as Settings.WhoCanWake names it.
const (
	WakeCollaborators = "collaborators"
	WakeMembers       = "members"
	WakeAnyone        = "anyone"
)

// MayWake decides whether a person's comment may wake a fixer, from what
// GitHub says of them: their permission on the repository, and whether
// they are a member of its organization. Members, as a setting, still
// includes collaborators with write access: an outside contractor given
// write access is trusted with the code already.
func MayWake(who, permission string, member bool) bool {
	switch who {
	case WakeAnyone:
		return true
	case WakeMembers:
		return member || CanWrite(permission)
	}
	return CanWrite(permission)
}
