import { useImperativeHandle, type Ref } from "react";
import { ChatComposer, type ChatComposerProps } from "@dude/design-system/components";
import { useDraft } from "../hooks/useDraft.ts";

/** Sets the composer's text from outside it, drafted as if typed. */
export interface DraftHandle {
  set(text: string): void;
}

/**
 * A `ChatComposer` whose text is the draft for `place` (null: not drafted).
 * The draft's state lives here, below the transcript, so a keystroke
 * re-renders the composer alone, never the screen's turns.
 */
export function DraftedComposer({ place, draftRef, ...props }: Omit<ChatComposerProps, "value" | "defaultValue" | "onValueChange"> & {
  place: string | null;
  draftRef?: Ref<DraftHandle> | undefined;
}) {
  const draft = useDraft(place);
  useImperativeHandle(draftRef, () => ({ set: draft.onValueChange }), [draft.onValueChange]);
  return <ChatComposer {...props} value={draft.value} onValueChange={draft.onValueChange} />;
}
