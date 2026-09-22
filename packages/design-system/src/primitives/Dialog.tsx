import * as RadixDialog from "@radix-ui/react-dialog";
import type { ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { compact } from "../util/compact.ts";
import { Icon } from "../icons/index.tsx";
import { IconButton } from "./Button.tsx";
import styles from "./Dialog.module.css";

export interface DialogProps {
  readonly open?: boolean | undefined;
  readonly defaultOpen?: boolean | undefined;
  readonly onOpenChange?: ((open: boolean) => void) | undefined;
  /** Element that opens the dialog; rendered with asChild. */
  readonly trigger?: ReactNode;
  readonly title: ReactNode;
  readonly description?: ReactNode;
  readonly size?: "sm" | "md" | "lg" | "xl" | undefined;
  /** Danger/attention prefix icon; use for destructive confirmations. */
  readonly tone?: "danger" | "attention" | undefined;
  readonly footer?: ReactNode;
  readonly children?: ReactNode;
  readonly className?: string | undefined;
}

/**
 * Modal dialog (Radix). Focus is trapped, Escape closes, the title is the
 * accessible name. Keep it for decisions — confirmations, small forms —
 * not for browsing content; use a side panel for that.
 */
export function Dialog({
  open,
  defaultOpen,
  onOpenChange,
  trigger,
  title,
  description,
  size = "md",
  tone,
  footer,
  children,
  className,
}: DialogProps) {
  return (
    <RadixDialog.Root {...compact({ open, defaultOpen, onOpenChange })}>
      {trigger ? <RadixDialog.Trigger asChild>{trigger}</RadixDialog.Trigger> : null}
      <RadixDialog.Portal>
        <RadixDialog.Overlay className={styles["overlay"]} />
        <RadixDialog.Content className={cx(styles["content"], size !== "md" && styles[size], className)}>
          <div className={styles["header"]}>
            {tone ? (
              <span
                className={cx(styles["toneIcon"], tone === "danger" ? styles["toneDanger"] : styles["toneAttention"])}
                aria-hidden
              >
                <Icon name={tone === "danger" ? "alert" : "warning"} size={16} />
              </span>
            ) : null}
            <div className={styles["headerText"]}>
              <RadixDialog.Title className={styles["title"]}>{title}</RadixDialog.Title>
              {description ? (
                <RadixDialog.Description className={styles["description"]}>{description}</RadixDialog.Description>
              ) : null}
            </div>
            <RadixDialog.Close asChild>
              <IconButton icon="close" label="Close" size="sm" />
            </RadixDialog.Close>
          </div>
          {children ? <div className={styles["body"]}>{children}</div> : null}
          {footer ? <div className={styles["footer"]}>{footer}</div> : null}
        </RadixDialog.Content>
      </RadixDialog.Portal>
    </RadixDialog.Root>
  );
}

/** Wrap a button with this to make it close the dialog. */
export const DialogClose = RadixDialog.Close;
