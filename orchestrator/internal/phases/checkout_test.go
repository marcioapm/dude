package phases

import (
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// Where a phase checks a repository out: the commit it was asked to start
// from; with none, the branch a task took over, else the default branch. A
// conductor that may change it sits on the task branch by name.
func TestARunChecksOutTheTakenOverBranchWhereNothingWasPublished(t *testing.T) {
	target := delivery.Repository{Name: "target", DefaultBranch: "main", Access: "write"}
	web := delivery.Repository{Name: "web", DefaultBranch: "trunk", Access: "write"}
	fresh := delivery.TaskBranch{Branch: delivery.BranchFor("t1", 1)}
	adopted := delivery.TaskBranch{Branch: "feature/greet", Bases: map[string]string{"target": "main"}}

	cases := []struct {
		name string
		run  phaseRun
		repo delivery.Repository
		tb   delivery.TaskBranch
		want string
	}{
		{"new work, nothing yet", phaseRun{Phase: delivery.PhaseImplement}, target, fresh, "main"},
		{"new work, a head", phaseRun{Phase: delivery.PhaseReview, BaseRefs: map[string]string{"target": "abc"}}, target, fresh, "abc"},
		{"adopted, nothing yet", phaseRun{Phase: delivery.PhaseReview}, target, adopted, "feature/greet"},
		{"adopted, a head", phaseRun{Phase: delivery.PhaseFix, BaseRefs: map[string]string{"target": "def"}}, target, adopted, "def"},
		{"adopted, another repository", phaseRun{Phase: delivery.PhaseReview}, web, adopted, "trunk"},
		{"conductor on adopted", phaseRun{Role: delivery.RoleConductor, BaseRefs: map[string]string{"target": "def"}}, target, adopted, "feature/greet"},
		{"conductor on new work", phaseRun{Role: delivery.RoleConductor, BaseRefs: map[string]string{"target": "def"}}, target, fresh, fresh.Branch},
	}
	for _, c := range cases {
		if ref, _ := checkoutOf(c.run, c.repo, c.tb); ref != c.want {
			t.Errorf("%s: checks out %s, want %s", c.name, ref, c.want)
		}
	}
}
