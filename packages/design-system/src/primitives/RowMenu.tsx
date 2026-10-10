import * as RadixMenu from "@radix-ui/react-dropdown-menu";
import { forwardRef, type KeyboardEvent, type MouseEvent, type ReactNode, type SyntheticEvent } from "react";
import { cx } from "../util/cx.ts";
import { compact } from "../util/compact.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import { IconButton, type IconButtonProps } from "./Button.tsx";
import { Tooltip } from "./Tooltip.tsx";
import styles from "./RowMenu.module.css";

export interface RowMenuAction {
  readonly kind?: "item" | undefined;
  readonly id: string;
  readonly label: string;
  readonly icon?: IconName | undefined;
  /** A face in the glyph's place (a project's, a person's), for a menu of things rather than actions. */
  readonly leading?: ReactNode;
  /** Display only — the menu does not bind it. "⌘E", "Del". */
  readonly shortcut?: string | undefined;
  /** A second, quieter line under the label: what the item is. */
  readonly description?: string | undefined;
  /** Set the label in mono: a name, a path, a `{{variable}}`. */
  readonly mono?: boolean | undefined;
  /** Danger: the action loses work. Rendered in danger ink; still needs a confirm dialog behind it. */
  readonly tone?: "default" | "danger" | undefined;
  readonly disabled?: boolean | undefined;
  /** Why it is disabled; shown as a tooltip and read to screen readers. */
  readonly disabledReason?: string | undefined;
  readonly onSelect?: (() => void) | undefined;
}

export interface RowMenuSeparator {
  readonly kind: "separator";
}

export interface RowMenuSubmenu {
  readonly kind: "submenu";
  readonly id: string;
  readonly label: string;
  readonly icon?: IconName | undefined;
  readonly items: ReadonlyArray<RowMenuItem>;
  readonly disabled?: boolean | undefined;
  readonly disabledReason?: string | undefined;
}

export type RowMenuItem = RowMenuAction | RowMenuSeparator | RowMenuSubmenu;

export interface RowMenuProps {
  readonly items: ReadonlyArray<RowMenuItem>;
  /** Accessible name of the trigger: "Actions for CP-41". Default "More actions". */
  readonly label?: string | undefined;
  readonly size?: "sm" | "md" | undefined;
  /** Replace the default "more" IconButton. Must forward its ref (asChild). */
  readonly trigger?: ReactNode;
  readonly open?: boolean | undefined;
  readonly defaultOpen?: boolean | undefined;
  readonly onOpenChange?: ((open: boolean) => void) | undefined;
  /** Fires for every action, after the item's own `onSelect`. */
  readonly onSelect?: ((id: string) => void) | undefined;
  readonly align?: "start" | "end" | undefined;
  readonly side?: "top" | "right" | "bottom" | "left" | undefined;
  /**
   * Where focus goes when the menu closes. Radix returns it to the trigger;
   * a tree or grid row wants it back on the row so arrow keys keep working
   * — call `e.preventDefault()` and focus the row.
   */
  readonly onCloseAutoFocus?: ((e: Event) => void) | undefined;
  /** Class on the menu panel. */
  readonly className?: string | undefined;
  /** Class on the default trigger; ignored when `trigger` is given. */
  readonly triggerClassName?: string | undefined;
  /** Rendered with the menu open for a static preview (gallery). Never in the product. */
  readonly forceMount?: true | undefined;
  /** Where the menu is portaled; the body by default. A preview inside a themed pane passes the pane. */
  readonly container?: HTMLElement | null | undefined;
}

/**
 * The overflow menu behind a "…" button on a row — a tree row, a board
 * header, a table row. The design system does not know the actions; the
 * app passes them. Every item has a label and may have a glyph, a shortcut
 * hint, a danger tone, a disabled reason and a submenu ("Move to epic ›").
 *
 * Opens on click, on Shift+F10 / the context-menu key and, if the row
 * spreads `rowMenuOpeners`, on right-click. Portaled above dialogs.
 */
