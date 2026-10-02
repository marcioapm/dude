import * as RadixDialog from "@radix-ui/react-dialog";
import { useLayoutEffect, useRef, type FocusEvent, type KeyboardEvent, type ReactNode } from "react";
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
  /** In the header, before Close: quiet actions on the whole thing (a `Read` toggle). */
  readonly headerActions?: ReactNode;
  /**
   * `document` only: what it reads as, shown over the body and the aside
   * while set — one centred column at the document measure, scrolling on
   * its own. The writing stays laid out under it, hidden and inert, so its
   * scroll positions and its fields' state are as they were on return, and
   * focus goes back to the field that last had it.
   */
  readonly reading?: ReactNode;
  /** Accessible name of the reading column. */
  readonly readingLabel?: string | undefined;
  /** Escape while `reading`: called instead of closing the dialog, after `onEscapeKeyDown` has had it. */
  readonly onCloseReading?: (() => void) | undefined;
  /** Danger/attention prefix icon; use for destructive confirmations. */
  readonly tone?: "danger" | "attention" | undefined;
  readonly footer?: ReactNode;
  /** At the footer's start, muted: key hints. Hidden under 640px, where there is rarely a keyboard. */
  readonly footerStart?: ReactNode;
  /**
   * Why the last save or load failed, in the footer's start slot in place of
   * `footerStart`: one line of danger ink beside the buttons that were just
   * pressed, the whole text in its tooltip. Shown at every width.
   */
  readonly footerProblem?: string | null | undefined;
  readonly children?: ReactNode;
  readonly className?: string | undefined;
  readonly onKeyDown?: ((event: KeyboardEvent<HTMLDivElement>) => void) | undefined;
  /** Escape, before the dialog closes; `preventDefault()` keeps it open. */
  readonly onEscapeKeyDown?: ((event: globalThis.KeyboardEvent) => void) | undefined;
  /** Where focus goes on open; call `preventDefault()` to place it yourself. */
  readonly onOpenAutoFocus?: ((event: Event) => void) | undefined;
  /**
   * Where focus goes on close. By default it returns to whatever had it
   * when the dialog opened (the trigger, or the button that opened a
   * controlled dialog); call `preventDefault()` to place it yourself.
   */
  readonly onCloseAutoFocus?: ((event: Event) => void) | undefined;
  /**
   * Puts the header, the body and the footer inside an element of the app's
   * (an `AttachDropZone` for the whole dialog). Give it `flex: 1` and a flex
   * column, as the dialog lays them out.
   */
  readonly wrapContent?: ((content: ReactNode) => ReactNode) | undefined;
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
  headerActions,
  reading,
  readingLabel,
  onCloseReading,
  tone,
  footer,
  footerStart,
  footerProblem,
  children,
  className,
  onKeyDown,
  onEscapeKeyDown,
  onOpenAutoFocus,
  onCloseAutoFocus,
  wrapContent,
}: DialogProps) {
  // Radix returns focus only to its own Trigger; a dialog opened from a
  // menu item or a button elsewhere would leave it on <body>.
  const opener = useRef<Element | null>(null);
  const split = size === "document" && aside !== undefined && aside !== null;
  const isReading = size === "document" && reading !== undefined && reading !== null && reading !== false;
  const readingColumn = useRef<HTMLDivElement>(null);
  const writing = useRef<HTMLDivElement>(null);
  // The field last focused in the writing, to go back to after reading.
  const lastFocused = useRef<HTMLElement | null>(null);
  const wasReading = useRef(false);

  useLayoutEffect(() => {
    if (isReading === wasReading.current) return;
    wasReading.current = isReading;
    if (isReading) {
      // Focus in what just went out of reach goes to the document, so keys scroll it.
      const active = document.activeElement;
      if (!active || active === document.body || writing.current?.contains(active)) readingColumn.current?.focus();
    } else if (lastFocused.current?.isConnected) {
      lastFocused.current.focus({ preventScroll: true });
    }
  }, [isReading]);

  // While reading, the writing stays laid out under the document, hidden
  // and inert: its scroll positions, its editors' modes and selections are
  // as they were on return.
  const writingProps = {
    ref: writing,
    "data-covered": isReading ? "true" : undefined,
    ...(isReading ? { inert: true } : {}),
    // React bubbles focus from portalled popups (a Select's options) through
    // here too; those unmount, so only the writing's own elements are kept.
    onFocusCapture: (e: FocusEvent<HTMLDivElement>) => {
      if (e.target instanceof HTMLElement && e.currentTarget.contains(e.target)) lastFocused.current = e.target;
    },
  };
  const body = split ? (
    <div className={styles["split"]} {...writingProps}>
      <div className={styles["main"]}>{children}</div>
      <aside className={styles["aside"]} aria-label={asideLabel}>
        {aside}
      </aside>
    </div>
  ) : children ? (
    <div className={styles["body"]} {...writingProps}>
      {children}
    </div>
  ) : null;

  const content = (
    <>
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
        {headerActions ? <div className={styles["headerActions"]}>{headerActions}</div> : null}
        <RadixDialog.Close asChild>
          <IconButton icon="close" label="Close" size="sm" />
        </RadixDialog.Close>
      </div>
      {size === "document" ? (
        <div className={styles["stage"]}>
          {body}
          {isReading ? (
            <div ref={readingColumn} className={styles["reading"]} role="region" aria-label={readingLabel} tabIndex={0}>
              <div className={styles["readingMeasure"]}>{reading}</div>
            </div>
          ) : null}
        </div>
      ) : (
        body
      )}
      {footer ? (
        <div className={styles["footer"]}>
          {footerProblem ? (
            <div className={styles["footerProblem"]} role="alert" title={footerProblem}>
              {footerProblem}
            </div>
          ) : footerStart ? (
            <div className={styles["footerStart"]}>{footerStart}</div>
          ) : null}
          {footer}
        </div>
      ) : null}
    </>
  );

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
          onEscapeKeyDown={(e) => {
            onEscapeKeyDown?.(e);
            if (e.defaultPrevented || !isReading || !onCloseReading) return;
            // Reading is a view of the dialog, not a layer over it: Escape leaves it, and only it.
            e.preventDefault();
            onCloseReading();
          }}
        >
          {wrapContent ? wrapContent(content) : content}
        </RadixDialog.Content>
      </RadixDialog.Portal>
    </RadixDialog.Root>
  );
}

/** Wrap a button with this to make it close the dialog. */
export const DialogClose = RadixDialog.Close;

