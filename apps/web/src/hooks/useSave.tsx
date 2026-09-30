/**
 * Forms that save to the API: what every dialog and settings tab does
 * around its one call — busy while it runs, the server's reason when it
 * refuses, an optional toast when it lands.
 */

import { useCallback, useId, useRef, useState, type ReactNode } from "react";
import { Button, Callout, Dialog, FormStack, useToast } from "@dude/design-system/primitives";
import { ApiError } from "../api/client.ts";

/** What went wrong, in words a person can act on. */
export function errorText(err: unknown): string {
  return err instanceof ApiError || err instanceof Error ? err.message : String(err);
}

export interface Save {
  busy: boolean;
  problem: string | null;
  /** Run `action`; on success toast `done` (when given) and call `then`. Resolves whether it succeeded. */
  save: (action: () => Promise<unknown>, then?: () => void, done?: string) => Promise<boolean>;
  clear: () => void;
}

export function useSave(): Save {
  const { toast } = useToast();
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const save = useCallback(
    async (action: () => Promise<unknown>, then?: () => void, done?: string) => {
      setBusy(true);
      setProblem(null);
      try {
        await action();
        if (done) toast({ title: done, tone: "success" });
        then?.();
        return true;
      } catch (err) {
        setProblem(errorText(err));
        return false;
      } finally {
        setBusy(false);
      }
    },
    [toast],
  );
  const clear = useCallback(() => setProblem(null), []);
  return { busy, problem, save, clear };
}

export interface FormDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: ReactNode;
  description?: ReactNode;
  /** Above the title: where the thing sits (a `Breadcrumb size="sm"`). */
  context?: ReactNode;
  /** `document`: writing one document, with `aside` beside the fields. */
  size?: "sm" | "md" | "document";
  /** `document` only: the column beside the fields — where it sits, help for writing it. */
  aside?: ReactNode;
  asideLabel?: string;
  /** At the footer's start, muted: key hints. */
  footerStart?: ReactNode;
  submitLabel: ReactNode;
  canSubmit: boolean;
  onSubmit: () => void;
  problem: string | null;
  /** Buttons beside the submit one (e.g. "Create and deliver"). */
  extraActions?: ReactNode;
  submitTestId?: string;
  /**
   * Opt in to asking before closing loses writing: the words not yet saved
   * (`unsavedWords` in discard.ts), 0 when there is nothing worth asking
   * about. Escape, ×, Cancel and a click outside then ask first.
   */
  unsavedWords?: number;
  /** The confirmation's question: "Discard this task?". */
  discardTitle?: string;
  /**
   * What would be lost. Default: the count of words written, which is only
   * true when every word is new (creating); an edit says so without a count.
   */
  discardDescription?: string | undefined;
  /**
   * The fields. Rendered only while the dialog is open, so a component
   * holding their state starts fresh each time it opens.
   */
  children: ReactNode;
}

/**
 * A dialog that is one form: Enter submits it (in a document, Ctrl/⌘+Enter
 * does, and Enter in a one-line field moves to the next), Cancel closes it,
 * and the server's reason for refusing shows under the fields (in a
 * document, in the footer beside its buttons).
 */
export function FormDialog(props: FormDialogProps) {
  const formId = useId();
  const document = props.size === "document";
  const [confirming, setConfirming] = useState(false);
  const keep = useRef<HTMLButtonElement>(null);
  const unsaved = props.unsavedWords ?? 0;
  // Every way out of the dialog comes here; with writing at stake it asks.
  const requestOpenChange = (open: boolean) => {
    if (!open && unsaved > 0) setConfirming(true);
    else props.onOpenChange(open);
  };
  return (
    <>
      <Dialog
        open={props.open}
        onOpenChange={requestOpenChange}
        size={props.size ?? "sm"}
        title={props.title}
        description={props.description}
        context={props.context}
        aside={props.open ? props.aside : undefined}
        asideLabel={props.asideLabel}
        footerStart={props.footerStart}
        // A document's column scrolls: under the fields the reason would be out of sight.
        footerProblem={document ? props.problem : undefined}
        onKeyDown={(e) => {
          // A document's fields are multi-line: Enter is a new line there, so Ctrl/⌘+Enter submits.
          if (!document || e.key !== "Enter" || !(e.ctrlKey || e.metaKey) || e.defaultPrevented) return;
          e.preventDefault();
          if (props.canSubmit) props.onSubmit();
        }}
        footer={
          <>
            <Button variant="quiet" onClick={() => requestOpenChange(false)}>
              Cancel
            </Button>
            <Button type="submit" form={formId} variant={props.extraActions ? "secondary" : "primary"}
              disabled={!props.canSubmit} data-testid={props.submitTestId}>
              {props.submitLabel}
            </Button>
            {props.extraActions}
          </>
        }
      >
        {props.open ? (
          <form
            id={formId}
            onKeyDown={(e) => {
              // In a document, plain Enter in a one-line field moves on to the next field, never submits.
              if (!document || e.key !== "Enter" || e.ctrlKey || e.metaKey || e.nativeEvent.isComposing) return;
              if (!(e.target instanceof HTMLInputElement)) return;
              e.preventDefault();
              const fields = [...e.currentTarget.querySelectorAll<HTMLElement>("input, textarea, select")]
                .filter((f) => !(f as HTMLInputElement).disabled && f.offsetParent !== null);
              fields[fields.indexOf(e.target) + 1]?.focus();
            }}
            onSubmit={(e) => {
              e.preventDefault();
              if (props.canSubmit) props.onSubmit();
            }}
          >
            <FormStack fill={document}>
              {props.children}
              {props.problem && !document ? <Callout tone="danger">{props.problem}</Callout> : null}
            </FormStack>
          </form>
        ) : null}
      </Dialog>
      <Dialog
        open={props.open && confirming}
        onOpenChange={setConfirming}
        size="sm"
        tone="danger"
        title={props.discardTitle ?? "Discard your changes?"}
        description={props.discardDescription ?? `You have written ${unsaved.toLocaleString("en-US")} ${unsaved === 1 ? "word" : "words"} that haven't been saved.`}
        onOpenAutoFocus={(e) => {
          e.preventDefault();
          keep.current?.focus();
        }}
        footer={
          <>
            <Button ref={keep} variant="quiet" onClick={() => setConfirming(false)} data-testid="discard-keep">
              Keep writing
            </Button>
            <Button variant="danger" solid data-testid="discard-confirm"
              onClick={() => {
                setConfirming(false);
                props.onOpenChange(false);
              }}>
              Discard
            </Button>
          </>
        }
      />
    </>
  );
}
