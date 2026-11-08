package phases

import (
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// copyNeeded keeps a failed or aborted writer's copy for the phases that
// publish (delivery.Publishes, through PublishesSQL), not a list of its
// own, and its retention is a whole number of seconds.
func TestCopyNeededTakesTheWritersFromPublishes(t *testing.T) {
	if !strings.Contains(copyNeeded, "r.phase IN ("+delivery.PublishesSQL()+")") || strings.Count(copyNeeded, "r.phase IN (") != 1 {
		t.Errorf("copyNeeded does not take its writer phases from delivery.Publishes: %s", copyNeeded)
	}
	if copyRetentionSQL != "make_interval(secs => 2592000)" || strings.Contains(copyNeeded, "e+") {
		t.Errorf("copyRetentionSQL = %s", copyRetentionSQL)
	}
}
