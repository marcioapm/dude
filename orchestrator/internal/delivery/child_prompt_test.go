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

func TestChildToolsContractGolden(t *testing.T) {
	got := render(mustTemplate("contracts/child-tools"), PromptInput{Child: true, Tools: true, CLI: true})
	if *updateGoldens {
		if err := os.WriteFile("testdata/child-tools.golden", []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile("testdata/child-tools.golden")
	if err != nil {
		t.Fatal(err)
	}
	if got != strings.TrimSuffix(string(want), "\n") {
		t.Fatalf("child tools contract differs:\n%s", got)
	}
}
