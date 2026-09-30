/**
 * Forms that save to the API: what every dialog and settings tab does
 * around its one call — busy while it runs, the server's reason when it
 * refuses, an optional toast when it lands.
 */

import { useCallback, useId, useState, type ReactNode } from "react";
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
   * The fields. Rendered only while the dialog is open, so a component
   * holding their state starts fresh each time it opens.
   */
  children: ReactNode;
}

/**
 * A dialog that is one form: Enter submits it (Ctrl/⌘+Enter in a document,
 * whose fields take Enter), Cancel closes it, and the server's reason for
 * refusing shows under the fields.
 */
export function FormDialog(props: FormDialogProps) {
  const formId = useId();
  const document = props.size === "document";
  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      size={props.size ?? "sm"}
      title={props.title}
      description={props.description}
      context={props.context}
      aside={props.open ? props.aside : undefined}
      asideLabel={props.asideLabel}
      footerStart={props.footerStart}
      onKeyDown={(e) => {
        // A document's fields are multi-line: Enter is a new line there, so Ctrl/⌘+Enter submits.
        if (!document || e.key !== "Enter" || !(e.ctrlKey || e.metaKey) || e.defaultPrevented) return;
        e.preventDefault();
        if (props.canSubmit) props.onSubmit();
      }}
      footer={
        <>
          <Button variant="quiet" onClick={() => props.onOpenChange(false)}>
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
          onSubmit={(e) => {
            e.preventDefault();
            if (props.canSubmit) props.onSubmit();
          }}
        >
          <FormStack fill={document}>
            {props.children}
            {props.problem ? <Callout tone="danger">{props.problem}</Callout> : null}
          </FormStack>
        </form>
      ) : null}
    </Dialog>
  );
}
