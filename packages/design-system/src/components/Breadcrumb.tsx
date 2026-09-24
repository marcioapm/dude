import { Fragment, type HTMLAttributes, type MouseEvent } from "react";
import { cx } from "../util/cx.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import styles from "./Breadcrumb.module.css";

export interface BreadcrumbItem {
  readonly id: string;
  readonly label: string;
  /** A glyph before the label: `layers` for an epic. */
  readonly icon?: IconName | undefined;
  /** Monospace label — a work item key. */
  readonly mono?: boolean | undefined;
  /** Rendered as an anchor when given; a button when only `onSelect` is; plain text when neither. */
  readonly href?: string | undefined;
  readonly onSelect?: (() => void) | undefined;
}

export interface BreadcrumbProps extends Omit<HTMLAttributes<HTMLElement>, "children" | "onSelect"> {
  readonly items: ReadonlyArray<BreadcrumbItem>;
  /** Fires for any crumb, after its own `onSelect`. */
  readonly onSelect?: ((item: BreadcrumbItem) => void) | undefined;
  /** Character budget for a crumb before it is elided in the middle; the last crumb keeps its full text. Default 32. */
  readonly maxChars?: number | undefined;
  readonly size?: "sm" | "md" | undefined;
}

/**
 * Where you are: Project › Epic › KEY. Every crumb but the last is a link
 * or a button; the last is the current place and is `aria-current`. Long
 * middle crumbs elide in the middle (`Webhook re…bility`) so the head and
 * tail both survive; the full text lives in the title. The last crumb is
 * never elided: it is the answer to "where am I".
 */
export function Breadcrumb({ items, onSelect, maxChars = 32, size = "md", className, ...rest }: BreadcrumbProps) {
  return (
    <nav aria-label="Breadcrumb" className={cx(styles["root"], size === "sm" && styles["sm"], className)} {...rest}>
      <ol className={styles["list"]}>
        {items.map((it, i) => {
          const last = i === items.length - 1;
          const text = last ? it.label : elideMiddle(it.label, maxChars);
          const elided = text !== it.label;
          const inner = (
            <>
              {it.icon ? <Icon name={it.icon} size={12} className={styles["glyph"]} /> : null}
              <span className={cx(styles["text"], it.mono && styles["mono"])}>{text}</span>
            </>
          );
          // The eye gets the elided text; the screen reader gets the whole label.
          const title = elided ? it.label : undefined;
          const ariaLabel = elided ? it.label : undefined;
          const activate = (e: MouseEvent) => {
            if (it.href && (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0)) return;
            if (it.href && (it.onSelect || onSelect)) e.preventDefault();
            it.onSelect?.();
            onSelect?.(it);
          };
          const clickable = it.href !== undefined || it.onSelect !== undefined || onSelect !== undefined;
          return (
            <Fragment key={it.id}>
              {i > 0 ? (
                <li aria-hidden className={styles["sep"]}>
                  <Icon name="chevron-right" size={12} />
                </li>
              ) : null}
              <li className={cx(styles["item"], last && styles["current"])}>
                {last || !clickable ? (
                  <span className={styles["crumb"]} aria-current={last ? "page" : undefined} title={title} aria-label={ariaLabel}>
                    {inner}
                  </span>
                ) : it.href ? (
                  <a className={cx(styles["crumb"], styles["link"])} href={it.href} title={title} aria-label={ariaLabel} onClick={activate}>
                    {inner}
                  </a>
                ) : (
                  <button type="button" className={cx(styles["crumb"], styles["link"])} title={title} aria-label={ariaLabel} onClick={activate}>
                    {inner}
                  </button>
                )}
              </li>
            </Fragment>
          );
        })}
      </ol>
    </nav>
  );
}

/** "Webhook reliability and retries" at 16 → "Webhook…retries". Keeps the head and the tail. */
export function elideMiddle(text: string, maxChars: number): string {
  const chars = Array.from(text);
  if (maxChars < 3 || chars.length <= maxChars) return text;
  const keep = maxChars - 1;
  const head = Math.ceil(keep / 2);
  const tail = keep - head;
  return `${chars.slice(0, head).join("").trimEnd()}…${chars.slice(chars.length - tail).join("").trimStart()}`;
}
