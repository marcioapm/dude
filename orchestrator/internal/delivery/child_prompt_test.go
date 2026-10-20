package delivery

import (
	"os"
	"strings"
	"testing"
)

func TestChildResultPromptGolden(t *testing.T) {
	got, err := childResultText([]byte(`{"childId":"run_child","profile":"investigator","status":"completed"}`), "")
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

func TestGoldenChildResultReply(t *testing.T) {
	got, err := childResultText([]byte(`{"childId":"run_child","profile":"implementer","status":"completed"}`),
		"I did not implement it: the base branch is missing.\n\n```yaml\nchecks: []\n```")
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "child-result-reply", got)
}

func TestChildToolsContractGolden(t *testing.T) {
	got := render(mustTemplate("contracts/child-tools"), InstructionsInput{Child: true, Tools: true, CLI: true}.data("investigator"))
	if *updateGoldens {
		if err := os.WriteFile("testdata/child-tools.golden", []byte(got+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile("testdata/child-tools.golden")
	if err != nil {
		t.Fatal(err)
	}
	if got != strings.TrimSuffix(string(want), "\n") {
		t.Fatalf("child tools contract differs:\n%s", got)
	}
}
