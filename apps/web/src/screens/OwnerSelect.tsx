/**
 * Who drives a task, and handing it to someone else. The owner is told
 * when the task waits on a person, and is the only one who answers its
 * agents; anyone may reassign it.
 */

import { useEffect, useState } from "react";
import { Select } from "@dude/design-system/primitives";
import type { ApiClient, Person, Task } from "../api/client.ts";
import { errorText } from "../hooks/useSave.tsx";

export function OwnerSelect({ client, task, onChanged, onProblem }: {
  client: ApiClient;
  task: Task;
  onChanged: () => void;
  onProblem: (message: string) => void;
}) {
  const [people, setPeople] = useState<Person[] | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    void client.listPeople().then((p) => setPeople(p.people), (err: unknown) => onProblem(errorText(err)));
  }, [client, onProblem]);

  // The owner stays listed even when their key is gone from the picker
  // (revoked), so the select never shows blank for a task someone drives.
  const options = [...(people ?? [])];
  if (task.owner && !options.some((p) => p.id === task.owner!.id)) options.push(task.owner);

  const reassign = async (ownerId: string) => {
    if (ownerId === task.owner?.id) return;
    setBusy(true);
    try {
      await client.reassignTask(task.id, ownerId);
      onChanged();
    } catch (err) {
      onProblem(`Could not reassign it: ${errorText(err)}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="ownerField" data-testid="task-owner" data-owner={task.owner?.name ?? ""}>
      <span className="muted" aria-hidden>Owner</span>
      <Select
        size="sm"
        aria-label="Owner"
        placeholder="Nobody owns it"
        value={task.owner?.id}
        disabled={busy || people === null}
        onValueChange={(id) => void reassign(id)}
        options={options.map((p) => ({ value: p.id, label: p.name }))}
      />
    </div>
  );
}
