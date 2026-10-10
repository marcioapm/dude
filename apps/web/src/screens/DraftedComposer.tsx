import { ChatComposer, type ChatComposerProps } from "@dude/design-system/components";
import { useDraft } from "../hooks/useDraft.ts";

/**
 * A `ChatComposer` whose text is the draft for `place` (null: not drafted).
 * The draft's state lives here, below the transcript, so a keystroke
 * re-renders the composer alone, never the screen's turns.
 */
export function DraftedComposer({ place, ...props }: Omit<ChatComposerProps, "value" | "defaultValue" | "onValueChange"> & { place: string | null }) {
  const draft = useDraft(place);
  return <ChatComposer {...props} value={draft.value} onValueChange={draft.onValueChange} />;
}
