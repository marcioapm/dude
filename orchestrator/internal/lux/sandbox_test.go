package lux_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// Whether a submitted Run may start containers is its stored spec's
// sandbox, as lux answers the submit; the spec sent only when lux's answer
// has no sandbox.
func TestASubmittedRunsNestedContainersIsTheSandboxLuxReturned(t *testing.T) {
	asks := lux.Spec{Sandbox: &lux.Sandbox{NestedContainers: true}}
	for _, c := range []struct {
		name   string
		answer string
		sent   lux.Spec
		want   bool
	}{
		{"lux's Run may, the retry asks for none", `{"id":"r","spec":{"sandbox":{"nestedContainers":true}}}`, lux.Spec{}, true},
		{"lux's Run may not, the retry asks", `{"id":"r","spec":{"sandbox":{}}}`, asks, false},
		{"no sandbox in lux's answer: what was sent", `{"id":"r","spec":{}}`, asks, true},
		{"no sandbox in lux's answer, none sent", `{"id":"r","spec":{}}`, lux.Spec{}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(c.answer))
			}))
			defer srv.Close()
			lr, err := lux.New(srv.URL, "k").Submit(context.Background(), c.sent, "key")
			if err != nil {
				t.Fatal(err)
			}
			if got := lr.NestedContainers(c.sent); got != c.want {
				t.Errorf("NestedContainers = %v, want %v", got, c.want)
			}
		})
	}
}
