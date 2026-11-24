/**
 * Automatic task previews, as the Overview rail's "This task" block shows
 * them, with the rail's task-choice component (BeforePullRequestFact): the
 * task's own choice — its project's (saying what that is now), On or Off —
 * with what is in effect in its hint. Only what dude does when it opens or
 * reopens the task's pull request: a preview someone asks for starts
 * whatever this says. Anyone on the organisation changes it, as they edit
 * the task; once the task is done, failed or aborted it is disabled, saying
 * so, as the rail's other choices are.
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

/** Why a task that ended without being done no longer takes the choice. */
export const TASK_OVER_TEXT = "The task is over.";

/** What the task's choice comes to: its own, else its project's. */
export function autoPreviewInEffect(own: boolean | null | undefined, project: boolean): boolean {
  return own ?? project;
}

/** The hint under the choice: what it governs, and what is in effect. */
export function autoPreviewHint(own: boolean | null, project: boolean): string {
  return "Only whether dude prepares this task’s preview when it opens or reopens the pull request. A preview someone asks for still starts. "
    + `In effect: ${onOff(autoPreviewInEffect(own, project))} · ${own === null ? "project default" : "this task"}`;
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
  const state: BeforePullRequestFactState = task.status === "done" ? { kind: "locked", reason: TASK_DONE_TEXT }
    : task.status === "failed" || task.status === "aborted" ? { kind: "locked", reason: TASK_OVER_TEXT }
    : { kind: "editable" };

  const change = async (to: Choice) => {
    if (to === value) return;
    setShown(to);
    setBusy(true);
    try {
      await client.updateTask(task.id, { autoTaskPreview: to === "project" ? null : to === "on" });
      toast({ title: `Automatic preview ${onOff(autoPreviewInEffect(to === "project" ? null : to === "on", project)).toLowerCase()} for this task`, tone: "success" });
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
