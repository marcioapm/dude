package lux

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// NameRe is lux's rule for the names of a spec's repositories, volumes,
// services and MCP servers (volumeRe in lux's internal/spec/spec.go).
var NameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// SecretNameRe is lux's rule for a secret's name (nameRe there).
var SecretNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,62}$`)

// ServerNameRe is lux's rule for a server's name (serverNameRe there).
var ServerNameRe = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,28}[a-z0-9])?$`)

// nameHashLen is the hex digits of the suffix SpecName adds to a name it
// rewrites.
const nameHashLen = 8

// SpecName is name as lux takes it for a repository: itself when NameRe
// already accepts it; otherwise lowercased, every other character mapped to
// '-', leading '-' and '_' dropped, cut to leave room, and suffixed with
// the first 8 hex digits of name's SHA-256. The suffix is on every rewritten
// name, not only a long one, because lowercasing and mapping are lossy
// ("Web" and "web", "a.b" and "a-b") and a name must stay unique within a
// Run whatever else the Run holds. The suffix does not keep a rewritten
// name off another's unchanged one ("Web" is "web-29751047", a valid name
// itself): a project's repositories are unique by it (migration 094). Stable:
// the SQL function lux_name (migration 093) computes the same, for queries
// over runs.lux_repositories.
func SpecName(name string) string {
	if NameRe.MatchString(name) {
		return name
	}
	var b strings.Builder
	for _, c := range name {
		switch {
		case c >= 'A' && c <= 'Z':
			b.WriteRune(c + 'a' - 'A')
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_', c == '-':
			b.WriteRune(c)
		default:
			b.WriteByte('-')
		}
	}
	base := strings.TrimLeft(b.String(), "-_")
	if keep := 32 - 1 - nameHashLen; len(base) > keep {
		base = base[:keep]
	}
	sum := sha256.Sum256([]byte(name))
	suffix := hex.EncodeToString(sum[:])[:nameHashLen]
	if base == "" {
		return suffix
	}
	return base + "-" + suffix
}
