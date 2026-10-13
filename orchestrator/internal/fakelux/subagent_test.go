package fakelux

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The fake lux plays lux-fake's subagent lines as a real lux relays them:
// every record lux wrote for lux-fake's `subagent` and `subagent ask`
// under its opencode adapter (phases/testdata/luxfake-subagent*.jsonl),
// field for field, ids included, with only times and the parent's
// session and message ids, which differ per Run, set aside.
func TestTheFakeLuxPlaysLuxFakesSubagentAsRealLuxRelaysIt(t *testing.T) {
	for line, file := range map[string]string{"subagent": "luxfake-subagent.jsonl", "subagent ask": "luxfake-subagent-ask.jsonl"} {
		t.Run(line, func(t *testing.T) {
			fake := New("", "k", func(map[string]any) Behaviour { return Behaviour{Hang: true, Subagent: line} })
			t.Cleanup(fake.Close)
			_, run := submitAdapter(t, fake, "opencode")
			var got []map[string]any
			for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
				got = relayed(fake.Records(run.ID))
				if slices.ContainsFunc(got, func(r map[string]any) bool {
					return r["type"] == "opencode.task" && r["data"].(map[string]any)["status"] == "completed"
				}) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("the subagent never ended: %v", got)
				}
			}
			want := relayed(luxRecords(t, file))
			if g, w := comparable(t, got), comparable(t, want); !slices.Equal(g, w) {
				t.Errorf("fake lux:\n%s\nreal lux:\n%s", strings.Join(g, "\n"), strings.Join(w, "\n"))
			}
		})
	}
}

// relayed are the records lux writes of OpenCode's bus, with its warnings.
func relayed(recs []map[string]any) []map[string]any {
	var out []map[string]any
	for _, r := range recs {
		if typ, _ := r["type"].(string); strings.HasPrefix(typ, "opencode.") || typ == "lux.warning" {
			out = append(out, r)
		}
	}
	return out
}

func luxRecords(t *testing.T, name string) []map[string]any {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "phases", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []map[string]any
	for sc := bufio.NewScanner(f); sc.Scan(); {
		var r map[string]any
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// comparable is each record as JSON with what varies per Run replaced:
// times by 0, the parent session's id by $P and the child's message id by
// $M, each taken from the first record that carries it.
func comparable(t *testing.T, recs []map[string]any) []string {
	t.Helper()
	var parent, msg string
	var out []string
	for _, r := range recs {
		d := r["data"].(map[string]any)
		switch r["type"] {
		case "opencode.session":
			parent = cmpOr(parent, d["parentID"])
			d["createdAt"] = 0
		case "opencode.part":
			msg = cmpOr(msg, d["messageID"])
			d["time"] = 0
		case "opencode.message":
			d["completed"] = 0
		}
		b, _ := json.Marshal(r)
		out = append(out, string(b))
	}
	if parent == "" || msg == "" {
		t.Fatalf("no session or part in %v", recs)
	}
	for i := range out {
		// The message id first: the fake's carries the parent's.
		out[i] = strings.ReplaceAll(strings.ReplaceAll(out[i], msg, "$M"), parent, "$P")
	}
	return out
}

func cmpOr(have string, v any) string {
	if have != "" {
		return have
	}
	s, _ := v.(string)
	return s
}
