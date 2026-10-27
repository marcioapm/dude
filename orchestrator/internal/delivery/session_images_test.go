package delivery

import (
	"strings"
	"testing"
)

// The section filing adds shows each image as the task's text references
// an upload, readable by ImageRefs whatever the goal ends in.
func TestWithSessionImagesIsReadAsTheTasksImages(t *testing.T) {
	images := []SessionImage{{ID: "att_one", Name: "design [v2].png"}, {ID: "att_two", Name: "menu.png"}}
	for _, goal := range []string{"Build the dropdown.", "Run this:\n\n```sh\nexit ${PIPESTATUS[0]}"} {
		out := WithSessionImages(goal, images)
		if !strings.HasPrefix(out, goal) {
			t.Errorf("the goal's words changed:\n%s", out)
		}
		refs := ImageRefs(out)
		if len(refs) != 2 || refs[0].ID != "att_one" || refs[0].Alt != "design (v2).png" || refs[1].ID != "att_two" {
			t.Errorf("after %q the references are %+v:\n%s", goal, refs, out)
		}
	}
	if out := WithSessionImages("Build it.", nil); out != "Build it." {
		t.Errorf("no images changed the goal: %q", out)
	}
}
