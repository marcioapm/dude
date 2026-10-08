package lux

import (
	"strings"
	"testing"
)

// SpecName leaves a name lux takes alone, and makes any other one lux
// takes, stable, and distinct from the names it could be confused with.
func TestSpecNameIsOneLuxTakes(t *testing.T) {
	for _, keep := range []string{"web", "bl-target", "a_b-c", "0abc", strings.Repeat("a", 32)} {
		if got := SpecName(keep); got != keep {
			t.Errorf("SpecName(%q) = %q, want it unchanged", keep, got)
		}
	}
	names := []string{"BILL-billing-api", "bill-billing-api-x", "WC-Web", "WC-web.app", "WC-web-app", "-x", "_x", ".", "Ü",
		"BILL-" + strings.Repeat("payments-ledger-", 4), strings.Repeat("a", 33), strings.Repeat("a", 34)}
	seen := map[string]string{}
	for _, n := range names {
		got := SpecName(n)
		if !NameRe.MatchString(got) {
			t.Errorf("SpecName(%q) = %q, which lux refuses", n, got)
		}
		if again := SpecName(n); again != got {
			t.Errorf("SpecName(%q) is not stable: %q then %q", n, got, again)
		}
		if SpecName(got) != got {
			t.Errorf("SpecName(%q) = %q is not kept as it is", n, got)
		}
		if other, dup := seen[got]; dup {
			t.Errorf("%q and %q both become %q", n, other, got)
		}
		seen[got] = n
	}
	if got := SpecName("BILL-billing-api"); !strings.HasPrefix(got, "bill-billing-api-") {
		t.Errorf("SpecName keeps nothing readable: %q", got)
	}
	// Mapped the same way, told apart by the suffix, and apart from the name
	// that is already valid.
	if SpecName("WC-Web") == SpecName("wc-web") || SpecName("WC-web.app") == SpecName("WC-web-app") {
		t.Error("lossy mappings collide")
	}
}
