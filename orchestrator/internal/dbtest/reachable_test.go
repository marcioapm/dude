package dbtest

import (
	"context"
	"strings"
	"testing"
)

// Reachable fails on an address nothing listens on, naming it, and is nil
// when the test database answers.
func TestReachableNamesTheAddressItCannotReach(t *testing.T) {
	ctx := context.Background()
	if admin, err := adminConn(ctx); err == nil {
		admin.Close(ctx)
		if err := Reachable(ctx); err != nil {
			t.Errorf("Reachable with the test database up: %v", err)
		}
	}
	t.Setenv("DUDE_TEST_PG", "127.0.0.1:1")
	if err := Reachable(ctx); err == nil || !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Errorf("Reachable of a closed port: %v", err)
	}
}
