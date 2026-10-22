package guide

import (
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

func TestParseSplitsIntroAndSections(t *testing.T) {
	p, err := Parse("delivering", []byte("---\ntitle: Delivering a task\ncovers: [tool:start_phase, settings:delivery]\n---\nIntro one.\n\nIntro two.\n\n## How it starts\nFirst.\n\n```\n## not a heading\n```\n## Rounds & limits (v2)\nSecond.\n"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "Delivering a task" || strings.Join(p.Covers, ",") != "tool:start_phase,settings:delivery" {
		t.Errorf("title %q covers %v", p.Title, p.Covers)
	}
	var got []string
	for _, s := range p.Sections {
		got = append(got, s.Anchor+"="+s.Text)
	}
	want := []string{"=Intro one.\n\nIntro two.", "how-it-starts=First.\n\n```\n## not a heading\n```", "rounds-limits-v2=Second."}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("sections %q, want %q", got, want)
	}
	if s, ok := p.Section("rounds-limits-v2"); !ok || p.SectionTitle(s) != "Delivering a task — Rounds & limits (v2)" {
		t.Errorf("section title %q", p.SectionTitle(s))
	}
	if s, _ := p.Section(""); p.SectionTitle(s) != "Delivering a task" {
		t.Errorf("intro title %q", p.SectionTitle(s))
	}
}

func TestParseWithoutIntroHasNoEmptySection(t *testing.T) {
	p, err := Parse("x", []byte("---\ntitle: X\n---\n## Only\nText\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Sections) != 1 || p.Sections[0].Anchor != "only" {
		t.Errorf("sections %+v", p.Sections)
	}
}

func TestAnchor(t *testing.T) {
	for in, want := range map[string]string{
		"A section heading":     "a-section-heading",
		"  Why -- not?  ":       "why-not",
		"GitHub's PR loop":      "github-s-pr-loop",
		"Ünïcode and 2 numbers": "n-code-and-2-numbers",
	} {
		if got := Anchor(in); got != want {
			t.Errorf("Anchor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseRefuses(t *testing.T) {
	for name, tc := range map[string]struct{ slug, src, want string }{
		"no front matter":     {"a", "# Title\n", "no front matter"},
		"unclosed":            {"a", "---\ntitle: A\n", "not closed"},
		"no title":            {"a", "---\ncovers: [tool:x]\n---\nText", "title is required"},
		"unknown key":         {"a", "---\ntitle: A\nauthor: me\n---\n", "field author not found"},
		"bad covers":          {"a", "---\ntitle: A\ncovers: [x]\n---\n", `covers "x"`},
		"empty covers name":   {"a", "---\ntitle: A\ncovers: [\"tool:\"]\n---\n", `covers "tool:"`},
		"duplicate anchor":    {"a", "---\ntitle: A\n---\n## Same\n1\n## same!\n2\n", `anchor "same"`},
		"heading, no anchor":  {"a", "---\ntitle: A\n---\n## ??\n", "no anchor"},
		"file name not slug":  {"Bad_Name", "---\ntitle: A\n---\n", "not a slug"},
		"yaml does not parse": {"a", "---\ntitle: [\n---\n", "front matter"},
	} {
		if _, err := Parse(tc.slug, []byte(tc.src)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want an error saying %q", name, err, tc.want)
		}
	}
}

func TestLoadFailsOnAnyBadPage(t *testing.T) {
	fsys := fstest.MapFS{
		"pages/a.md": {Data: []byte("---\ntitle: A\n---\nx")},
		"pages/b.md": {Data: []byte("no front matter")},
	}
	if _, err := Load(fsys); err == nil || !strings.Contains(err.Error(), `"b"`) {
		t.Errorf("Load: %v", err)
	}
}

// The built-in pages parse (init would have panicked otherwise) and are
// found by slug; covers index every page that names an id.
func TestBuiltInGuide(t *testing.T) {
	if len(Pages()) == 0 {
		t.Fatal("no pages")
	}
	p, ok := PageBy("settings")
	if !ok || p.Title == "" {
		t.Fatalf("settings page: %v %+v", ok, p)
	}
	if _, ok := PageBy("nope"); ok {
		t.Error("found a page that does not exist")
	}
	if got := Covers()["settings:delivery"]; !slices.Contains(got, "delivery") {
		t.Errorf("covers settings:delivery: %v", got)
	}
}

func TestLink(t *testing.T) {
	if got := Link("https://dude.example/", "acme co", "settings", "delivery"); got != "https://dude.example/#/o/acme%20co/help/settings/delivery" {
		t.Errorf("link %q", got)
	}
	if got := Link("", "acme", "memory", ""); got != "#/o/acme/help/memory" {
		t.Errorf("relative link %q", got)
	}
}
