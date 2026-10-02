package servers

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// maxLabel is a DNS label's limit. A preview's hostname is one label under
// the preview domain: its wildcard certificate covers one level only.
const maxLabel = 63

// labelPart is one part of a preview hostname: lowercase, every character
// outside [a-z0-9] a '-', runs of '-' one, no '-' at either end.
func labelPart(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// PreviewLabel is the one DNS label a preview server is served at:
// <server>-<task>-<project>, each part normalised. Past 63 characters it is
// cut to 54 and ends in '-' and 8 hex characters of a hash of the three
// parts as given, so it stays deterministic and two previews share one only
// on a 32-bit hash collision. salt, when not empty, joins the hash and
// forces the hashed form: a second choice after lux answers hostname_taken.
func PreviewLabel(server, task, project, salt string) string {
	var parts []string
	for _, p := range []string{server, task, project} {
		if n := labelPart(p); n != "" {
			parts = append(parts, n)
		}
	}
	label := strings.Join(parts, "-")
	if len(label) <= maxLabel && salt == "" && label != "" {
		return label
	}
	sum := sha256.Sum256([]byte(server + "\x00" + task + "\x00" + project + "\x00" + salt))
	suffix := hex.EncodeToString(sum[:4])
	keep := maxLabel - 1 - len(suffix)
	if len(label) > keep {
		label = strings.TrimRight(label[:keep], "-")
	}
	if label == "" {
		return "p-" + suffix
	}
	return label + "-" + suffix
}

// PreviewHostname is PreviewLabel under the normalised preview domain,
// or the bare label when lux resolves the domain itself.
func PreviewHostname(domain, server, task, project, salt string) string {
	label := PreviewLabel(server, task, project, salt)
	if domain == "" {
		return label
	}
	return label + "." + domain
}