export function RowMenu({ items, label = "More actions", size = "sm", trigger, open, defaultOpen, onOpenChange, onSelect, align = "end", side = "bottom", onCloseAutoFocus, className, triggerClassName, forceMount, container }: RowMenuProps) {
  return (
    <RadixMenu.Root modal={false} {...compact({ open, defaultOpen, onOpenChange })}>
      <RadixMenu.Trigger asChild>
        {trigger ?? <IconButton icon="more" label={label} size={size} className={cx(styles["trigger"], triggerClassName)} onClick={stop} onKeyDown={stopIfActivation} />}
      </RadixMenu.Trigger>
      <RadixMenu.Portal {...(forceMount ? { forceMount } : {})} {...(container ? { container } : {})}>
        <RadixMenu.Content
          className={cx(styles["content"], className)}
          align={align}
          side={side}
          sideOffset={4}
          collisionPadding={8}
          loop
          onClick={stop}
          onKeyDown={stopIfActivation}
          onContextMenu={swallow}
          {...(onCloseAutoFocus ? { onCloseAutoFocus } : {})}
          {...(forceMount ? { forceMount } : {})}
        >
          <MenuItems items={items} onSelect={onSelect} />
        </RadixMenu.Content>
      </RadixMenu.Portal>
    </RadixMenu.Root>
  );
}

/** The click that opens the menu must not also select the row beneath it. */
function stop(e: SyntheticEvent) {
  e.stopPropagation();
}
/** A right-click on the open menu is neither the row's nor the browser's. */
function swallow(e: SyntheticEvent) {
  e.preventDefault();
  e.stopPropagation();
}
/** Enter / Space inside the menu must not reach a row's own key handler. */
function stopIfActivation(e: KeyboardEvent) {
  if (e.key === "Enter" || e.key === " " || e.key.startsWith("Arrow") || e.key === "Home" || e.key === "End" || e.key === "Escape") e.stopPropagation();
}

/** Run `ours` after `theirs` (a handler injected by a Radix `asChild` slot), so neither is lost. */
export function chain<E>(theirs: ((e: E) => void) | undefined, ours: (e: E) => void): (e: E) => void {
  return (e) => {
    theirs?.(e);
    ours(e);
  };
}

/**
 * Nothing holds focus — the active element is missing or the body. The
 * moment to hand focus back to a row after its menu closes; if something
 * else (another row's trigger) already has it, leave it there.
 */
export function focusIsFree(doc: { readonly activeElement: Element | null; readonly body: Element | null }): boolean {
  const el = doc.activeElement;
  return el === null || el === doc.body;
}

function MenuItems({ items, onSelect }: { readonly items: ReadonlyArray<RowMenuItem>; readonly onSelect: ((id: string) => void) | undefined }) {
  return (
    <>
      {items.map((it, i) => {
        if (it.kind === "separator") return <RadixMenu.Separator key={`sep-${i}`} className={styles["separator"]} />;
        if (it.kind === "submenu") {
          const disabled = it.disabled === true;
          return (
            <RadixMenu.Sub key={it.id}>
              <WithReason reason={disabled ? it.disabledReason : undefined}>
                <RadixMenu.SubTrigger className={cx(styles["item"], styles["subTrigger"])} disabled={disabled} data-testid={`rowmenu-${it.id}`}>
                  <ItemBody icon={it.icon} label={it.label} reason={disabled ? it.disabledReason : undefined} />
                  <Icon name="chevron-right" size={12} className={styles["subChevron"]} />
                </RadixMenu.SubTrigger>
              </WithReason>
              <RadixMenu.Portal>
                <RadixMenu.SubContent className={styles["content"]} sideOffset={2} alignOffset={-4} collisionPadding={8} loop onClick={stop} onKeyDown={stopIfActivation} onContextMenu={swallow}>
                  <MenuItems items={it.items} onSelect={onSelect} />
                </RadixMenu.SubContent>
              </RadixMenu.Portal>
            </RadixMenu.Sub>
          );
        }
        const disabled = it.disabled === true;
        return (
          <WithReason key={it.id} reason={disabled ? it.disabledReason : undefined}>
            <RadixMenu.Item
              className={cx(styles["item"], it.tone === "danger" && styles["danger"], it.description && styles["twoLine"])}
              disabled={disabled}
              data-testid={`rowmenu-${it.id}`}
              onSelect={() => {
                it.onSelect?.();
                onSelect?.(it.id);
              }}
            >
              <ItemBody icon={it.icon} leading={it.leading} label={it.label} reason={disabled ? it.disabledReason : undefined} description={it.description} mono={it.mono} />
              {it.shortcut ? (
                <kbd className={styles["shortcut"]} aria-hidden>
                  {it.shortcut}
                </kbd>
              ) : null}
            </RadixMenu.Item>
          </WithReason>
        );
      })}
    </>
  );
}

