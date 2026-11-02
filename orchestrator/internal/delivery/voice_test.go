package delivery

import (
	"slices"
	"testing"
)

// The voice is written from outside the steps (the hand-back, a person's
// message in Chat): a step that is running keeps the row's.
func TestTheVoiceIsOwnedByTheRow(t *testing.T) {
	if def := Workflow(&Store{}, nil); !slices.Contains(def.Owned, "voice") {
		t.Errorf("Owned %v lacks voice", def.Owned)
	}
}

// Quiet is the conductor's alone, and Deliver's rules hold its gate as
// they hold one no conductor decides.
func TestQuietAndWhoseRulesOpen(t *testing.T) {
	for _, c := range []struct {
		decider, voice      string
		quiet, deliverOpens bool
	}{
		{"", "", false, true},
		{DeciderPolicy, VoiceQuiet, false, true},
		{DeciderConductor, "", false, false},
		{DeciderConductor, VoiceChatty, false, false},
		{DeciderConductor, VoiceQuiet, true, true},
	} {
		st := &State{Decider: c.decider, Voice: c.voice}
		if st.quiet() != c.quiet || st.deliverOpens() != c.deliverOpens {
			t.Errorf("%q/%q: quiet %v deliverOpens %v", c.decider, c.voice, st.quiet(), st.deliverOpens())
		}
	}
}
