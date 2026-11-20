package delivery

import (
	"strings"
	"testing"
	"time"
)

// The commit's time is moved onto the orchestrator's clock by the skew
// measured at the read: a container whose clock is an hour behind dude's
// committed an hour later, on dude's clock, than its committer time says.
// The bound errs early by at most the clocks' whole second.
func TestTheCommitTimeIsMovedOntoTheOrchestratorsClock(t *testing.T) {
	readStart := time.Unix(1_800_000_000, 0)
	for _, c := range []struct {
		name string
		skew time.Duration // dude's clock minus the container's
	}{
		{"the same clock", 0},
		{"the container an hour behind", time.Hour},
		{"the container an hour ahead", -time.Hour},
	} {
		t.Run(c.name, func(t *testing.T) {
			containerNow := readStart.Add(-c.skew).Unix()
			committed := containerNow - 600 // ten minutes before the read, on the container's clock
			got := Checkouts{Now: containerNow, Heads: []CheckoutHead{{Repo: "api", SHA: strings.Repeat("a", 40), Committed: committed}}}.Since(readStart)
			want := readStart.Add(-10*time.Minute - hostSkewMargin)
			if got.After(want) || got.Before(want.Add(-time.Second)) {
				t.Fatalf("since %v, want within a second before %v", got, want)
			}
		})
	}
}

// The bound keeps the host-skew margin: a commit five minutes "ahead" of
// a call, by another host's clock, still bounds below that call.
func TestTheBoundKeepsTheHostSkewMargin(t *testing.T) {
	readStart := time.Unix(1_800_000_000, 0)
	committed := readStart.Unix() + 4*60 // stamped four minutes in dude's future
	got := Checkouts{Now: readStart.Unix(), Heads: []CheckoutHead{{Repo: "api", SHA: strings.Repeat("a", 40), Committed: committed}}}.Since(readStart)
	if !got.Before(readStart) {
		t.Fatalf("since %v, not before %v", got, readStart)
	}
}

// CheckoutScript's account is read whole or not at all.
func TestParseCheckoutsReadsAWholeAccount(t *testing.T) {
	a := strings.Repeat("a", 40)
	repos := []string{"api"}
	if c, ok := ParseCheckouts("now 9\nrepo api "+a+" 5 dirty\n", 0, repos); !ok || c.Now != 9 || c.Clean() || c.Heads[0].Committed != 5 {
		t.Fatalf("%+v %v", c, ok)
	}
	if c, ok := ParseCheckouts("now 9\nmissing api\n", 0, repos); !ok || len(c.Missing) != 1 || len(c.Heads) != 0 {
		t.Fatalf("%+v %v", c, ok)
	}
	for _, bad := range []string{"repo api " + a + " 5 clean\n", "now 9\n", "now 9\nrepo api xyz 5 clean\n", "now 9\nrepo web " + a + " 5 clean\n"} {
		if _, ok := ParseCheckouts(bad, 0, repos); ok {
			t.Errorf("read %q", bad)
		}
	}
}