function ItemBody({ icon, leading, label, reason, description, mono }: {
  readonly icon: IconName | undefined;
  readonly leading?: ReactNode;
  readonly label: string;
  readonly reason: string | undefined;
  readonly description?: string | undefined;
  readonly mono?: boolean | undefined;
}) {
  return (
    <>
      {leading ? <span className={styles["leading"]} aria-hidden>{leading}</span> : (
        <span className={styles["icon"]} aria-hidden>
          {icon ? <Icon name={icon} size={12} /> : null}
        </span>
      )}
      {description ? (
        <span className={styles["label"]}>
          <span className={cx(styles["name"], mono && styles["mono"])}>{label}</span>
          <span className={styles["description"]}>{description}</span>
        </span>
      ) : (
        <span className={cx(styles["label"], mono && styles["mono"])}>{label}</span>
      )}
      {reason ? <span className="ds-sr-only">. {reason}</span> : null}
    </>
  );
}

/** A disabled item explains itself on hover; enabled items need no wrapper. */
function WithReason({ reason, children }: { readonly reason: string | undefined; readonly children: ReactNode }) {
  if (!reason) return <>{children}</>;
  return (
    <Tooltip content={reason} side="right">
      {children}
    </Tooltip>
  );
}

// ---------------------------------------------------------------------------
// Opening a row's menu from the row itself
// ---------------------------------------------------------------------------

/** Shift+F10 or the dedicated context-menu key: the keyboard's right-click. */
export function isContextMenuKey(e: { readonly key: string; readonly shiftKey: boolean }): boolean {
  return e.key === "ContextMenu" || (e.key === "F10" && e.shiftKey);
}

export interface RowMenuOpeners {
  readonly onContextMenu: (e: MouseEvent<HTMLElement>) => void;
  readonly onKeyDown: (e: KeyboardEvent<HTMLElement>) => void;
}

/**
 * Handlers for the row that owns a `RowMenu`: right-click and Shift+F10 /
 * context-menu key open it. Spread on the row and drive the menu's `open`
 * with the same state. Only the menu's own keys are consumed, so a tree's
 * arrow-key handling composes with it untouched.
 */
export function rowMenuOpeners(open: () => void): RowMenuOpeners {
  return {
    onContextMenu: (e) => {
      e.preventDefault();
      open();
    },
    onKeyDown: (e) => {
      if (!isContextMenuKey(e)) return;
      e.preventDefault();
      e.stopPropagation();
      open();
    },
  };
}

export interface RowMenuTriggerProps extends Omit<IconButtonProps, "icon" | "size"> {
  readonly size?: "sm" | "md" | undefined;
}

/**
 * A trigger that sits in a row and stays out of the tab order: the row
 * opens it with Shift+F10. The Radix trigger slot injects its own click
 * and key handlers; they are chained with ours, not replaced.
 */
export const RowMenuTrigger = forwardRef<HTMLButtonElement, RowMenuTriggerProps>(function RowMenuTrigger({ label, size = "sm", className, onClick, onKeyDown, ...rest }, ref) {
  return <IconButton ref={ref} {...rest} icon="more" label={label} size={size} tabIndex={-1} className={cx(styles["trigger"], className)} onClick={chain(onClick, stop)} onKeyDown={chain(onKeyDown, stopIfActivation)} />;
});
