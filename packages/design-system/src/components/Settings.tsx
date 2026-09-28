import { type ButtonHTMLAttributes, type HTMLAttributes, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import styles from "./Settings.module.css";

/*
 * The pieces a settings page is made of, shared by the organization's and
 * a project's: a left menu of pages (a page may have sub-pages, as Agents
 * has its roles), a note on who may change what, sections of rows — a
 * label with its help on the left, the control on the right — and, on a
 * project's page, each value's source: "From Acme" or "Overridden", with
 * Reset.
 */

export interface SettingsNavItem {
  readonly id: string;
  readonly label: string;
  readonly icon?: IconName | undefined;
  /** A leading visual in place of the icon (an agent's face). */
  readonly leading?: ReactNode;
  /** Right-aligned: "2 changed", "off". */
  readonly note?: ReactNode;
  /** Sub-pages, shown under it while it or one of them is current. */
  readonly items?: ReadonlyArray<SettingsNavItem> | undefined;
}

export interface SettingsLayoutProps extends Omit<HTMLAttributes<HTMLDivElement>, "onSelect"> {
  /** Above the menu: whose settings these are. */
  readonly scope: { readonly title: ReactNode; readonly subtitle?: ReactNode; readonly leading?: ReactNode };
  readonly items: ReadonlyArray<SettingsNavItem>;
  readonly current: string;
  readonly onSelect: (id: string) => void;
  /** Under the menu: where values not changed here come from. */
  readonly footer?: ReactNode;
  readonly children: ReactNode;
}

export function SettingsLayout({ scope, items, current, onSelect, footer, className, children, ...rest }: SettingsLayoutProps) {
  const open = (item: SettingsNavItem) => item.id === current || Boolean(item.items?.some((s) => s.id === current));
  const row = (item: SettingsNavItem, sub = false) => (
    <button
      key={item.id}
      type="button"
      className={cx(styles["navItem"], sub && styles["navSub"], item.id === current && styles["navCurrent"])}
      aria-current={item.id === current ? "page" : undefined}
      data-settings-nav={item.id}
      onClick={() => onSelect(item.items?.[0] && !sub ? item.items[0].id : item.id)}
    >
      {item.leading ?? (item.icon ? <Icon name={item.icon} size={16} className={styles["navIcon"]} /> : null)}
      <span className={styles["navLabel"]}>{item.label}</span>
      {item.note ? <span className={styles["navNote"]}>{item.note}</span> : null}
    </button>
  );
  return (
    <div className={cx(styles["layout"], className)} {...rest}>
      <nav className={styles["nav"]} aria-label="Settings">
        <div className={styles["scope"]}>
          {scope.leading}
          <span className={styles["scopeText"]}>
            <b>{scope.title}</b>
            {scope.subtitle ? <small>{scope.subtitle}</small> : null}
          </span>
        </div>
        {items.map((item) => (
          <div key={item.id} className={styles["navGroup"]}>
            {row(item)}
            {item.items && open(item) ? item.items.map((s) => row(s, true)) : null}
          </div>
        ))}
        {footer ? <div className={styles["navFooter"]}>{footer}</div> : null}
      </nav>
      <div className={styles["main"]}>{children}</div>
    </div>
  );
}

/** A page's title and what it is for; `leading` is its face (an agent's), `actions` sit on the right. */
export function SettingsHeader({ title, description, leading, actions }: {
  readonly title: ReactNode;
  readonly description?: ReactNode;
  readonly leading?: ReactNode;
  readonly actions?: ReactNode;
}) {
  return (
    <header className={styles["header"]}>
      {leading}
      <div className={styles["headerText"]}>
        <h2 className={styles["title"]}>{title}</h2>
        {description ? <p className={styles["description"]}>{description}</p> : null}
      </div>
      {actions ? <div className={styles["headerActions"]}>{actions}</div> : null}
    </header>
  );
}

/** Who may change these, and where the rest comes from. */
export function SettingsNote({ icon = "info", children }: { readonly icon?: IconName; readonly children: ReactNode }) {
  return (
    <p className={styles["note"]}>
      <Icon name={icon} size={14} className={styles["noteIcon"]} />
      <span>{children}</span>
    </p>
  );
}

export function SettingsSection({ title, actions, children, ...rest }: {
  readonly title?: ReactNode;
  /** On the title's line, at the right. */
  readonly actions?: ReactNode;
  readonly children: ReactNode;
} & Omit<HTMLAttributes<HTMLElement>, "title">) {
  return (
    <section className={styles["section"]} {...rest}>
      {title || actions ? (
        <div className={styles["sectionHead"]}>
          {title ? <h3 className={styles["sectionTitle"]}>{title}</h3> : null}
          {actions ? <div className={styles["sectionActions"]}>{actions}</div> : null}
        </div>
      ) : null}
      {children}
    </section>
  );
}

/** Small muted words beside a control: "Last changed by Eli · yesterday". */
export function SettingsMeta({ children }: { readonly children: ReactNode }) {
  return <span className={styles["meta"]}>{children}</span>;
}

/** A button that reads as a link, inside words: "History". */
export function TextButton({ className, ...rest }: ButtonHTMLAttributes<HTMLButtonElement>) {
  return <button type="button" className={cx(styles["reset"], className)} {...rest} />;
}

/** Settings side by side: a role's model, effort and time limit. */
export function SettingFields({ children }: { readonly children: ReactNode }) {
  return <div className={styles["fields"]}>{children}</div>;
}

/** One setting in a `SettingFields` strip: its label over its control, its source under it. */
export function SettingField({ label, htmlFor, source, children }: {
  readonly label: ReactNode;
  readonly htmlFor?: string | undefined;
  readonly source?: ReactNode;
  readonly children: ReactNode;
}) {
  return (
    <div className={styles["field"]}>
      <label className={styles["fieldLabel"]} htmlFor={htmlFor}>
        {label}
      </label>
      {children}
      {source}
    </div>
  );
}

/** Something inherited, folded: the organization's prompt a project's adds to. */
export function SettingsDisclosure({ summary, children }: { readonly summary: ReactNode; readonly children: ReactNode }) {
  return (
    <details className={styles["disclosure"]}>
      <summary className={styles["disclosureSummary"]}>
        <Icon name="layers" size={14} className={styles["noteIcon"]} />
        <span className={styles["disclosureText"]}>{summary}</span>
        <span className={styles["disclosureShow"]}>Show</span>
      </summary>
      <div className={styles["disclosureBody"]}>{children}</div>
    </details>
  );
}

export interface SettingRowProps extends Omit<HTMLAttributes<HTMLDivElement>, "title"> {
  readonly label: ReactNode;
  readonly help?: ReactNode;
  /** The control's id, so the label names it. */
  readonly htmlFor?: string | undefined;
  /** Beside the control: its source, on a project's page. */
  readonly source?: ReactNode;
  readonly children: ReactNode;
}

export function SettingRow({ label, help, htmlFor, source, className, children, ...rest }: SettingRowProps) {
  return (
    <div className={cx(styles["row"], className)} {...rest}>
      <div className={styles["rowLabel"]}>
        {htmlFor ? <label htmlFor={htmlFor}>{label}</label> : <span>{label}</span>}
        {help ? <small className={styles["rowHelp"]}>{help}</small> : null}
      </div>
      <div className={styles["rowControl"]}>
        {children}
        {source}
      </div>
    </div>
  );
}

export interface SettingSourceProps {
  /** Where the value comes from. */
  readonly source: "organization" | "project";
  /** The organization's name: "From Acme". */
  readonly from: string;
  /** The value underneath, as a person reads it: "Acme: off". */
  readonly inherited?: ReactNode;
  /** Put the organization's value back. */
  readonly onReset?: (() => void) | undefined;
  readonly disabled?: boolean | undefined;
}

/**
 * Where a project's value comes from: its organization ("From Acme"), or
 * the project itself ("Overridden", what it overrides, and Reset). Glyph
 * and word, never colour alone.
 */
export function SettingSource({ source, from, inherited, onReset, disabled }: SettingSourceProps) {
  if (source === "organization") {
    return (
      <span className={styles["inherited"]} data-source="organization">
        <Icon name="layers" size={12} />
        From {from}
      </span>
    );
  }
  return (
    <span className={styles["overridden"]} data-source="project">
      <span className={styles["overriddenTag"]}>Overridden</span>
      {inherited !== undefined ? (
        <span className={styles["overriddenWas"]}>
          {from}: {inherited}
        </span>
      ) : null}
      {onReset ? (
        <button type="button" className={styles["reset"]} onClick={onReset} disabled={disabled}>
          Reset
        </button>
      ) : null}
    </span>
  );
}

export interface SwitchProps {
  readonly checked: boolean;
  readonly onCheckedChange: (checked: boolean) => void;
  readonly label: ReactNode;
  readonly disabled?: boolean | undefined;
  readonly id?: string | undefined;
  readonly testId?: string | undefined;
}

/** On or off, saying which in words beside it. */
export function Switch({ checked, onCheckedChange, label, disabled, id, testId }: SwitchProps) {
  return (
    <label className={styles["switchRow"]}>
      <button
        id={id}
        type="button"
        role="switch"
        aria-checked={checked}
        disabled={disabled}
        className={cx(styles["switch"], checked && styles["switchOn"])}
        onClick={() => onCheckedChange(!checked)}
        data-testid={testId}
      >
        <span className={styles["switchThumb"]} />
      </button>
      <span className={styles["switchLabel"]}>{label}</span>
    </label>
  );
}
