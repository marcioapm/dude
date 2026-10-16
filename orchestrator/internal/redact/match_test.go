package redact

import (
	"encoding/base64"
	"math/rand/v2"
	"net/url"
	"slices"
	"sort"
	"strings"
	"testing"
)

// naiveString is the per-pattern redaction the matcher replaced: every
// match of every pattern by strings.Index, sorted by start then pattern,
// touching or overlapping ones merged under the first's name.
func naiveString(r *Redactor, s string) string {
	var spans []span
	for i, p := range r.pats {
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
	sort.Slice(spans, func(i, j int) bool { return spans[i].before(spans[j]) })
	var b strings.Builder
	last, cur := 0, spans[0]
	flush := func() {
		b.WriteString(s[last:cur.start])
		b.WriteString(Marker(r.pats[cur.pat].name))
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

func naiveContains(r *Redactor, s string) (string, bool) {
	best, name := -1, ""
	for _, p := range r.pats {
		if i := strings.Index(s, p.from); i >= 0 && (best < 0 || i < best) {
			best, name = i, p.name
		}
	}
	return name, best >= 0
}

// Over random texts built from overlapping, nested and touching values on
// a small alphabet, the one-pass matcher redacts exactly as matching each
// pattern on its own did. Each round's secrets include one value that is
// a prefix of another, and values cut from another's encoded forms; its
// texts are made of every form FromList builds, with values spliced into
// encoded forms, their own included. The alphabet has characters URL and
// JSON escaping change, so those forms differ from the raw value.
func TestOnePassMatchesEachPatternOnItsOwn(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 11))
	word := func(n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = "abc&<\""[rng.IntN(6)]
		}
		return string(b)
	}
	for round := range 400 {
		first := word(MinLength + rng.IntN(4))
		list := []Secret{{"A", first}, {"B", first + word(1+rng.IntN(4))}}
		for i := range rng.IntN(4) {
			name := string(rune('C' + i))
			switch rng.IntN(3) {
			case 0:
				// A value cut from another's encoded form.
				forms := FromList(slices.Clone(list)).pats
				f := forms[rng.IntN(len(forms))].from
				at := rng.IntN(len(f) - MinLength + 1)
				list = append(list, Secret{name, f[at : at+MinLength+rng.IntN(len(f)-at-MinLength+1)]})
			case 1:
				// A value inside or overlapping another's.
				prev := list[rng.IntN(len(list))].Value
				list = append(list, Secret{name, prev[rng.IntN(len(prev)/2):] + word(rng.IntN(4))})
			default:
				list = append(list, Secret{name, word(MinLength + rng.IntN(6))})
			}
		}
		r := FromList(slices.Clone(list))
		piece := func() string {
			switch rng.IntN(5) {
			case 0, 1:
				return r.pats[rng.IntN(len(r.pats))].from
			case 2:
				// A value spliced into an encoded form, maybe its own.
				f, v := r.pats[rng.IntN(len(r.pats))].from, list[rng.IntN(len(list))].Value
				at := rng.IntN(len(f) + 1)
				return f[:at] + v + f[at:]
			}
			return word(1 + rng.IntN(5))
		}
		for range 20 {
			var b strings.Builder
			for b.Len() < 120 {
				b.WriteString(piece())
			}
			s := b.String()
			if got, want := r.String(s), naiveString(r, s); got != want {
				t.Fatalf("round %d, %q in %q:\n got %q\nwant %q", round, list, s, got, want)
			}
			gn, gok := r.Contains(s)
			wn, wok := naiveContains(r, s)
			if gn != wn || gok != wok {
				t.Fatalf("round %d, %q in %q: Contains = %q %v, want %q %v", round, list, s, gn, gok, wn, wok)
			}
		}
	}
}

// One fixed case per collision class the random test aims at, each checked
// against the expected text and the naive oracle.
func TestOnePassCollisions(t *testing.T) {
	const long = "abcdef&ghi"
	esc := url.QueryEscape(long) // abcdef%26ghi
	b64 := base64.StdEncoding.EncodeToString([]byte(long))
	for _, c := range []struct {
		name     string
		values   map[string]string
		text     string
		want     string
		contains string
	}{
		{"a value a prefix of another", map[string]string{"A": "abcdef", "B": "abcdefghi"},
			"x abcdefghi y abcdefgh z", "x [REDACTED:B] y [REDACTED:A]gh z", "B"},
		{"a value a prefix of another's encoded form", map[string]string{"A": "abcdef", "B": long},
			"x " + esc + " y", "x [REDACTED:B] y", "B"},
		{"a value inside its own encoded form", map[string]string{"B": long},
			b64[:5] + long + b64[5:], b64[:5] + "[REDACTED:B]" + b64[5:], "B"},
		// The automaton's path follows the longer value and leaves it; the
		// shorter ends there, found only through a dictionary link.
		{"a value ending inside another's path", map[string]string{"A": "abcdefgz", "B": "bcdefg"},
			"abcdefgy", "a[REDACTED:B]y", "B"},
		{"touching values", map[string]string{"A": "abcdef", "B": "ghijkl"},
			"-abcdefghijkl-", "-[REDACTED:A]-", "A"},
		{"overlapping values", map[string]string{"A": "abcdefgh", "B": "efghijkl"},
			"-abcdefghijkl-", "-[REDACTED:A]-", "A"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := New(c.values)
			if got := r.String(c.text); got != c.want {
				t.Errorf("String = %q, want %q", got, c.want)
			}
			if got := naiveString(r, c.text); got != c.want {
				t.Errorf("the oracle says %q, want %q", got, c.want)
			}
			if got, ok := r.Contains(c.text); !ok || got != c.contains {
				t.Errorf("Contains = %q %v, want %q", got, ok, c.contains)
			}
		})
	}
}
