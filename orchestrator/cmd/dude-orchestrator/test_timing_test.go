package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/phases"
)

func TestTestConductorAnswerWait(t *testing.T) {
	if delivery.ConductorAnswerWait != 10*time.Minute {
		t.Fatal("production answer wait changed")
	}
	for _, tt := range []struct {
		value string
		want  time.Duration
		bad   bool
	}{
		{"", 0, false}, {"30s", 30 * time.Second, false},
		{"0s", 0, true}, {"-1s", 0, true}, {"invalid", 0, true},
	} {
		t.Run(tt.value, func(t *testing.T) {
			got, err := testConductorAnswerWait(func(key string) string {
				if key != "DUDE_TEST_CONDUCTOR_ANSWER_WAIT" {
					t.Fatalf("unexpected key %s", key)
				}
				return tt.value
			})
			if got != tt.want || (err != nil) != tt.bad {
				t.Fatalf("got %s, %v; want %s, error=%v", got, err, tt.want, tt.bad)
			}
			// The clock the process starts with reaches both the writer
			// question's wait and the held asks' (reviewer question,
			// repository request, idle park); none leaves both at zero,
			// the Syncer's production default.
			s := &phases.Syncer{}
			withTestWaits(s, got)
			if s.ConductorAnswerWait != tt.want || s.HeldAskWait != tt.want {
				t.Fatalf("syncer waits %s / %s, want both %s", s.ConductorAnswerWait, s.HeldAskWait, tt.want)
			}
		})
	}
}

// run gives the syncer the test clock through withTestWaits, once, right
// after building it: a process started with the variable shortens the
// held-ask handover too.
func TestRunAppliesTheTestWaits(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(src), "withTestWaits(syncer, answerWait)"); n != 1 {
		t.Fatalf("run applies the test waits %d times, want once", n)
	}
}
