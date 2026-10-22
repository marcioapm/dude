package api

import (
	"net/http"

	"github.com/marciomartins/dude/orchestrator/internal/guide"
)

type helpSection struct {
	Anchor  string `json:"anchor"`
	Heading string `json:"heading"`
	// Text is the section's Markdown, on a page's read only.
	Text string `json:"text,omitempty"`
}

type helpPage struct {
	Slug     string        `json:"slug"`
	Title    string        `json:"title"`
	Sections []helpSection `json:"sections"`
}

// helpOf is p for the Help page: its headed sections for a list, and on a
// page's read the intro (anchor "") and every section's text too. The web
// app scrolls by the guide's anchors, which search_memory's links name.
func helpOf(p guide.Page, text bool) helpPage {
	out := helpPage{Slug: p.Slug, Title: p.Title, Sections: []helpSection{}}
	for _, s := range p.Sections {
		if !text && s.Heading == "" {
			continue
		}
		h := helpSection{Anchor: s.Anchor, Heading: s.Heading}
		if text {
			h.Text = s.Text
		}
		out.Sections = append(out.Sections, h)
	}
	return out
}

// helpRoutes serve dude's guide, as this build embeds it, to the web app's
// Help page: the same text search_memory finds as type doc.
func (s *Server) helpRoutes(mux *http.ServeMux) {
	mux.Handle("GET /internal/help", s.auth(func(w http.ResponseWriter, r *http.Request, _ string) error {
		pages := []helpPage{}
		for _, p := range guide.Pages() {
			pages = append(pages, helpOf(p, false))
		}
		write(w, http.StatusOK, map[string]any{"pages": pages})
		return nil
	}))
	mux.Handle("GET /internal/help/{page}", s.auth(func(w http.ResponseWriter, r *http.Request, _ string) error {
		p, ok := guide.PageBy(r.PathValue("page"))
		if !ok {
			return fail(http.StatusNotFound, "not_found", "no help page %q", r.PathValue("page"))
		}
		write(w, http.StatusOK, helpOf(p, true))
		return nil
	}))
}
