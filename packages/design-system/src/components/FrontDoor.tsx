import type { ButtonHTMLAttributes, HTMLAttributes, ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import styles from "./FrontDoor.module.css";

export interface FrontDoorProps extends Omit<HTMLAttributes<HTMLDivElement>, "title"> {
  /** The app's face, above everything (the design system has none). */
  readonly mark?: ReactNode;
  /** The page's heading, centred. */
  readonly title?: ReactNode;
  /** Under the heading, at the prose measure. */
  readonly line?: ReactNode;
  /** Small centred words at the foot: "Signed in as … · Sign out". */
  readonly footer?: ReactNode;
  /** `outside` (any organisation's): the canvas shade, 640px. `inside`: an organisation's page on the surface shade, 720px. */
  readonly variant?: "outside" | "inside" | undefined;
  readonly children?: ReactNode;
}

/**
 * A page that is one centred column: dude's face, a heading and its line,
 * cards, and fine print at the foot. Outside any organisation (No
 * organisation, You're invited, an invitation's accept page) it sits on
 * the canvas with no sidebar; inside one (Welcome to Northwind) on the
 * surface beside the sidebar. One column on a phone.
 */
export function FrontDoor({ mark, title, line, footer, variant = "outside", className, children, ...rest }: FrontDoorProps) {
  return (
    <div className={cx(styles["page"], variant === "inside" && styles["inside"], className)} {...rest}>
      <div className={styles["column"]}>
        {mark ? <div className={styles["mark"]}>{mark}</div> : null}
        {title || line ? (
          <header className={styles["hero"]}>
            {title ? <h1 className={styles["title"]}>{title}</h1> : null}
            {line ? <p className={styles["line"]}>{line}</p> : null}
          </header>
        ) : null}
        {children}
        {footer ? <div className={styles["fine"]}>{footer}</div> : null}
      </div>
    </div>
  );
}

export interface FrontDoorMessageProps extends Omit<HTMLAttributes<HTMLDivElement>, "title"> {
  /** A face (the organisation's), or a glyph on a sunken disc. */
  readonly lead?: ReactNode;
  readonly icon?: IconName | undefined;
  readonly title: ReactNode;
  readonly line?: ReactNode;
  /** Under the line: what it is about (a KeyValueList), left-aligned in the centre. */
  readonly facts?: ReactNode;
}

/** One message in a card, centred: a face or glyph, the heading (an h1), a line, and its facts. The card's footer holds its actions. */
export function FrontDoorMessage({ lead, icon, title, line, facts, className, ...rest }: FrontDoorMessageProps) {
  return (
    <div className={cx(styles["message"], className)} {...rest}>
      {lead ?? (icon ? <span className={styles["glyph"]} aria-hidden><Icon name={icon} size={20} /></span> : null)}
      <h1 className={styles["messageTitle"]}>{title}</h1>
      {line ? <p className={styles["messageLine"]}>{line}</p> : null}
      {facts ? <div className={styles["facts"]}>{facts}</div> : null}
    </div>
  );
}

/** An action in a front door's fine print, drawn as an inline link: "Sign out". */
export function FrontDoorLink({ className, ...rest }: Omit<ButtonHTMLAttributes<HTMLButtonElement>, "type">) {
  return <button type="button" className={cx(styles["link"], className)} {...rest} />;
}

export interface HowItem {
  readonly icon: IconName;
  readonly title: string;
  readonly body: string;
}

/** How something works, in a few lines: a glyph, a bold lead-in and a muted sentence each. */
export function HowList({ items, label, className, ...rest }: { readonly items: ReadonlyArray<HowItem>; readonly label: string } & HTMLAttributes<HTMLUListElement>) {
  return (
    <ul className={cx(styles["how"], className)} role="list" aria-label={label} {...rest}>
      {items.map((h) => (
        <li key={h.title} className={styles["howItem"]}>
          <Icon name={h.icon} size={16} className={styles["howIcon"]} />
          <span><b>{h.title}.</b> <span className={styles["howBody"]}>{h.body}</span></span>
        </li>
      ))}
    </ul>
  );
}
