package lux

import (
	"net"
	"regexp"
	"strings"
)

// hostnamePattern is a concrete hostname: dot-separated labels of letters,
// digits, '-' and '_', none starting or ending with '-'.
var hostnamePattern = regexp.MustCompile(`^(?i:[a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?)(\.(?i:[a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?))*\.?$`)

// wildcardDomain is what follows a wildcard's "*.", as lux's hostLabelsRe
// takes it: lowercase labels, at least two (lux#68).
var wildcardDomain = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// ValidWildcard reports whether a host rule is lux's wildcard form:
// "*." then a domain of at least two labels, with no port or path.
func ValidWildcard(rule string) bool {
	domain, ok := strings.CutPrefix(rule, "*.")
	return ok && len(domain) <= 253 && wildcardDomain.MatchString(domain)
}

// ParseEgressRule is one allowed entry other than "*" as lux takes it: an
// address (as a one-address range), a range, a hostname, or a wildcard
// *.<domain> (lowercased). ok is false for anything lux would refuse,
// which would fail the whole Run.
func ParseEgressRule(a string) (EgressRule, bool) {
	if ip := net.ParseIP(a); ip != nil {
		bits := "/128"
		if ip.To4() != nil {
			bits = "/32"
		}
		return EgressRule{CIDR: ip.String() + bits}, true
	}
	if strings.Contains(a, "*") {
		a = strings.ToLower(a)
		return EgressRule{Host: a}, ValidWildcard(a)
	}
	if strings.Contains(a, "/") {
		if _, _, err := net.ParseCIDR(a); err != nil {
			return EgressRule{}, false
		}
		return EgressRule{CIDR: a}, true
	}
	if len(a) > 253 || !hostnamePattern.MatchString(a) {
		return EgressRule{}, false
	}
	return EgressRule{Host: a}, true
}

// Allows reports whether a Run on this network may look name up, as lux's
// resolver decides: unrestricted, the name itself listed, or one or more
// labels under a listed wildcard's domain (never the domain itself).
// Case-insensitive; a trailing dot is ignored.
func (n *Network) Allows(name string) bool {
	if n == nil {
		return false
	}
	if n.Unrestricted {
		return true
	}
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	for _, r := range n.Egress {
		rule := strings.ToLower(r.Host)
		if rule == name && rule != "" ||
			ValidWildcard(rule) && strings.HasSuffix(name, rule[1:]) && len(name) > len(rule)-1 && hostnamePattern.MatchString(name) {
			return true
		}
	}
	return false
}
