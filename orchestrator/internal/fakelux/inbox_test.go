package fakelux

import (
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// {{inbox.last}} and {{inbox.from}} name the newest peer message among an
// agent's inputs, as delivery words it; other inputs are passed over, and
// none read gives "".
func TestInboxLastNamesTheNewestPeerMessageRead(t *testing.T) {
	ask := delivery.MessageDirective("ABS-1's implementer", "pm_ask", "", "Which body?")
	reply := delivery.MessageDirective("ABS-2's fixer", "pm_reply", "pm_ask", "{metric}")
	for _, c := range []struct {
		inputs   []string
		id, from string
	}{
		{nil, "", ""},
		{[]string{"A person's steer."}, "", ""},
		{[]string{ask}, "pm_ask", "ABS-1"},
		{[]string{ask, "A person's steer.", reply, "Epic note added: 1 · x"}, "pm_reply", "ABS-2"},
	} {
		if id, from := InboxLast(c.inputs); id != c.id || from != c.from {
			t.Errorf("InboxLast(%q) = %q, %q; want %q, %q", c.inputs, id, from, c.id, c.from)
		}
	}
}
