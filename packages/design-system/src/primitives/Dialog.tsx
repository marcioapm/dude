import * as RadixDialog from "@radix-ui/react-dialog";
import { useRef, type KeyboardEvent, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { compact } from "../util/compact.ts";
import { closeAutoFocus, focusedElement } from "../util/focusReturn.ts";
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
  /** Above the title, quiet: where the thing sits ("Project › Epic"), usually a `Breadcrumb size="sm"`. */
  readonly context?: ReactNode;
  /**
   * `sm`…`xl` grow with their content. `document` is for writing one
   * document: a fixed 1120×900 (less 24px of margin each side) so it does
   * not resize while typing, full screen under 640px.
   */
  readonly size?: "sm" | "md" | "lg" | "xl" | "document" | undefined;
  /**
   * `document` only: a 300px column beside the body, on the chrome shade,
   * scrolling on its own — where the thing sits and help for writing it.
   * Under 960px it follows the body in one scroll.
   */
  readonly aside?: ReactNode;
  /** Accessible name of the aside. */
  readonly asideLabel?: string | undefined;
  /** Danger/attention prefix icon; use for destructive confirmations. */
  readonly tone?: "danger" | "attention" | undefined;
  readonly footer?: ReactNode;
  /** At the footer's start, muted: key hints. Hidden when the footer wraps on a phone. */
  readonly footerStart?: ReactNode;
  readonly children?: ReactNode;
  readonly className?: string | undefined;
  readonly onKeyDown?: ((event: KeyboardEvent<HTMLDivElement>) => void) | undefined;
  /** Where focus goes on open; call `preventDefault()` to place it yourself. */
  readonly onOpenAutoFocus?: ((event: Event) => void) | undefined;
  /**
   * Where focus goes on close. By default it returns to whatever had it
   * when the dialog opened (the trigger, or the button that opened a
   * controlled dialog); call `preventDefault()` to place it yourself.
   */
  readonly onCloseAutoFocus?: ((event: Event) => void) | undefined;
}

/**
 * Modal dialog (Radix). Focus is trapped, Escape closes, the title is the
 * accessible name, and on close focus returns to where it was. Keep it for
 * decisions — confirmations, small forms — and for writing one document
 * (`size="document"`), not for browsing content; use a side panel for that.
 */
export function Dialog({
  open,
  defaultOpen,
  onOpenChange,
  trigger,
  title,
  description,
  context,
  size = "md",
  aside,
  asideLabel,
  tone,
  footer,
  footerStart,
  children,
  className,
  onKeyDown,
  onOpenAutoFocus,
  onCloseAutoFocus,
}: DialogProps) {
  // Radix returns focus only to its own Trigger; a dialog opened from a
  // menu item or a button elsewhere would leave it on <body>.
  const opener = useRef<Element | null>(null);
  const split = size === "document" && aside !== undefined && aside !== null;
  return (
    <RadixDialog.Root {...compact({ open, defaultOpen, onOpenChange })}>
      {trigger ? <RadixDialog.Trigger asChild>{trigger}</RadixDialog.Trigger> : null}
      <RadixDialog.Portal>
        <RadixDialog.Overlay className={styles["overlay"]} />
        <RadixDialog.Content
          className={cx(styles["content"], size !== "md" && styles[size], className)}
          onOpenAutoFocus={(e) => {
            opener.current = focusedElement();
            onOpenAutoFocus?.(e);
          }}
          onCloseAutoFocus={closeAutoFocus(() => opener.current, onCloseAutoFocus)}
          {...(onKeyDown ? { onKeyDown } : {})}
        >
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
              {context ? <div className={styles["context"]}>{context}</div> : null}
              <RadixDialog.Title className={styles["title"]}>{title}</RadixDialog.Title>
              {description ? (
                <RadixDialog.Description className={styles["description"]}>{description}</RadixDialog.Description>
              ) : null}
            </div>
            <RadixDialog.Close asChild>
              <IconButton icon="close" label="Close" size="sm" />
            </RadixDialog.Close>
          </div>
          {split ? (
            <div className={styles["split"]}>
              <div className={styles["main"]}>{children}</div>
              <aside className={styles["aside"]} aria-label={asideLabel}>
                {aside}
              </aside>
            </div>
          ) : children ? (
            <div className={styles["body"]}>{children}</div>
          ) : null}
          {footer ? (
            <div className={styles["footer"]}>
              {footerStart ? <div className={styles["footerStart"]}>{footerStart}</div> : null}
              {footer}
            </div>
          ) : null}
        </RadixDialog.Content>
      </RadixDialog.Portal>
    </RadixDialog.Root>
  );
}

/** Wrap a button with this to make it close the dialog. */
export const DialogClose = RadixDialog.Close;
