package agenttools

import (
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

func (s *Server) diffBases(w http.ResponseWriter, r *http.Request) {
	c := r.Context().Value(callerKey{}).(Caller)
	var given map[string][]string
	err := s.DB.InOrg(r.Context(), c.Org, func(tx pgx.Tx) error {
		var attempt int
		if err := tx.QueryRow(r.Context(), `SELECT attempt FROM runs WHERE id = $1`, c.RunID).Scan(&attempt); err != nil {
			return err
		}
		var err error
		given, err = delivery.DiffGivens(r.Context(), tx, c.RunID, c.TaskID, attempt, c.Role == delivery.RoleConductor)
		return err
	})
	if err != nil {
		http.Error(w, "cannot read recorded sync commits", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	for _, name := range slices.Sorted(maps.Keys(given)) {
		fmt.Fprintln(w, name+" "+strings.Join(given[name], " "))
	}
}
