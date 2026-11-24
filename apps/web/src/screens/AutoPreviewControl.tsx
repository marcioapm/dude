/**
 * Automatic task previews, as the Overview rail's "This task" block shows
 * them, with the rail's task-choice component (BeforePullRequestFact): the
 * task's own choice — its project's (saying what that is now), On or Off —
 * with what is in effect in its hint. Only what dude does when the task's
 * pull request opens or reopens: a preview someone asks for starts
 * whatever this says. Anyone on the organisation changes it, as they edit
 * the task; once the task is done it is disabled, saying so, as
 * BeforePullRequestControl is (a failed or aborted task can come back).
 */

import { useEffect, useState } from "react";
import { BeforePullRequestFact, type BeforePullRequestFactState } from "@dude/design-system/components";
import { useToast } from "@dude/design-system/primitives";
import type { ApiClient, TaskDetail } from "../api/client.ts";
import { errorText } from "../hooks/useSave.tsx";
import { TASK_DONE_TEXT } from "../beforePullRequest.ts";

type Choice = "project" | "on" | "off";

const toChoice = (v: boolean | null | undefined): Choice => (v === true ? "on" : v === false ? "off" : "project");
const onOff = (v: boolean) => (v ? "On" : "Off");

/** The hint under the choice: what it governs, and what is in effect (its own, else its project's). */
function autoPreviewHint(own: boolean | null, project: boolean): string {
  return "Only whether dude prepares this task’s preview when its pull request opens or reopens. A preview someone asks for still starts. "
    + `Effective: ${onOff(own ?? project)} · ${own === null ? "project default" : "task override"}`;
}

export function AutoPreviewControl({ client, task, onChanged }: {
  client: ApiClient;
  task: Pick<TaskDetail, "id" | "status" | "autoTaskPreview" | "projectAutoTaskPreview">;
  onChanged: () => void;
}) {
  const { toast } = useToast();
  const [busy, setBusy] = useState(false);
  const [shown, setShown] = useState<Choice | null>(null);
  // What was picked shows until the task, read again, says it.
  useEffect(() => setShown(null), [task.autoTaskPreview]);
  const project = task.projectAutoTaskPreview ?? false;
  const value = shown ?? toChoice(task.autoTaskPreview);
  const own = value === "project" ? null : value === "on";
  const state: BeforePullRequestFactState = task.status === "done" ? { kind: "locked", reason: TASK_DONE_TEXT } : { kind: "editable" };

  const change = async (to: Choice) => {
    if (to === value) return;
    setShown(to);
    setBusy(true);
    const next = to === "project" ? null : to === "on";
    try {
      await client.updateTask(task.id, { autoTaskPreview: next });
      toast({ title: `Automatic preview ${onOff(next ?? project).toLowerCase()} for this task`, tone: "success" });
      onChanged();
    } catch (err) {
      setShown(null);
      toast({ title: errorText(err), tone: "danger" });
    } finally {
      setBusy(false);
    }
  };

  return (
    <div data-testid="task-auto-preview">
      <BeforePullRequestFact<Choice>
        label="Automatic preview"
        value={value}
        options={[
          { value: "project", label: `Use project default (${onOff(project)})` },
          { value: "on", label: "On" },
          { value: "off", label: "Off" },
        ]}
        state={state}
        busy={busy}
        hint={autoPreviewHint(own, project)}
        onValueChange={(v) => void change(v)}
      />
    </div>
  );
}
