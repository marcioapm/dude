package agenttools

import (
	"context"
	"encoding/json"
	"maps"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// eventsLux answers Events with frames after an event id, as one page.
type eventsLux struct {
	lux.Client
	frames []lux.Frame
}

func (l *eventsLux) Events(_ context.Context, _ string, after int64) ([]lux.Frame, error) {
	var out []lux.Frame
	for _, f := range l.frames {
		if f.EventID > after {
			out = append(out, f)
		}
	}
	return out, nil
}

// syncReport is a git.sync frame of lux's.
func syncReport(id int64, res lux.SyncResult) lux.Frame {
	data, _ := json.Marshal(res)
	return lux.Frame{Kind: "lux", EventID: id, EventType: "git.sync", EventData: data}
}

var targetMain = []lux.SyncRef{{Repo: "target", Ref: "main", Mode: lux.SyncFetch}}

// Reports that are not an earlier landed fetch of this Run's base: none
// is counted, nor masks one that is. want is lux's To by repository.
func TestLandedEarlierCountsOnlyThisRunsLandedFetchOfTheBase(t *testing.T) {
	landed := lux.SyncResult{Repo: "target", Ref: "main", RequestID: "base-r1-1", Mode: "fetch", Status: "fetched",
		To: "aaa111", Behind: 1}
	cases := []struct {
		name   string
		frames []lux.Frame
		refs   []lux.SyncRef
		want   map[string]string
	}{
		{"another repository's report", []lux.Frame{
			syncReport(1, lux.SyncResult{Repo: "web", Ref: "main", RequestID: "base-r1-1", Status: "fetched", To: "bbb222"}),
			// No ref: only the repository tells it apart.
			syncReport(2, lux.SyncResult{Repo: "web", RequestID: "base-r1-1", Status: "fetched", To: "bbb333"}),
		}, targetMain, map[string]string{}},
		{"another ref's report: the base changed between calls", []lux.Frame{
			syncReport(1, landed),
		}, []lux.SyncRef{{Repo: "target", Ref: "release", Mode: lux.SyncFetch}}, map[string]string{}},
		{"a failed sync after a landed one", []lux.Frame{
			syncReport(1, landed),
			syncReport(2, lux.SyncResult{Repo: "target", Ref: "main", RequestID: "base-r1-2", Status: "failed", To: "fff000"}),
		}, targetMain, map[string]string{"target": "aaa111"}},
		{"another Run's, or a checkout sync's, request", []lux.Frame{
			syncReport(1, lux.SyncResult{Repo: "target", Ref: "main", RequestID: "sync-r1-1", Status: "fetched", To: "ccc333"}),
			syncReport(2, lux.SyncResult{Repo: "target", Ref: "main", RequestID: "base-r2-1", Status: "fetched", To: "ccc333"}),
		}, targetMain, map[string]string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Caller{RunID: "r1", env: env{lux: &eventsLux{frames: tc.frames}}}
			got := map[string]string{}
			for repo, res := range landedEarlier(context.Background(), c, baseAsk{LuxRunID: "lr1", Refs: tc.refs}, "base-r1-9") {
				got[repo] = res.To
			}
			if !maps.Equal(got, tc.want) {
				t.Fatalf("counted %v, want %v", got, tc.want)
			}
		})
	}
}
