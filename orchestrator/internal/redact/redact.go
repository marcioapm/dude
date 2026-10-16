// Package redact replaces a Run's secret values in text dude stores or
// shows with [REDACTED:NAME].
//
// The forms are those lux's shim Redactor matches (fakelux/redact.go
// mirrors it): the raw value, base64 standard (padded and not) and URL,
// query and path escaping, lower-case hex, and the value as a JSON string
// body with and without HTML escapes. Overlapping or adjacent matches merge
// into one marker. A value shorter than MinLength is left alone: such
// values match too much ordinary text.
//
// Redaction is defence in depth, not a guarantee: an agent that encodes a
// value some other way (reversed, split, compressed) is not caught.
package redact

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
)

// MinLength is the shortest value redacted, in bytes.
const MinLength = 6

// MaxText is the largest text artifact dude reads whole to redact.
const MaxText = 32 << 20

// TextType says whether an artifact of this content type is text, which
// dude redacts; anything else is binary and is served as stored.
func TextType(ctype string) bool {
	mt, _, _ := strings.Cut(strings.ToLower(ctype), ";")
	mt = strings.TrimSpace(mt)
	if strings.HasPrefix(mt, "text/") {
		return true
	}
	switch mt {
	case "application/json", "application/xml", "application/yaml", "application/x-yaml", "application/javascript",
		"application/x-ndjson", "application/sql", "application/toml", "application/x-sh", "image/svg+xml":
		return true
	}
	return strings.HasSuffix(mt, "+json") || strings.HasSuffix(mt, "+xml")
}

// Marker is what replaces a value of the secret name.
func Marker(name string) string { return "[REDACTED:" + name + "]" }

type pattern struct{ from, name string }

// Redactor redacts one Run's values. The zero value and nil redact nothing.
type Redactor struct {
	pats []pattern
	m    *matcher
}

// Secret is one value to redact, under its secret's name. One name may
// carry several values: the one a Run was given and its rotation.
type Secret struct{ Name, Value string }

// New is a Redactor for values, by secret name.
func New(values map[string]string) *Redactor {
	list := make([]Secret, 0, len(values))
	for n, v := range values {
		list = append(list, Secret{n, v})
	}
	return FromList(list)
}

// FromList is a Redactor for list.
func FromList(list []Secret) *Redactor {
	seen := map[string]bool{}
	r := &Redactor{}
	add := func(v, name string) {
		if len(v) < MinLength || seen[v] {
			return
		}
		seen[v] = true
		r.pats = append(r.pats, pattern{v, name})
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	for _, s := range list {
		name, v := s.Name, s.Value
		if len(v) < MinLength {
			continue
		}
		b64 := base64.StdEncoding.EncodeToString([]byte(v))
		add(v, name)
		add(b64, name)
		add(strings.TrimRight(b64, "="), name)
		add(base64.URLEncoding.EncodeToString([]byte(v)), name)
		add(strings.TrimRight(base64.URLEncoding.EncodeToString([]byte(v)), "="), name)
		add(url.QueryEscape(v), name)
		add(url.PathEscape(v), name)
		add(hex.EncodeToString([]byte(v)), name)
		add(jsonBody(v, true), name)
		add(jsonBody(v, false), name)
	}
	// Longest first: at one position the longer form wins.
	sort.SliceStable(r.pats, func(i, j int) bool { return len(r.pats[i].from) > len(r.pats[j].from) })
	if len(r.pats) > 0 {
		r.m = newMatcher(r.pats)
	}
	return r
}

// Empty says whether r redacts nothing.
func (r *Redactor) Empty() bool { return r == nil || len(r.pats) == 0 }

// span is a match of pattern pat at s[start:end], or a run of touching or
// overlapping matches named by the first one's (earliest start, then
// longest) pattern.
type span struct{ start, end, pat int }

// before orders spans by start, then pattern index (longest first).
func (a span) before(b span) bool { return a.start < b.start || a.start == b.start && a.pat < b.pat }

// String is s with every form of every value replaced, in one pass.
func (r *Redactor) String(s string) string {
	if r.Empty() || s == "" {
		return s
	}
	// Matches arrive by end, so each new one can only join the merged runs
	// at the top of the stack: those ending at or after its start.
	var merged []span
	r.m.scan(s, nil, func(sp span) {
		for len(merged) > 0 && merged[len(merged)-1].end >= sp.start {
			top := merged[len(merged)-1]
			merged = merged[:len(merged)-1]
			if top.before(sp) {
				sp.start, sp.pat = top.start, top.pat
			}
			sp.end = max(sp.end, top.end)
		}
		merged = append(merged, sp)
	})
	if len(merged) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, sp := range merged {
		b.WriteString(s[last:sp.start])
		b.WriteString(Marker(r.pats[sp.pat].name))
		last = sp.end
	}
	b.WriteString(s[last:])
	return b.String()
}

// Contains says whether s holds any form of a value, and the first
// secret's name found (by position).
func (r *Redactor) Contains(s string) (string, bool) {
	if r.Empty() {
		return "", false
	}
	best := span{pat: -1}
	limit := len(s)
	r.m.scan(s, &limit, func(sp span) {
		if best.pat < 0 || sp.before(best) {
			best = sp
			// No match ending past here can start at or before best.
			limit = min(limit, best.start+r.m.maxLen)
		}
	})
	if best.pat < 0 {
		return "", false
	}
	return r.pats[best.pat].name, true
}

// Value is v, decoded JSON, with every string in it redacted, as a copy.
// Keys are left alone; JSON catches them in its text pass.
func (r *Redactor) Value(v any) any {
	if r.Empty() {
		return v
	}
	switch x := v.(type) {
	case string:
		return r.String(x)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = r.Value(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = r.Value(e)
		}
		return out
	case []string:
		out := make([]string, len(x))
		for i, e := range x {
			out[i] = r.String(e)
		}
		return out
	case json.RawMessage:
		return r.JSON(x)
	}
	return v
}

// JSON is raw JSON with every string value in it redacted; raw as it was
// when nothing matched, or when it is not JSON (then redacted as text).
func (r *Redactor) JSON(raw json.RawMessage) json.RawMessage {
	if r.Empty() || len(raw) == 0 {
		return raw
	}
	if _, found := r.Contains(string(raw)); !found {
		return raw
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return json.RawMessage(r.String(string(raw)))
	}
	out, err := json.Marshal(r.Value(v))
	if err != nil {
		return json.RawMessage(r.String(string(raw)))
	}
	// A value escaped as JSON inside a string body, or a key holding one,
	// is caught by the text pass over the re-encoded document.
	return json.RawMessage(r.String(string(out)))
}

// jsonBody is v as a JSON string literal without its quotes, with or
// without the HTML escapes json.Marshal adds.
func jsonBody(v string, escapeHTML bool) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(escapeHTML)
	_ = enc.Encode(v)
	return strings.TrimSuffix(strings.TrimPrefix(strings.TrimSuffix(b.String(), "\n"), `"`), `"`)
}
