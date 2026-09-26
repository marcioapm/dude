/**
 * A place that does not exist: a link to a task, a run or an epic that was
 * deleted, mistyped, or belongs to another organization. Said plainly, with
 * a way somewhere that does.
 */

import { Button, EmptyState } from "@dude/design-system/primitives";

export function NotFound({ what, onBack }: { what: "task" | "run" | "epic" | "project"; onBack: () => void }) {
  return (
    <div className="centered" data-testid="not-found">
      <EmptyState
        icon="search"
        title={`This ${what} doesn't exist (or was deleted)`}
        description="The link may be old, or for another organization."
        action={<Button variant="secondary" leadingIcon="arrow-right" onClick={onBack} data-testid="not-found-back">Go to the board</Button>}
      />
    </div>
  );
}
