package fakelux

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
)

// redact replaces each secret value in s, and its common encodings, with
// [REDACTED:NAME], as lux's shim Redactor (v0.1.11 internal/shim/redact.go)
// does to a server's output before it is written anywhere: every
// occurrence of every form, overlapping or adjacent matches merged into
// one marker, values shorter than 4 bytes left alone.
func redact(values map[string]string, s string) string {
	type pattern struct{ from, to string }
	seen := map[string]bool{}
	var pats []pattern
	add := func(v, name string) {
		if len(v) < 4 || seen[v] {
			return
		}
		seen[v] = true
		pats = append(pats, pattern{v, "[REDACTED:" + name + "]"})
	}
	for name, v := range values {
		b64 := base64.StdEncoding.EncodeToString([]byte(v))
		add(v, name)
		add(b64, name)
		add(strings.TrimRight(b64, "="), name)
		add(base64.URLEncoding.EncodeToString([]byte(v)), name)
		add(url.QueryEscape(v), name)
		add(url.PathEscape(v), name)
		add(hex.EncodeToString([]byte(v)), name)
		quoted, _ := json.Marshal(v)
		add(string(quoted[1:len(quoted)-1]), name)
	}
	sort.Slice(pats, func(i, j int) bool { return len(pats[i].from) > len(pats[j].from) })
	type span struct{ start, end, pat int }
	var spans []span
	for i, p := range pats {
		for at := 0; ; {
			j := strings.Index(s[at:], p.from)
			if j < 0 {
				break
			}
			spans = append(spans, span{at + j, at + j + len(p.from), i})
			at += j + 1
		}
	}
	if len(spans) == 0 {
		return s
	}
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].start != spans[j].start {
			return spans[i].start < spans[j].start
		}
		return spans[i].pat < spans[j].pat
	})
	var b strings.Builder
	last, cur := 0, spans[0]
	flush := func() {
		b.WriteString(s[last:cur.start])
		b.WriteString(pats[cur.pat].to)
		last = cur.end
	}
	for _, sp := range spans[1:] {
		if sp.start <= cur.end {
			cur.end = max(cur.end, sp.end)
			continue
		}
		flush()
		cur = sp
	}
	flush()
	b.WriteString(s[last:])
	return b.String()
}
