import { useId, useMemo, useRef, useState, type ClipboardEvent, type KeyboardEvent } from "react";
import { cx } from "../util/cx.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import { IconButton } from "../primitives/Button.tsx";
import { emailChipSummary, parseEmailEntries, TAKEN, type EmailChipKind, type EmailChipView, type EmailEntry } from "../util/emails.ts";
import styles from "./EmailChipsInput.module.css";

/**
 * Each kind's tone and glyph. A chip shows the address only; its tone says
 * whether it is taken (plain), renewed (glyph), not an email (danger) or
 * left out (struck through). The reason is said in words once, in the list
 * under the field (the view's note, else `reason`), and to a screen reader
 * on the chip itself (`words`).
 */
const CHIP: Record<EmailChipKind, { readonly tone: "ok" | "info" | "bad" | "skip"; readonly icon: IconName | null; readonly words: string; readonly reason: string }> = {
  valid: { tone: "ok", icon: null, words: "will be invited", reason: "will be invited" },
  renew: { tone: "info", icon: "retry", words: "will be renewed", reason: "will be renewed" },
  invalid: { tone: "bad", icon: "warning", words: "left out", reason: "not an email" },
  member: { tone: "skip", icon: "human", words: "left out", reason: "left out" },
  invited: { tone: "skip", icon: "link", words: "left out", reason: "left out" },
  duplicate: { tone: "skip", icon: "minus", words: "left out", reason: "twice in the list" },
};

export interface EmailChipsInputProps {
  readonly entries: ReadonlyArray<EmailEntry>;
  readonly onChange: (entries: EmailEntry[]) => void;
  /** One view per entry, in order (`emailChipViews`). */
  readonly views: ReadonlyArray<EmailChipView>;
  readonly label?: string | undefined;
  readonly hint?: string | undefined;
  readonly placeholder?: string | undefined;
  readonly disabled?: boolean | undefined;
  readonly autoFocus?: boolean | undefined;
  readonly className?: string | undefined;
  readonly "data-testid"?: string | undefined;
}

/**
 * Email addresses as chips, for an invite. Enter, a comma, a semicolon, a
 * space, Tab or leaving the field turn what is typed into chips; a paste is
 * split by line, comma and semicolon, keeping `Name <email>` names and a
 * spreadsheet row's name cell. Backspace in the empty field takes the last
 * chip back; each chip has its ✕. The box is one control tall and grows with
 * its chips. Under it: the count line, then every address that is left out
 * or renewed with its reason and at most one inline action, and "Remove left
 * out". The hint shows only while the field is empty.
 */
export function EmailChipsInput({ entries, onChange, views, label = "Emails", hint, placeholder, disabled, autoFocus, className, ...rest }: EmailChipsInputProps) {
  const [draft, setDraft] = useState("");
  const input = useRef<HTMLInputElement>(null);
  const id = useId();
  const hintId = `${id}-hint`;
  const summaryId = `${id}-summary`;
  const commit = (text: string) => {
    const got = parseEmailEntries(text);
    if (got.length) onChange([...entries, ...got]);
    setDraft("");
  };
  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if ((e.key === "Enter" || e.key === "," || e.key === " " || e.key === ";" || e.key === "Tab") && draft.trim()) {
      e.preventDefault();
      commit(draft);
    } else if (e.key === "Enter") {
      // An empty field's Enter is the dialog's, not a chip.
    } else if (e.key === "Backspace" && draft === "" && entries.length) {
      e.preventDefault();
      onChange(entries.slice(0, -1));
    }
  };
  const onPaste = (e: ClipboardEvent<HTMLInputElement>) => {
    e.preventDefault();
    commit(draft + e.clipboardData.getData("text"));
  };
  const summary = emailChipSummary(views);
  const explained = entries.map((e, i) => ({ e, view: views[i] ?? { kind: "valid" as const } })).filter(({ view }) => view.kind !== "valid");
  const leftOut = explained.some(({ view }) => !TAKEN.has(view.kind));
  const showHint = Boolean(hint) && entries.length === 0;
  // Typing changes only the draft; the reasons list depends on the entries and their views alone.
  const reasons = useMemo(() => explained.length ? (
        <ul className={styles["reasons"]} aria-label="Left out or renewed" data-testid="chip-reasons">
          {explained.map(({ e, view }) => {
            const spec = CHIP[view.kind];
            return (
              <li key={e.key} className={styles["reason"]} data-tone={spec.tone} data-reason={view.kind}>
                {spec.icon ? <Icon name={spec.icon} size={12} className={styles["icon"]} /> : null}
                <span className={styles["reasonMail"]}>{e.email ?? e.raw}</span>
                <span className={styles["reasonWhy"]}>{view.note ?? spec.reason}</span>
                {view.action ? (
                  <button type="button" className={styles["action"]} disabled={disabled} onClick={() => view.action!.onClick()}>
                    {view.action.label}
                  </button>
                ) : null}
              </li>
            );
          })}
        </ul>
  ) : null, [entries, views, disabled]);
  return (
    <div className={cx(styles["field"], className)} data-testid={rest["data-testid"]}>
      <label className={styles["label"]} htmlFor={id}>{label}</label>
      <div className={styles["box"]} data-disabled={disabled ? "true" : undefined} onClick={() => input.current?.focus()}>
        {entries.length ? (
          <ul className={styles["chips"]} role="list" aria-label={label}>
            {entries.map((e, i) => {
              const view = views[i] ?? { kind: "valid" as const };
              const spec = CHIP[view.kind];
              const shown = e.email ?? e.raw;
              return (
                <li key={e.key} className={styles["chip"]} data-tone={spec.tone} data-state={view.kind} data-email={e.email ?? undefined}
                  title={shown}>
                  {spec.icon ? <Icon name={spec.icon} size={12} className={styles["icon"]} /> : null}
                  <span className={styles["text"]}>
                    {e.name ? <><b className={styles["name"]}>{e.name}</b> <span className={styles["mail"]}>{e.email}</span></> : <span className={styles["mail"]}>{shown}</span>}
                  </span>
                  <span className="ds-sr-only">, {view.note ? `${view.note}, ` : ""}{spec.words}</span>
                  <IconButton size="sm" icon="close" label={`Remove ${shown}`} className={styles["remove"]} disabled={disabled}
                    onClick={(ev) => { ev.stopPropagation(); onChange(entries.filter((x) => x.key !== e.key)); input.current?.focus(); }} />
                </li>
              );
            })}
          </ul>
        ) : null}
        <input ref={input} id={id} className={styles["input"]} value={draft} disabled={disabled} autoFocus={autoFocus}
          placeholder={entries.length ? "" : placeholder} autoComplete="off" spellCheck={false}
          aria-describedby={[showHint ? hintId : null, summary ? summaryId : null].filter(Boolean).join(" ") || undefined}
          onChange={(e) => setDraft(e.target.value)} onKeyDown={onKey} onBlur={() => draft.trim() && commit(draft)} onPaste={onPaste} />
      </div>
      {showHint ? <small id={hintId} className={styles["hint"]}>{hint}</small> : null}
      <div className={styles["summaryRow"]} hidden={!summary}>
        <p id={summaryId} className={styles["summary"]} aria-live="polite" data-testid="chip-summary">{summary}</p>
        {leftOut ? (
          <button type="button" className={styles["action"]} disabled={disabled} data-testid="chip-remove-left-out"
            onClick={() => { onChange(entries.filter((_, i) => TAKEN.has((views[i] ?? { kind: "valid" }).kind))); input.current?.focus(); }}>
            Remove left out
          </button>
        ) : null}
      </div>
      {reasons}
    </div>
  );
}
