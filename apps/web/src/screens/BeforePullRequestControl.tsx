/**
 * Before the pull request, as the Overview rail's "This task" block shows
 * it: the owner changes the task's own choice while it runs (it is read
 * when Deliver reaches the pull request), with a toast saying what Deliver
 * will now do; once a pull request is open, or the task is done, it is
 * disabled, saying so; anyone else reads it with whose it is.
 */

import { useEffect, useState } from "react";
import { BeforePullRequestFact, type BeforePullRequestFactState } from "@dude/design-system/components";
import { firstName } from "@dude/design-system";
import { useToast } from "@dude/design-system/primitives";
import type { ApiClient, TaskDetail } from "../api/client.ts";
import { errorText } from "../hooks/useSave.tsx";
import {
  PULL_REQUEST_OPEN_TEXT, TASK_DONE_TEXT, choiceToast, taskChoice, taskChoiceOptions, type TaskPullRequestChoice,
} from "../beforePullRequest.ts";

export function BeforePullRequestControl({ client, task, you, onChanged }: {
  client: ApiClient;
  task: Pick<TaskDetail, "id" | "owner" | "status" | "pullRequestChoice" | "projectPullRequest" | "pullRequestOpened">;
  you: string | null;
  onChanged: () => void;
}) {
  const { toast } = useToast();
  const [busy, setBusy] = useState(false);
  const [shown, setShown] = useState<TaskPullRequestChoice | null>(null);
  // What was picked shows until the task, read again, says it.
  useEffect(() => setShown(null), [task.pullRequestChoice]);
  const value = shown ?? taskChoice(task.pullRequestChoice);
  const yours = !task.owner || (you !== null && task.owner.id === you);
  const state: BeforePullRequestFactState = task.pullRequestOpened ? { kind: "locked", reason: PULL_REQUEST_OPEN_TEXT }
    : task.status === "done" ? { kind: "locked", reason: TASK_DONE_TEXT }
    : yours ? { kind: "editable" } : { kind: "read-only", owner: firstName(task.owner!.name) };

  const change = async (to: TaskPullRequestChoice) => {
    if (to === value) return;
    setShown(to);
    setBusy(true);
    try {
      await client.setPullRequestChoice(task.id, to === "project" ? null : to);
      toast({ title: choiceToast(to, task.projectPullRequest), tone: "success" });
      onChanged();
    } catch (err) {
      setShown(null);
      toast({ title: errorText(err), tone: "danger" });
    } finally {
      setBusy(false);
    }
  };

  return (
    <BeforePullRequestFact<TaskPullRequestChoice>
      value={value}
      options={taskChoiceOptions(task.projectPullRequest)}
      state={state}
      busy={busy}
      hint="Read when Deliver reaches the pull request."
      onValueChange={(v) => void change(v)}
    />
  );
}
