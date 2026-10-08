package delivery

import "testing"

// A project's key is 2 to 6 letters or digits starting with a letter
// (PROJECT_KEY, @dude/domain), so a key with digits (PAY2, BILL99, the
// derived WI2) makes task keys TaskKey must read: its prefix is never cut
// at a digit, and only the part after the last dash is the number.
func TestTaskKeysWithADigitBearingProjectKeyParse(t *testing.T) {
	for in, want := range map[string]string{
		"PAY2-1":       "PAY2-1",
		"pay2-007":     "PAY2-7",
		"BILL99-12":    "BILL99-12",
		"A1B2C3-4":     "A1B2C3-4",
		"wi2-3":        "WI2-3",
		" BL-58 ":      "BL-58",
		"Bwor-2":       "BWOR-2",
		"X9-100000000": "X9-100000000",
	} {
		got, ok := TaskKey(in)
		if !ok || got != want {
			t.Errorf("TaskKey(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"2PAY-1", "PAY2", "PAY2-", "-1", "PA Y-1", "PAY-2-1"} {
		if got, ok := TaskKey(in); ok {
			t.Errorf("TaskKey(%q) = %q, want no key", in, got)
		}
	}
}
