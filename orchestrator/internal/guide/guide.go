// Package guide is dude's own documentation, built into each orchestrator:
// the pages under pages/, parsed when the process starts. Agents search it
// through search_memory (type doc, synced into guide_sections by Sync) and
// people read it on the web app's Help page.
//
// A page is Markdown with front matter:
//
//	---
//	title: Delivering a task
//	covers: [tool:start_phase, settings:delivery]
//	---
//	Intro paragraph(s).
//
//	## A section heading
//
// Each `## ` heading starts a section whose anchor is the heading slugified;
// the text before the first one is the page's intro (anchor "").
package guide

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"net/url"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

//go:embed pages/*.md
var embedded embed.FS

// Section is one part of a page: its intro (Anchor and Heading "") or one
// `## ` heading and the text under it.
type Section struct {
	Anchor  string `json:"anchor"`
	Heading string `json:"heading"`
	Text    string `json:"-"`
}

// Page is one parsed guide page.
type Page struct {
	Slug     string    `json:"slug"`
	Title    string    `json:"title"`
	Covers   []string  `json:"-"`
	Sections []Section `json:"sections"`
	// Markdown is the page without its front matter, as people read it.
	Markdown string `json:"-"`
}

// Section is the page's section at anchor.
func (p Page) Section(anchor string) (Section, bool) {
	for _, s := range p.Sections {
		if s.Anchor == anchor {
			return s, true
		}
	}
	return Section{}, false
}

// SectionTitle is how a found section is titled: the page's title, then
// the section's heading unless it is the intro.
func (p Page) SectionTitle(s Section) string {
	if s.Heading == "" {
		return p.Title
	}
	return p.Title + " — " + s.Heading
}

type frontMatter struct {
	Title  string   `yaml:"title"`
	Covers []string `yaml:"covers"`
}

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Anchor is heading's anchor: lowercase, every run of characters other
// than a-z and 0-9 one "-", none at either end.
func Anchor(heading string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(heading) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
			continue
		}
		dash = true
	}
	return b.String()
}

// Parse reads one page's source; slug is its file name without .md.
func Parse(slug string, src []byte) (Page, error) {
	p := Page{Slug: slug}
	if !slugPattern.MatchString(slug) {
		return p, fmt.Errorf("guide page %q: the file name is not a slug (a-z, 0-9, single dashes)", slug)
	}
	text := strings.ReplaceAll(string(src), "\r\n", "\n")
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return p, fmt.Errorf("guide page %q: no front matter (a first line ---)", slug)
	}
	head, body, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		if head, ok = strings.CutSuffix(rest, "\n---"); !ok {
			return p, fmt.Errorf("guide page %q: front matter not closed by a line ---", slug)
		}
	}
	var fm frontMatter
	dec := yaml.NewDecoder(bytes.NewReader([]byte(head)))
	dec.KnownFields(true)
	if err := dec.Decode(&fm); err != nil {
		return p, fmt.Errorf("guide page %q: front matter: %w", slug, err)
	}
	p.Title = strings.TrimSpace(fm.Title)
	if p.Title == "" {
		return p, fmt.Errorf("guide page %q: title is required", slug)
	}
	for _, c := range fm.Covers {
		kind, name, _ := strings.Cut(c, ":")
		if (kind != "tool" && kind != "settings") || name == "" {
			return p, fmt.Errorf("guide page %q: covers %q is not tool:<name> or settings:<section>", slug, c)
		}
	}
	p.Covers = fm.Covers
	p.Markdown = strings.TrimSpace(body)

	cur := Section{}
	var lines []string
	flush := func() {
		cur.Text = strings.TrimSpace(strings.Join(lines, "\n"))
		if cur.Heading != "" || cur.Text != "" {
			p.Sections = append(p.Sections, cur)
		}
		lines = nil
	}
	fenced := false
	for _, line := range strings.Split(p.Markdown, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
		}
		if heading, ok := strings.CutPrefix(line, "## "); ok && !fenced {
			flush()
			heading = strings.TrimSpace(heading)
			cur = Section{Heading: heading, Anchor: Anchor(heading)}
			if cur.Anchor == "" {
				return p, fmt.Errorf("guide page %q: heading %q has no anchor", slug, heading)
			}
			if _, dup := p.Section(cur.Anchor); dup {
				return p, fmt.Errorf("guide page %q: two sections have the anchor %q", slug, cur.Anchor)
			}
			continue
		}
		lines = append(lines, line)
	}
	flush()
	return p, nil
}

// Load parses every pages/*.md in fsys, ordered by slug.
func Load(fsys fs.FS) ([]Page, error) {
	names, err := fs.Glob(fsys, "pages/*.md")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	var out []Page
	for _, name := range names {
		src, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		p, err := Parse(strings.TrimSuffix(path.Base(name), ".md"), src)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// pages is the built-in guide. A page that does not parse stops the
// process at start, and fails `go test ./internal/guide`.
var pages = func() []Page {
	p, err := Load(embedded)
	if err != nil {
		panic(err)
	}
	return p
}()

// Pages is the built-in guide, ordered by slug.
func Pages() []Page { return slices.Clone(pages) }

// PageBy is the built-in page slug.
func PageBy(slug string) (Page, bool) {
	for _, p := range pages {
		if p.Slug == slug {
			return p, true
		}
	}
	return Page{}, false
}

// Covers is which pages cover each id (tool:<name>, settings:<section>).
func Covers() map[string][]string {
	return CoversOf(pages)
}

// CoversOf is Covers over the given pages.
func CoversOf(ps []Page) map[string][]string {
	out := map[string][]string{}
	for _, p := range ps {
		for _, c := range p.Covers {
			out[c] = append(out[c], p.Slug)
		}
	}
	return out
}

// HashPath is the web app's place of a page or section in organization
// org: `#/o/<org>/help/<page>[/<anchor>]` (apps/web/src/place.ts).
func HashPath(org, page, anchor string) string {
	p := "#/o/" + url.PathEscape(org) + "/help/" + url.PathEscape(page)
	if anchor != "" {
		p += "/" + url.PathEscape(anchor)
	}
	return p
}

// Link is a page or section's link: base (dude's public origin) then its
// hash path, or the hash path alone when there is no base.
func Link(base, org, page, anchor string) string {
	if base == "" {
		return HashPath(org, page, anchor)
	}
	return strings.TrimSuffix(base, "/") + "/" + HashPath(org, page, anchor)
}
