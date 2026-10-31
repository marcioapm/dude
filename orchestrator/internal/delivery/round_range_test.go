package delivery

import (
	"reflect"
	"testing"
)

// A conducted loop's later round reviews from the heads the round before
// reviewed, for each repository that moved since. Wherever that range
// could leave a commit unreviewed — a repository the last round had no
// head for — or before any round, or outside a conducted loop, or for an
// integration, the round reviews the whole branch; a loop's own range
// (the simplifier's, a pull request fix's) is never replaced.
func TestARoundsRangeNeverLeavesACommitOutside(t *testing.T) {
	conducted := func(round int, last, heads map[string]string) *State {
		return &State{Decider: DeciderConductor, Round: round, RoundHeads: last, Heads: heads}
	}
	for _, c := range []struct {
		name string
		st   *State
		want *ReviewRange
	}{
		{"round 2 from the last round's head", conducted(1, map[string]string{"api": "a1", "web": "w1"}, map[string]string{"api": "a2", "web": "w1"}),
			&ReviewRange{Of: RangeFixRound, Base: map[string]string{"api": "a1"}}},
		{"a repository the last round did not see", conducted(1, map[string]string{"api": "a1"}, map[string]string{"api": "a2", "web": "w1"}), nil},
		{"nothing moved", conducted(2, map[string]string{"api": "a1"}, map[string]string{"api": "a1"}), nil},
		{"round 1", conducted(0, nil, map[string]string{"api": "a1"}), nil},
		{"deliver", &State{Round: 1, RoundHeads: map[string]string{"api": "a1"}, Heads: map[string]string{"api": "a2"}}, nil},
		{"handed back", &State{HadConductor: true, Round: 1, RoundHeads: map[string]string{"api": "a1"}, Heads: map[string]string{"api": "a2"}},
			&ReviewRange{Of: RangeFixRound, Base: map[string]string{"api": "a1"}}},
		{"an integration", func() *State {
			st := conducted(1, map[string]string{"api": "a1"}, map[string]string{"api": "a2"})
			st.Integration = &Integration{Repo: "api"}
			return st
		}(), nil},
		{"the simplifier's loop", func() *State {
			st := conducted(1, map[string]string{"api": "a1"}, map[string]string{"api": "a2"})
			st.SimplifyBase = map[string]string{"api": "s0"}
			return st
		}(), &ReviewRange{Of: RangeSimplify, Base: map[string]string{"api": "s0"}}},
	} {
		if got := c.st.roundRange(); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}
