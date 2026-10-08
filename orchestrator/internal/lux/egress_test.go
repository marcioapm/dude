package lux

import "testing"

func TestAnEgressEntryIsTheRuleLuxTakes(t *testing.T) {
	for in, want := range map[string]EgressRule{
		"registry.npmjs.org": {Host: "registry.npmjs.org"},
		"10.0.0.5":           {CIDR: "10.0.0.5/32"},
		"2001:db8::1":        {CIDR: "2001:db8::1/128"},
		"192.168.0.0/16":     {CIDR: "192.168.0.0/16"},
		"*.github.com":       {Host: "*.github.com"},
		"*.a.b.example.com":  {Host: "*.a.b.example.com"},
	} {
		if got, ok := ParseEgressRule(in); !ok || got != want {
			t.Errorf("ParseEgressRule(%q) = %+v, %v; want %+v", in, got, ok, want)
		}
	}
	// What lux refuses: a wildcard over a top-level domain or a single
	// label, one not in the first label, one with a port or path; a range
	// that does not parse; not a name at all.
	for _, bad := range []string{"*.com", "*.example", "a.*.com", "*", "*.", "**.example.com", "*.example.com:443",
		"*.example.com/x", "*.-a.com", "10.0.0.0/33", "not a host", ""} {
		if got, ok := ParseEgressRule(bad); ok {
			t.Errorf("ParseEgressRule(%q) = %+v, want refused", bad, got)
		}
	}
}
