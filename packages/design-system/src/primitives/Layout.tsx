import { useId, type FieldsetHTMLAttributes, type HTMLAttributes, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import styles from "./Layout.module.css";
// The field anatomy (label, hint, error) is Input's, as Textarea's is.
import field from "./Input.module.css";

/*
 * The structure of a page outside the board and the transcript — a work
 * item, settings, an inbox — so every one of them is laid out, spaced and
 * sized the same way, and follows the density. Content (lists, cards, chat)
 * comes from the components; this is only the frame.
 */

export interface PageProps extends HTMLAttributes<HTMLDivElement> {
  /** "page" (the default) keeps to a readable width; "full" takes the pane. */
  readonly width?: "page" | "full" | undefined;
}

/** A page: its header and sections, stacked with the density's panel gap. */
export function Page({ width = "page", className, children, ...rest }: PageProps) {
  return (
    <div className={cx(styles["page"], width === "full" && styles["full"], className)} {...rest}>
      {children}
    </div>
  );
}

export interface PageHeaderProps extends Omit<HTMLAttributes<HTMLElement>, "title"> {
  readonly title: ReactNode;
  /** Before the title, muted mono: an identifier ("TEXT-20"). */
  readonly itemKey?: ReactNode;
  /** Before the id: a status, as a StatusBadge. */
  readonly status?: ReactNode;
  /** Above the title: where this is (a Breadcrumb). Replaces a Back button. */
  readonly breadcrumb?: ReactNode;
  /** Under the title, secondary: what it is, in a sentence or two. */
  readonly description?: ReactNode;
  /** On the right of the title row. */
  readonly actions?: ReactNode;
}

/** What a page is about: where it is, its title, and what can be done with it. */
export function PageHeader({ title, itemKey, status, breadcrumb, description, actions, className, children, ...rest }: PageHeaderProps) {
  return (
    <header className={cx(styles["header"], className)} {...rest}>
      {breadcrumb}
      <div className={styles["titleRow"]}>
        {status}
        {itemKey ? <span className={cx(styles["id"], "ds-mono")}>{itemKey}</span> : null}
        <h1 className={styles["title"]}>{title}</h1>
        {actions ? <div className={styles["actions"]}>{actions}</div> : null}
      </div>
      {description ? <div className={styles["description"]}>{description}</div> : null}
      {children}
    </header>
  );
}

export interface SectionProps extends Omit<HTMLAttributes<HTMLElement>, "title"> {
  /** Small caps, as every section of a page is titled. */
  readonly title?: ReactNode;
  /** After the title, muted: how many. */
  readonly count?: number | undefined;
  /** On the right of the title. */
  readonly actions?: ReactNode;
}

/** A titled part of a page. */
export function Section({ title, count, actions, className, children, ...rest }: SectionProps) {
  const titleId = useId();
  return (
    <section className={cx(styles["section"], className)} aria-labelledby={title ? titleId : undefined} {...rest}>
      {title || actions ? (
        <div className={styles["sectionHead"]}>
          {title ? (
            <h2 id={titleId} className={cx(styles["sectionTitle"], "ds-label")}>
              {title}
              {count !== undefined ? <span className={styles["sectionCount"]}>{count}</span> : null}
            </h2>
          ) : null}
          {actions ? <div className={styles["actions"]}>{actions}</div> : null}
        </div>
      ) : null}
      {children}
    </section>
  );
}

export type CalloutTone = "danger" | "attention" | "info" | "success" | "neutral";

const CALLOUT_ICON: Record<CalloutTone, IconName> = {
  danger: "alert",
  attention: "warning",
  info: "info",
  success: "check",
  neutral: "info",
};

export interface CalloutProps extends HTMLAttributes<HTMLDivElement> {
  readonly tone?: CalloutTone | undefined;
}

/**
 * A message in the page, where it applies: why a save was refused, a
 * connection that failed. Danger is announced at once (role=alert); the
 * other tones politely (role=status). Not for agent events — those are in
 * the transcript — and not for the outcome of an action, which is a toast.
 */
export function Callout({ tone = "neutral", className, children, ...rest }: CalloutProps) {
  return (
    <div role={tone === "danger" ? "alert" : "status"} data-tone={tone} className={cx(styles["callout"], className)} {...rest}>
      <Icon name={CALLOUT_ICON[tone]} size={14} className={styles["calloutIcon"]} />
      <div className={styles["calloutBody"]}>{children}</div>
    </div>
  );
}

export interface KeyValueListProps extends HTMLAttributes<HTMLDListElement> {
  readonly items: ReadonlyArray<{ readonly label: ReactNode; readonly value: ReactNode; readonly mono?: boolean | undefined }>;
}

/** Facts about a thing, a label and a value to a line. Mono for identifiers, URLs, paths. */
export function KeyValueList({ items, className, ...rest }: KeyValueListProps) {
  return (
    <dl className={cx(styles["kv"], className)} {...rest}>
      {items.map((it, i) => (
        <div key={i} className={styles["kvRow"]}>
          <dt className={styles["kvLabel"]}>{it.label}</dt>
          <dd className={cx(styles["kvValue"], it.mono && "ds-mono")}>{it.value}</dd>
        </div>
      ))}
    </dl>
  );
}

/**
 * Fields stacked, at a width a form reads well at. `fill`: the column's
 * whole width, a section's space apart — the fields of one document being
 * written, in a `Dialog size="document"`.
 */
export function FormStack({ fill, className, children, ...rest }: HTMLAttributes<HTMLDivElement> & { readonly fill?: boolean | undefined }) {
  return (
    <div className={cx(styles["form"], fill && styles["formFill"], className)} {...rest}>
      {children}
    </div>
  );
}

/** Two or more fields side by side, sharing the row equally. */
export function FormRow({ className, children, ...rest }: HTMLAttributes<HTMLDivElement>) {
  return (
    <div className={cx(styles["formRow"], className)} {...rest}>
      {children}
    </div>
  );
}

export interface FormActionsProps extends HTMLAttributes<HTMLDivElement> {
  /** Beside the buttons, muted: what saving does, or does not do. */
  readonly note?: ReactNode;
}

/** A form's buttons, primary first, and a note beside them. */
export function FormActions({ note, className, children, ...rest }: FormActionsProps) {
  return (
    <div className={cx(styles["formActions"], className)} {...rest}>
      {children}
      {note ? <span className={styles["formNote"]}>{note}</span> : null}
    </div>
  );
}

export interface FieldsetProps extends Omit<FieldsetHTMLAttributes<HTMLFieldSetElement>, "title"> {
  /** What the group is, labelled as a field is. */
  readonly legend: ReactNode;
  readonly hint?: ReactNode;
  readonly error?: ReactNode;
}

/** A group of controls answering one question: checkboxes, a list with an add button. */
export function Fieldset({ legend, hint, error, className, children, ...rest }: FieldsetProps) {
  const id = useId();
  return (
    <fieldset className={cx(styles["fieldset"], className)} aria-describedby={hint || error ? `${id}-hint` : undefined} {...rest}>
      <legend className={cx(field["label"], styles["legend"])}>{legend}</legend>
      {hint || error ? (
        <p id={`${id}-hint`} className={cx(field["hint"], styles["hint"], error ? field["hintError"] : undefined)}>
          {error ?? hint}
        </p>
      ) : null}
      {children}
    </fieldset>
  );
}
