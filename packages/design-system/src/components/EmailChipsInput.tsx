import { useId, useRef, useState, type ClipboardEvent, type KeyboardEvent } from "react";
import { cx } from "../util/cx.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import { IconButton } from "../primitives/Button.tsx";
import { emailChipSummary, parseEmailEntries, type EmailChipKind, type EmailChipView, type EmailEntry } from "../util/emails.ts";
import styles from "./EmailChipsInput.module.css";

/** Each kind's glyph and tone: meaning is in the glyph and the words too, never the colour alone. */
const CHIP: Record<EmailChipKind, { readonly tone: "ok" | "info" | "bad" | "skip"; readonly icon: IconName; readonly words: string }> = {
  valid: { tone: "ok", icon: "check", words: "will be invited" },
  renew: { tone: "info", icon: "retry", words: "will be renewed" },
  invalid: { tone: "bad", icon: "warning", words: "left out" },
  member: { tone: "skip", icon: "human", words: "left out" },
  invited: { tone: "info", icon: "link", words: "left out" },
  duplicate: { tone: "skip", icon: "minus", words: "left out" },
};

export interface EmailChipsInputProps {
  readonly entries: ReadonlyArray<EmailEntry>;
  readonly onChange: (entries: EmailEntry[]) => void;
  /** One view per entry, in order (`emailChipViews`). */
  readonly views: ReadonlyArray<EmailChipView>;
  readonly label?: string | undefined;
  readonly hint?: string | undefined;
  readonly placeholder?: string | undefined;
  /** The summary's word for each kind; `member` is usually "already in <organisation>". */
  readonly summaryWords?: Partial<Record<EmailChipKind, string>> | undefined;
  readonly disabled?: boolean | undefined;
  readonly autoFocus?: boolean | undefined;
  readonly className?: string | undefined;
  readonly "data-testid"?: string | undefined;
}

/**
 * Email addresses as chips, for an invite. Enter, a comma, a space or
 * leaving the field turn what is typed into chips; a paste is split by
 * line, comma and semicolon, keeping `Name <email>` names and a
 * spreadsheet row's name cell. Backspace in the empty field takes the last
 * chip back; each chip has its ✕. Each chip says what will happen to it by
 * glyph and words (the caller's `views`), with at most one inline action,
 * and the line under the field counts them.
 */
export function EmailChipsInput({ entries, onChange, views, label = "Emails", hint, placeholder, summaryWords, disabled, autoFocus, className, ...rest }: EmailChipsInputProps) {
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
    if ((e.key === "Enter" || e.key === "," || e.key === " " || e.key === ";") && draft.trim()) {
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
  const summary = emailChipSummary(views, summaryWords);
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
                <li key={e.key} className={styles["chip"]} data-tone={spec.tone} data-state={view.kind} data-email={e.email ?? undefined} title={shown}>
                  <Icon name={spec.icon} size={12} className={styles["icon"]} />
                  <span className={styles["text"]}>
                    {e.name ? <><b className={styles["name"]}>{e.name}</b> <span className={styles["mail"]}>{e.email}</span></> : <span className={styles["mail"]}>{shown}</span>}
                  </span>
                  {view.note ? <span className={styles["note"]}>{view.note}</span> : null}
                  <span className="ds-sr-only">, {spec.words}</span>
                  {view.action ? (
                    <button type="button" className={styles["action"]} disabled={disabled}
                      onClick={(ev) => { ev.stopPropagation(); view.action!.onClick(); }}>
                      {view.action.label}
                    </button>
                  ) : null}
                  <IconButton size="sm" icon="close" label={`Remove ${shown}`} className={styles["remove"]} disabled={disabled}
                    onClick={(ev) => { ev.stopPropagation(); onChange(entries.filter((x) => x.key !== e.key)); input.current?.focus(); }} />
                </li>
              );
            })}
          </ul>
        ) : null}
        <input ref={input} id={id} className={styles["input"]} value={draft} disabled={disabled} autoFocus={autoFocus}
          placeholder={entries.length ? "" : placeholder} autoComplete="off" spellCheck={false}
          aria-describedby={[hint ? hintId : null, summary ? summaryId : null].filter(Boolean).join(" ") || undefined}
          onChange={(e) => setDraft(e.target.value)} onKeyDown={onKey} onBlur={() => draft.trim() && commit(draft)} onPaste={onPaste} />
      </div>
      {hint ? <small id={hintId} className={styles["hint"]}>{hint}</small> : null}
      <p id={summaryId} className={styles["summary"]} aria-live="polite" data-testid="chip-summary">{summary}</p>
    </div>
  );
}
