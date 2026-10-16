package redact

import (
	"math/rand/v2"
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
// pattern on its own did.
func TestOnePassMatchesEachPatternOnItsOwn(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 11))
	word := func(n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = "abc"[rng.IntN(3)]
		}
		return string(b)
	}
	for round := range 400 {
		var list []Secret
		for i := range 1 + rng.IntN(5) {
			v := word(MinLength + rng.IntN(6))
			if i > 0 && rng.IntN(3) == 0 {
				// A value inside or overlapping another's.
				prev := list[rng.IntN(len(list))].Value
				v = prev[rng.IntN(len(prev)/2):] + word(rng.IntN(4))
			}
			list = append(list, Secret{Name: string(rune('A' + i)), Value: v})
		}
		r := FromList(list)
		for range 20 {
			var b strings.Builder
			for b.Len() < 80 {
				if rng.IntN(3) == 0 {
					b.WriteString(list[rng.IntN(len(list))].Value)
				} else {
					b.WriteString(word(1 + rng.IntN(5)))
				}
			}
			s := b.String()
			if got, want := r.String(s), naiveString(r, s); got != want {
				t.Fatalf("round %d, %v in %q:\n got %q\nwant %q", round, list, s, got, want)
			}
			gn, gok := r.Contains(s)
			wn, wok := naiveContains(r, s)
			if gn != wn || gok != wok {
				t.Fatalf("round %d, %v in %q: Contains = %q %v, want %q %v", round, list, s, gn, gok, wn, wok)
			}
		}
	}
}
