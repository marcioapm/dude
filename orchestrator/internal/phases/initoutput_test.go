package phases

import (
	"encoding/json"
	"strings"
	"runtime"
	"testing"
	"unicode/utf8"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

func initEvent(epoch int, phase string, code int) lux.Frame {
	data, _ := json.Marshal(map[string]any{"phase": phase, "exitCode": code})
	return lux.Frame{Kind: "record", Epoch: epoch, Event: &lux.RecordEvent{Type: "lux.init", Data: data}}
}

func output(epoch int, ch, data string) lux.Frame {
	return lux.Frame{Kind: "record", Epoch: epoch, Channel: ch, Data: data}
}

// One frame of several MiB keeps only its end, cut on a rune.
func TestInitOutputKeepsTheEndOfAHugeFrame(t *testing.T) {
	var o InitOutput
	o.Record(initEvent(1, "start", 0))
	o.Record(output(1, "stdout", "earlier\n"))
	huge := output(1, "stdout", strings.Repeat("é", 3<<20)+"the end")
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	o.Record(huge)
	runtime.ReadMemStats(&after)
	if copied := after.TotalAlloc - before.TotalAlloc; copied > 4*initOutputMax {
		t.Errorf("allocated %d bytes to keep %d", copied, initOutputMax)
	}
	if len(o.Tail) > initOutputMax || !strings.HasSuffix(o.Tail, "the end") || !utf8.ValidString(o.Tail) {
		t.Fatalf("tail of %d bytes, ends %q, valid %v", len(o.Tail), o.Tail[max(0, len(o.Tail)-10):], utf8.ValidString(o.Tail))
	}
	if strings.Contains(o.Tail, "earlier") {
		t.Error("kept what came before a frame larger than the limit")
	}
	o.Record(output(1, "stderr", "é and more"))
	if len(o.Tail) > initOutputMax || !strings.HasSuffix(o.Tail, "the endé and more") || !utf8.ValidString(o.Tail) {
		t.Errorf("tail of %d bytes after a small frame, valid %v", len(o.Tail), utf8.ValidString(o.Tail))
	}
}
