package delivery

import "testing"

func TestChecksFixRoundsDefaultsToTwoAndEachLayerSetsIt(t *testing.T) {
	p, err := ResolvePolicy(nil, nil)
	if err != nil || p.ChecksRounds() != 2 {
		t.Fatalf("default = %d, %v; want 2", p.ChecksRounds(), err)
	}
	p, _ = ResolvePolicy([]byte(`{"checksFixRounds":1}`), nil)
	if p.ChecksRounds() != 1 {
		t.Errorf("organisation's 1 read as %d", p.ChecksRounds())
	}
	p, _ = ResolvePolicy([]byte(`{"checksFixRounds":1}`), []byte(`{"checksFixRounds":0}`))
	if p.ChecksRounds() != 0 {
		t.Errorf("project's 0 over the organisation's 1 read as %d", p.ChecksRounds())
	}
	for _, stored := range []string{`{"checksFixRounds":-1}`, `{"checksFixRounds":99}`} {
		p, _ = ResolvePolicy([]byte(stored))
		if p.ChecksRounds() != DefaultChecksFixRounds {
			t.Errorf("%s read as %d, want the default", stored, p.ChecksRounds())
		}
	}
}
