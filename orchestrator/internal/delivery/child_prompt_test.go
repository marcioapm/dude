package delivery

import (
	"os"
	"strings"
	"testing"
)

func TestChildResultPromptGolden(t *testing.T) {
	got, err := childResultText([]byte(`{"childId":"run_child","profile":"investigator","status":"completed"}`))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/child-result.golden")
	if err != nil {
		t.Fatal(err)
	}
	if got != strings.TrimSuffix(string(want), "\n") {
		t.Fatalf("result directive differs from golden:\n%s", got)
	}
}
