import { useRef, type ReactNode } from "react";
import { Button } from "./Button.tsx";
import { Dialog } from "./Dialog.tsx";

export interface DiscardConfirmProps {
  readonly open: boolean;
  /** The question: "Discard this task?". */
  readonly title: ReactNode;
  /** What would be lost: "You have written 195 words that haven't been saved." */
  readonly description: ReactNode;
  /** Close the confirmation and go back to the writing. Escape and × do the same. */
  readonly onKeep: () => void;
  readonly onDiscard: () => void;
}

/**
 * Asked before closing a dialog that holds writing would lose it. Keep
 * writing is quiet and focused on open, so Enter or Escape is the safe way
 * out; Discard is `danger solid`, inside its own confirmation. Whether to
 * ask at all is the app's rule.
 */
export function DiscardConfirm({ open, title, description, onKeep, onDiscard }: DiscardConfirmProps) {
  const keep = useRef<HTMLButtonElement>(null);
  return (
    <Dialog
      open={open}
      onOpenChange={(next) => !next && onKeep()}
      size="sm"
      tone="danger"
      title={title}
      description={description}
      onOpenAutoFocus={(e) => {
        e.preventDefault();
        keep.current?.focus();
      }}
      footer={
        <>
          <Button ref={keep} variant="quiet" onClick={onKeep} data-testid="discard-keep">
            Keep writing
          </Button>
          <Button variant="danger" solid onClick={onDiscard} data-testid="discard-confirm">
            Discard
          </Button>
        </>
      }
    />
  );
}
