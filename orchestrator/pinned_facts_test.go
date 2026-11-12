package orchestrator_test

import (
	"strings"
	"testing"
)

// A Run's first prompt carries its project's pinned facts, beside its
// epic: the implementer delivery starts is told the pinned fact, and not
// an unpinned one, another project's pinned one or a session's memory.
func TestARunsPromptCarriesTheProjectsPinnedFacts(t *testing.T) {
	w := conducting(t)
	// A real model's implementer is given the composed prompt (the
	// scripted one is given its script).
	w.onModel("implementer", "claude-opus-5-5")
	mustExec(t, w.owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_else', $1, 'E', 'else', 'E')`, w.org)
	mustExec(t, w.owner, `INSERT INTO sessions (id, organization_id, title, titled_by) VALUES ('ses_pf', $1, 'S', 'person')`, w.org)
	mustExec(t, w.owner, `INSERT INTO memories (id, organization_id, project_id, session_id, title, content, author_kind, system_reason, pinned_at)
		VALUES ('mem_pin', $1, $2, NULL, 'TestMemo is flaky on main', 'Rerun it once.', 'system', 'test', now()),
		('mem_unpin', $1, $2, NULL, 'Unpinned fact here', 'x', 'system', 'test', NULL),
		('mem_else', $1, 'prj_else', NULL, 'Another project pinned this', 'x', 'system', 'test', now()),
		('mem_ses', $1, NULL, 'ses_pf', 'A session remembered this', 'x', 'system', 'test', NULL)`, w.org, w.project)
	task := w.task()
	w.talk(task)
	w.must(task, "start_phase", `{"phase":"implement"}`)
	w.until("the implementer", func() bool { return w.specOf("implement") != nil })
	p := w.specOf("implement").Workload.Prompt
	if !strings.Contains(p, "## Project facts") || !strings.Contains(p, "**TestMemo is flaky on main** (mem_pin): Rerun it once.") {
		t.Errorf("the implementer's prompt lacks the pinned fact:\n%s", p)
	}
	for _, never := range []string{"Unpinned fact here", "Another project pinned this", "A session remembered this"} {
		if strings.Contains(p, never) {
			t.Errorf("the implementer's prompt carries %q", never)
		}
	}
}
