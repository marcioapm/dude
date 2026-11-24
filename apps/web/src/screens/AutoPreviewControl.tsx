/**
 * Automatic task previews, as the Overview rail's "This task" block shows
 * them: the task's own choice — its project's (saying what that is now),
 * On or Off — with what is in effect. Only what dude does when it opens or
 * reopens the task's pull request: a preview someone asks for starts
 * whatever this says. Anyone on the organisation changes it, as they edit
 * the task.
 */

import { useEffect, useState } from "react";
import { SettingRow } from "@dude/design-system/components";
import { Select, useToast } from "@dude/design-system/primitives";
import type { ApiClient, TaskDetail } from "../api/client.ts";
import { errorText } from "../hooks/useSave.tsx";

type Choice = "project" | "on" | "off";

const toChoice = (v: boolean | null | undefined): Choice => (v === true ? "on" : v === false ? "off" : "project");
const onOff = (v: boolean) => (v ? "On" : "Off");

/** What the task's choice comes to: its own, else its project's. */
export function autoPreviewInEffect(own: boolean | null | undefined, project: boolean): boolean {
  return own ?? project;
}

export function AutoPreviewControl({ client, task, onChanged }: {
  client: ApiClient;
  task: Pick<TaskDetail, "id" | "autoTaskPreview" | "projectAutoTaskPreview">;
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
  const effective = autoPreviewInEffect(own, project);

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
    // Its own container, so the row stacks its label over the control in the narrow rail.
    <div className="autoPreviewRail">
    <SettingRow label="Automatic preview" htmlFor="task-auto-preview" data-testid="task-auto-preview"
      help={<>
        Only whether dude prepares this task’s preview when it opens or reopens the pull request. A preview someone asks for still starts.
        <span className="autoPreviewEffect" data-testid="task-auto-preview-effect">
          {" "}In effect: <strong>{onOff(effective)}</strong> · {own === null ? "project default" : "this task"}
        </span>
      </>}>
      <Select<Choice> id="task-auto-preview" aria-label="Automatic preview" value={value} disabled={busy}
        onValueChange={(v) => void change(v)}
        options={[
          { value: "project", label: `Use project default (${onOff(project)})` },
          { value: "on", label: "On" },
          { value: "off", label: "Off" },
        ]} />
    </SettingRow>
    </div>
  );
}
