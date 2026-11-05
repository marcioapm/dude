package main

import (
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
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
		})
	}
}
