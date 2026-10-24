import { useId, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { firstName } from "../util/format.ts";
import { Icon } from "../icons/index.tsx";
import { Button } from "../primitives/Button.tsx";
import { Callout } from "../primitives/Layout.tsx";
import { Select } from "../primitives/Select.tsx";
import { Kbd } from "../primitives/Kbd.tsx";
import { PersonAvatar, type Person } from "./PersonAvatar.tsx";
import styles from "./SessionAccess.module.css";

/*
 * Who may open a session, and who a message names: the Share dialog's
 * General access row (one way, confirmed), and the @-mention parts of the
 * session's composer and transcript.
 */

/** The one-way warning, in the confirm and on the read-only row. */
export function oneWay(org: string): string {
  return `Everyone in ${org} will be able to read and chat. This can't be undone.`;
}

export interface GeneralAccessRowProps {
  readonly org: string;
  readonly value: "private" | "organisation";
  /** Whoever acts as owner: a private session's row is a Select for them. */
  readonly editable?: boolean | undefined;
  /** The owner chose Everyone: the caller asks to confirm (`confirming`). */
  readonly onOpen?: (() => void) | undefined;
  /** The confirm is shown: the Select reads Everyone while it is. */
  readonly confirming?: boolean | undefined;
  /** Confirmed: open it. */
  readonly onConfirm?: (() => void) | undefined;
  /** Not confirmed: the row reads Private again. */
  readonly onCancel?: (() => void) | undefined;
  readonly busy?: boolean | undefined;
}

/**
 * Above a session's people in the Share dialog: who may open it without
 * being added. One way: on a private session the owner gets a Select whose
 * Everyone option says it can't be undone, and choosing it asks to confirm.
 * Once open to the organisation the row is read-only for everyone, the
 * owner included.
 */
export function GeneralAccessRow({ org, value, editable, onOpen, confirming, onConfirm, onCancel, busy }: GeneralAccessRowProps) {
  const labelId = useId();
  const everyone = value === "organisation";
  return (
    <>
      <section className={styles["access"]} aria-labelledby={labelId} data-testid="general-access" data-access={value}>
        <span className={cx(styles["accessGlyph"], everyone && styles["accessGlyphOrg"])}><Icon name={everyone ? "building" : "lock"} size={14} /></span>
        <div className={styles["accessBody"]}>
          <span id={labelId} className={styles["accessLabel"]}>General access</span>
          {everyone ? <b className={styles["accessValue"]}>Everyone in {org}</b> : editable ? (
            <Select size="sm" aria-label="General access" value={confirming ? "organisation" : "private"} disabled={busy}
              data-testid="general-access-select"
              onValueChange={(v) => v === "organisation" && !confirming && onOpen?.()}
              options={[
                { value: "private", label: "Private" },
                { value: "organisation", label: `Everyone in ${org}`, description: "Can't be undone" },
              ]} />
          ) : <b className={styles["accessValue"]}>Private</b>}
          <span className={styles["accessHelp"]}>
            {everyone ? <>Every active member of {org} can open it and chat, without being added. <b>This can't be undone.</b></>
              : "Only the people below can open it."}
          </span>
        </div>
        {everyone ? <span className={styles["accessRole"]}>Can chat</span> : null}
      </section>
      {!everyone && editable && confirming ? (
        <Callout tone="attention" data-testid="general-access-confirm">
          <p className={styles["confirmText"]}>{oneWay(org)}</p>
          <span className={styles["actions"]}>
            <Button size="sm" variant="primary" disabled={busy} onClick={onConfirm} data-testid="general-access-open">
              Open to everyone in {org}
            </Button>
            <Button size="sm" variant="quiet" disabled={busy} onClick={onCancel}>Keep it private</Button>
          </span>
        </Callout>
      ) : null}
    </>
  );
}

// ---------------------------------------------------------------------------
// Mentions
// ---------------------------------------------------------------------------

export interface MentionItem {
  readonly person: Person;
  /** What mentioning them means: "In this session · can chat", "Not in it yet · can open it and chat". */
  readonly detail: string;
  /** A private session's non-member: shown locked, "Not in this session". */
  readonly outside?: boolean | undefined;
}

export interface MentionMenuProps {
  /** The listbox's id: the composer's `aria-controls`. */
  readonly id: string;
  readonly items: ReadonlyArray<MentionItem>;
  /** The highlighted option's index: the composer's `aria-activedescendant`. */
  readonly active: number;
  /** What was typed after "@": the empty list says so. */
  readonly query: string;
  readonly onPick: (item: MentionItem) => void;
  readonly onHover?: ((index: number) => void) | undefined;
  /** Its heading: "Mention someone in Acme · they're notified". */
  readonly heading?: ReactNode;
}

/** The option id of a menu's nth item, for `aria-activedescendant`. */
export function mentionOptionId(menuId: string, index: number): string {
  return `${menuId}-opt-${index}`;
}

/**
 * The listbox under the composer when "@" starts a word: face, name and
 * what mentioning them means, with the keys under it. The composer keeps
 * focus; a pointer press picks without taking it.
 */
export function MentionMenu({ id, items, active, query, onPick, onHover, heading }: MentionMenuProps) {
  return (
    <div className={styles["menu"]} data-testid="mention-menu">
      {heading ? <div className={styles["menuHead"]}>{heading}</div> : null}
      <ul id={id} role="listbox" aria-label="People to mention" className={styles["menuList"]}>
        {items.length === 0 ? <li className={styles["menuEmpty"]} role="presentation">Nobody called “{query}”</li> : items.map((it, i) => (
          <li key={it.person.id ?? it.person.name} id={mentionOptionId(id, i)} role="option" aria-selected={i === active}
            className={cx(styles["option"], i === active && styles["optionActive"])} data-outside={it.outside ? "true" : undefined}
            onMouseDown={(e) => { e.preventDefault(); onPick(it); }} onMouseEnter={() => onHover?.(i)}>
            <PersonAvatar person={it.person} size={24} aria-hidden title="" />
            <span className={styles["optionName"]}>{it.person.name}</span>
            {it.outside ? (
              <span className={styles["outside"]}><Icon name="lock" size={12} />Not in this session</span>
            ) : <span className={styles["optionDetail"]}>{it.detail}</span>}
          </li>
        ))}
      </ul>
      <div className={styles["menuKeys"]} aria-hidden>
        <Kbd>↑</Kbd><Kbd>↓</Kbd> move · <Kbd>Enter</Kbd> pick · <Kbd>Esc</Kbd> close
      </div>
    </div>
  );
}

/** A mention in a sent message: the face and "@First" on the info wash; `you` when it is the reader. */
export function MentionChip({ person, you }: { readonly person: Person; readonly you?: boolean | undefined }) {
  return (
    <span className={cx(styles["chip"], you && styles["chipYou"])} role="img" aria-label={`Mention: ${person.name}${you ? " (you)" : ""}`}
      data-testid="mention-chip">
      <PersonAvatar person={person} size={16} aria-hidden title="" />
      <span aria-hidden>@{firstName(person.name)}</span>
    </span>
  );
}

export interface MentionShareConfirmProps {
  readonly person: Person;
  /** Whoever may share it: offered Share and send. */
  readonly owner: boolean;
  readonly ownerName: string;
  readonly onShare?: (() => void) | undefined;
  readonly onPlain: () => void;
  readonly onCancel: () => void;
  readonly busy?: boolean | undefined;
}

/**
 * Under the composer, after a pick of someone who can't open a private
 * session. The owner may share it with them and send; anyone else is told
 * who can, and may send the name as plain text.
 */
export function MentionShareConfirm({ person, owner, ownerName, onShare, onPlain, onCancel, busy }: MentionShareConfirmProps) {
  const first = firstName(person.name);
  return (
    <div role="group" aria-label={`Mention ${person.name}`} className={styles["confirm"]} data-testid="mention-share-confirm"
      data-owner={owner ? "true" : "false"}>
      <p className={styles["confirmLine"]}>
        <PersonAvatar person={person} size={24} aria-hidden title="" />
        {owner ? <span><b>Share this session with {first} so they can see the mention?</b> They'll be able to chat.</span>
          : <span><b>{first} isn't in this session, so they won't see the mention.</b> Ask {firstName(ownerName)} to share it.</span>}
      </p>
      <span className={styles["actions"]}>
        {owner ? <Button size="sm" variant="primary" disabled={busy} onClick={onShare} data-testid="mention-share-send">Share and send</Button> : null}
        <Button size="sm" variant={owner ? "secondary" : "primary"} disabled={busy} onClick={onPlain} data-testid="mention-send-plain">
          {owner ? "Send without sharing" : "Send as plain text"}
        </Button>
        <Button size="sm" variant="quiet" disabled={busy} onClick={onCancel}>Cancel</Button>
      </span>
      {owner ? <p className={styles["confirmNote"]}>Without sharing, “{first}” stays in the text and nobody is notified.</p> : null}
    </div>
  );
}
