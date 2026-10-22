package api

import (
	"net/http"

	"github.com/marciomartins/dude/orchestrator/internal/guide"
)

// helpRoutes serve dude's guide, as this build embeds it, to the web app's
// Help page: the same text search_memory finds as type doc.
func (s *Server) helpRoutes(mux *http.ServeMux) {
	mux.Handle("GET /internal/help", s.auth(func(w http.ResponseWriter, r *http.Request, _ string) error {
		pages := guide.Pages()
		for i := range pages {
			pages[i].Sections = helpSections(pages[i].Sections)
		}
		write(w, http.StatusOK, map[string]any{"pages": pages})
		return nil
	}))
	mux.Handle("GET /internal/help/{page}", s.auth(func(w http.ResponseWriter, r *http.Request, _ string) error {
		p, ok := guide.PageBy(r.PathValue("page"))
		if !ok {
			return fail(http.StatusNotFound, "not_found", "no help page %q", r.PathValue("page"))
		}
		write(w, http.StatusOK, map[string]any{"slug": p.Slug, "title": p.Title, "sections": helpSections(p.Sections),
			"markdown": p.Markdown})
		return nil
	}))
}

// helpSections are the headed sections, for a page's contents: the intro
// has no heading to list.
func helpSections(all []guide.Section) []guide.Section {
	out := []guide.Section{}
	for _, s := range all {
		if s.Heading != "" {
			out = append(out, s)
		}
	}
	return out
}
