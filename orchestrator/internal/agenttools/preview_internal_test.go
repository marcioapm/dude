package agenttools

import (
	"fmt"
	"testing"
)

// A preview answers with at most maxPreviewServers of its servers, the
// first ones, in order.
func TestAPreviewAnswersWithAtMostTwentyServers(t *testing.T) {
	in := Preview{ID: "run_p"}
	for i := range 21 {
		in.Servers = append(in.Servers, PreviewServer{Name: fmt.Sprintf("s%02d", i)})
	}
	out := bounded(in)
	if len(out.Servers) != 20 || out.Servers[0].Name != "s00" || out.Servers[19].Name != "s19" {
		t.Errorf("%d servers in, %d out (%v); want the first 20", len(in.Servers), len(out.Servers), out.Servers)
	}
	if out := bounded(Preview{}); out.Servers == nil || len(out.Servers) != 0 {
		t.Errorf("no servers answered as %#v; want an empty list", out.Servers)
	}
}
