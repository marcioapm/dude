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

// PreviewOf identifies a preview's task (or, for a branch preview, its
// branch) and project: key or branch and slug name the label; ids
// distinguish hashed labels.
type PreviewOf struct {
	TaskID, TaskKey, ProjectID, ProjectSlug string
	// A branch preview's branch; "" for a task's preview.
	Branch string
}

// PreviewLabel is the one DNS label a preview server is served at:
// <server>-<task key>-<project slug>, or <server>-<branch>-<project slug>
// for a branch preview, each part normalised. Past 63 characters it is cut
// to 54 and ends in '-' and 8 hex characters of a hash of the server name,
// task id (a branch preview: "branch:" and its branch, which no task id
// starts with) and project id. The ids keep the suffix stable across
// key-prefix renames and distinguish previews with the same key and slug in
// different orgs. A nonempty salt joins the hash and forces the hashed form
// for a retry after lux answers hostname_taken.
func PreviewLabel(server string, of PreviewOf, salt string) string {
	middle, owner := of.TaskKey, of.TaskID
	if of.Branch != "" {
		middle, owner = of.Branch, "branch:"+of.Branch
	}
	var parts []string
	for _, p := range []string{server, middle, of.ProjectSlug} {
		if n := labelPart(p); n != "" {
			parts = append(parts, n)
		}
	}
	label := strings.Join(parts, "-")
	if len(label) <= maxLabel && salt == "" && label != "" {
		return label
	}
	sum := sha256.Sum256([]byte(server + "\x00" + owner + "\x00" + of.ProjectID + "\x00" + salt))
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
func PreviewHostname(domain, server string, of PreviewOf, salt string) string {
	label := PreviewLabel(server, of, salt)
	if domain == "" {
		return label
	}
	return label + "." + domain
}
