import { useEffect, useId, useRef, useState, type HTMLAttributes, type ReactNode } from "react";
import type { TaskStatus } from "@dude/domain";
import { cx } from "../util/cx.ts";
import { Checkbox } from "../primitives/Checkbox.tsx";
import { Button } from "../primitives/Button.tsx";
import { Icon } from "../icons/index.tsx";
import { statusSpec } from "../tokens/status.ts";
import { firstName } from "../util/format.ts";
import { PersonAvatar, type Person } from "./PersonAvatar.tsx";
import { ProjectAvatar } from "./ProjectAvatar.tsx";
import { Markdown } from "./Markdown.tsx";
import { RefLead } from "./RefLead.tsx";
import { StatusMark } from "./StatusMark.tsx";
import styles from "./Brainstorm.module.css";

/*
 * A brainstorm session's parts: the proposal card its agent fills and a
 * member files from, a session's row in the list, its people and what it
 * reads in the rail, and the marker for a session someone else is in too.
 */

// ---------------------------------------------------------------------------
// The session's name
// ---------------------------------------------------------------------------

export interface SessionTitleProps {
  /** null until its agent or a member names it: shown as `untitled`. */
  readonly title: string | null;
  readonly untitled?: string | undefined;
  /** Given, a member who can chat may rename it; a reader's title is plain words. Resolves once saved; a rejection keeps the field open. */
  readonly onRename?: ((title: string) => Promise<void>) | undefined;
  readonly maxLength?: number | undefined;
}

/**
 * A session's name in its header. Untitled, it reads "Untitled session" in
 * muted ink. With `onRename`, the name is a button: pressing it edits the
 * name in place, Enter saves, Escape (or an unchanged name) cancels.
 */
export function SessionTitle({ title, untitled = "Untitled session", onRename, maxLength = 200 }: SessionTitleProps) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState(false);
  const button = useRef<HTMLButtonElement>(null);
  const field = useRef<HTMLInputElement>(null);
  // Set when Enter or Escape ends the edit: the button that replaces the field takes the focus back.
  // A blur, or a save that resolves after the focus moved elsewhere, leaves it unset.
  const refocus = useRef(false);
  useEffect(() => {
    if (editing || !refocus.current) return;
    refocus.current = false;
    button.current?.focus();
  }, [editing]);
  const shown = title ?? untitled;
  // The whole name in the tooltip, for when the line cuts it.
  const words = <span className={cx(styles["titleWords"], title === null && styles["untitled"])} data-title-words=""
    title={shown} data-untitled={title === null || undefined}>{shown}</span>;
  if (!onRename) return <span className={styles["title"]} data-testid="session-title">{words}</span>;
  if (!editing) {
    return (
      <button ref={button} type="button" className={cx(styles["title"], styles["titleEdit"])} data-testid="session-title"
        aria-label={`Rename “${shown}”`} onClick={() => {
          refocus.current = false;
          setDraft(title ?? "");
          setEditing(true);
        }}>
        {words}
        <Icon name="edit" size={13} className={styles["titleGlyph"]} />
      </button>
    );
  }
  const close = (fromKeyboard: boolean) => {
    refocus.current = fromKeyboard;
    setEditing(false);
  };
  // While a save is pending the disabled field may drop the focus to the body; any other element
  // holding it means the user moved on, and a late save must not pull the focus back.
  const focusLeftAlone = () => {
    const active = document.activeElement;
    return !active || active === document.body || active === field.current || active === field.current?.parentElement;
  };
  const save = async () => {
    const next = draft.trim().replace(/\s+/g, " ");
    if (!next || next === title) {
      close(true);
      return;
    }
    setBusy(true);
    try {
      await onRename(next);
      close(focusLeftAlone());
    } catch {
      // The caller says why; the field stays open with what was typed.
    } finally {
      setBusy(false);
    }
  };
  return (
    <input ref={field} className={styles["titleInput"]} aria-label="Session name" data-testid="session-title-input" autoFocus
      // As wide as the name being typed (and the placeholder's room), up to the header's line.
      size={Math.max(draft.length, untitled.length) + 2}
      value={draft} maxLength={maxLength} disabled={busy} placeholder={untitled}
      onChange={(e) => setDraft(e.target.value)}
      onBlur={() => !busy && close(false)}
      onKeyDown={(e) => {
        if (e.key === "Enter") {
          e.preventDefault();
          void save();
        } else if (e.key === "Escape") {
          e.preventDefault();
          close(true);
        }
      }} />
  );
}

// ---------------------------------------------------------------------------
// The proposal card
// ---------------------------------------------------------------------------

export type ProposalKind = "epic" | "task" | "edit" | "comment" | "note" | "relation";

/** A relation an item proposes: to another item of the card by number, or to an existing task by key and status. */
export interface ProposalRelation {
  readonly kind: "blocked-by" | "blocks" | "related";
  readonly item?: number | undefined;
  readonly task?: { readonly key: string; readonly status: TaskStatus } | undefined;
}

export interface ProposalCardItem {
  readonly kind: ProposalKind;
  /** The title: a new epic's or task's, or what an edit, comment or note is on ("Edit BL-58 · Daily rollup", "Note on Usage"). */
  readonly title: ReactNode;
  /** One line under it: a task's goal, an epic's description, the comment's words. */
  readonly detail?: ReactNode;
  /** A proposed epic's notes, pinned for its agents when it is filed: Markdown, in order. */
  readonly notes?: ReadonlyArray<string> | undefined;
  /** An edit's text before and after. */
  readonly before?: ReactNode;
  readonly after?: ReactNode;
  /** Where it goes: the project (new work), or the task's key. */
  readonly project?: { readonly key: string; readonly name: string } | undefined;
  readonly taskKey?: string | undefined;
  /** Under an epic proposed on the same card. */
  readonly child?: boolean | undefined;
  /** A small tag beside the title: "Márcio's · not started". */
  readonly tag?: ReactNode;
  /** The person looking may file it. */
  readonly canFile: boolean;
  /**
   * Whom an item the person looking can't file is blocked for: `"you"`,
   * when someone else may file it (its task's owner, or a member when you
   * only read); `"everyone"`, when nobody can now (its task started, its
   * project unlinked). Unset reads as `"everyone"`: the card stays open
   * rather than fold a problem away.
   */
  readonly blockedFor?: "you" | "everyone" | undefined;
  /** Why not, said on the item ("Only Márcio can file this: it's his task"). */
  readonly why?: string | undefined;
  /** Filed already: by whom, as what ("Ana filed BL-61"). */
  readonly filed?: { readonly by: string; readonly key: string } | undefined;
  /** Its number on the card, shown when the card has relations. */
  readonly n?: number | undefined;
  /** What it waits on, holds up and relates to, under its goal. */
  readonly relations?: ReadonlyArray<ProposalRelation> | undefined;
  /** Ticked with these items (their numbers), which wait on it: "Ticked with 2 and 3: they wait on it." */
  readonly tickedWith?: ReadonlyArray<number> | undefined;
}

export interface ProposalCardProps extends Omit<HTMLAttributes<HTMLElement>, "title" | "onChange" | "onToggle"> {
  readonly items: ReadonlyArray<ProposalCardItem>;
  /** The items ticked, by index. Only items that can be filed are tickable. */
  readonly selected: ReadonlySet<number>;
  readonly onToggle: (index: number) => void;
  /** File the ticked items, as the person looking. */
  readonly onFile: () => void;
  /** Who files ("Ana"): the footer says what filing as them does. */
  readonly filingAs: string;
  readonly busy?: boolean | undefined;
  /** A reader: the card shows, and files nothing. */
  readonly readOnly?: boolean | undefined;
  /** A folded card starts open: the initial state of its line's toggle. */
  readonly defaultOpen?: boolean | undefined;
  /** The order the items' relations put them in, steps of item numbers: "Order 1 → 2 3". */
  readonly order?: ReadonlyArray<ReadonlyArray<number>> | undefined;
}

const KIND_WORD: Record<ProposalKind, [string, string]> = {
  epic: ["epic", "epics"], task: ["task", "tasks"], edit: ["edit", "edits"], comment: ["comment", "comments"], note: ["note", "notes"], relation: ["relation", "relations"],
};

/** "2 tasks, 1 edit": what a set of items is, by kind, in the card's order. */
export function proposalSummary(kinds: ReadonlyArray<ProposalKind>): string {
  const order: ProposalKind[] = ["epic", "task", "edit", "comment", "note", "relation"];
  return order
    .map((k) => [k, kinds.filter((x) => x === k).length] as const)
    .filter(([, n]) => n > 0)
    .map(([k, n]) => `${n} ${KIND_WORD[k][n === 1 ? 0 : 1]}`)
    .join(", ");
}

/** How many filed items the folded line names before "+N more". */
const FOLDED_NAMES = 4;

/** Unfiled, and someone else's to file: not the person looking's, yet not blocked for everyone. */
function othersToFile(item: ProposalCardItem, readOnly?: boolean): boolean {
  if (item.filed) return false;
  return item.canFile ? Boolean(readOnly) : item.blockedFor === "you";
}

/**
 * Nothing on the card is left for the person looking to file: each item is
 * filed, or someone else's to file. An item nobody can file keeps it open.
 */
export function nothingLeftToFile(items: ReadonlyArray<ProposalCardItem>, readOnly?: boolean): boolean {
  return items.length > 0 && items.every((item) => item.filed || othersToFile(item, readOnly));
}

/**
 * The folded card's words after "Proposed work · 2 epics, 3 tasks": all
 * filed, what they became (an epic, which has no key, by its title) and who
 * filed them; otherwise how many are filed and how many wait for others.
 */
export function foldedWords(items: ReadonlyArray<ProposalCardItem>, readOnly?: boolean): string[] {
  const filed = items.flatMap((item) => (item.filed ? [{ item, filed: item.filed }] : []));
  if (filed.length < items.length) {
    const others = `${items.filter((item) => othersToFile(item, readOnly)).length} for others to file`;
    return filed.length > 0 ? [`${filed.length} filed`, others] : [others];
  }
  const names = filed.map(({ item, filed: f }) =>
    (item.kind === "epic" || !f.key) && typeof item.title === "string" ? item.title : f.key || "an item");
  const shown = names.length > FOLDED_NAMES ? [...names.slice(0, FOLDED_NAMES), `+${names.length - FOLDED_NAMES} more`] : names;
  const by = [...new Set(filed.map(({ filed: f }) => firstName(f.by)))];
  const who = by.length > 1 ? `${by.slice(0, -1).join(", ")} and ${by.at(-1)}` : by[0];
  return [`all filed: ${shown.join(", ")}`, ...(who ? [`by ${who}`] : [])];
}

/**
 * What the session's agent proposes, for a member to file with a click.
 * Each item says where it goes; an edit shows its text before and after.
 * The person looking ticks what they keep and presses File: it files as
 * them, so an item only someone else may file stays, saying who. Filed
 * items say who filed them as what. Nothing here mentions the session: the
 * work filed is theirs, as if they had typed it.
 *
 * With nothing left for the person looking to file, the card folds to one
 * line in its place in the Chat, in the margin-note grammar of a
 * `ChatEvent`; the line opens the whole card, read only. The fold follows
 * from the items alone: a card filed here folds as soon as it says so. An
 * item blocked for everyone keeps the card open, its `why` showing.
 */
export function ProposalCard(props: ProposalCardProps) {
  const { items, selected, onToggle, onFile, filingAs, busy, readOnly, defaultOpen, className, ...rest } = props;
  const [open, setOpen] = useState(Boolean(defaultOpen));
  if (!nothingLeftToFile(items, readOnly)) return <OpenCard {...props} />;
  const words = [cardSummary(items), ...foldedWords(items, readOnly)].join(" · ");
  return (
    <section className={cx(styles["folded"], open && styles["foldedOpen"], className)} aria-label="Proposed work"
      data-testid="proposal-card" data-folded="true" {...rest}>
      <button type="button" className={styles["foldLine"]} aria-expanded={open} onClick={() => setOpen((o) => !o)}
        data-testid="proposal-fold" title={`Proposed work · ${words}`}>
        <span className={styles["foldGlyph"]} aria-hidden><Icon name="list-check" size={12} /></span>
        <span className={styles["foldWords"]}><b>Proposed work</b> · {words}</span>
        <span className={styles["foldChevron"]} aria-hidden><Icon name="chevron-right" size={14} className={styles["foldChevronIcon"]} /></span>
      </button>
      {open ? <OpenCard items={items} selected={selected} onToggle={onToggle} onFile={onFile} filingAs={filingAs}
        readOnly={readOnly} folded /> : null}
    </section>
  );
}

/** "3 tasks, 4 relations": the kinds, then the relations the items propose (a relation item is one). */
function cardSummary(items: ReadonlyArray<ProposalCardItem>): string {
  const kinds = proposalSummary(items.map((i) => i.kind));
  const n = relationCount(items);
  return n > 0 && !items.some((i) => i.kind === "relation") ? `${kinds}, ${n} ${n === 1 ? "relation" : "relations"}` : kinds;
}

/** The edges the items propose, each once: an in-card edge named from both ends counts once. */
function relationCount(items: ReadonlyArray<ProposalCardItem>): number {
  const seen = new Set<string>();
  items.forEach((item, i) => {
    const me = `#${item.n ?? i + 1}`;
    for (const r of item.relations ?? []) {
      const other = r.item !== undefined ? `#${r.item}` : r.task?.key ?? "?";
      seen.add(r.kind === "blocks" ? `${me}>${other}` : r.kind === "blocked-by" ? `${other}>${me}` : [me, other].sort().join("~"));
    }
  });
  return seen.size;
}

const RELATION_WORD: Record<ProposalRelation["kind"], string> = { "blocked-by": "Blocked by", blocks: "Blocks", related: "Related" };
const RELATION_GLYPH: Record<ProposalRelation["kind"], "lock" | "blocks" | "link"> = { "blocked-by": "lock", blocks: "blocks", related: "link" };

/** An item's relations under its goal: in-card ones by number, existing tasks by status mark and key. */
function ItemRelations({ relations }: { readonly relations: ReadonlyArray<ProposalRelation> }) {
  return (
    <span className={styles["itemRels"]} data-testid="item-relations">
      {relations.map((r, i) => (
        <span key={i} className={styles["itemRel"]}
          aria-label={`${RELATION_WORD[r.kind]} ${r.item !== undefined ? `item ${r.item}` : r.task?.key ?? ""}${r.task ? `, ${statusSpec(r.task.status).label.toLowerCase()}` : ""}`}>
          <Icon name={RELATION_GLYPH[r.kind]} size={12} />
          <span>{RELATION_WORD[r.kind]}</span>
          {r.item !== undefined ? <b>item {r.item}</b> : r.task ? <><RefLead type="task" taskKey={r.task.key} status={r.task.status} /><StatusMark status={r.task.status} size="sm" /></> : null}
        </span>
      ))}
    </span>
  );
}

/** "Ticked with 2 and 3: they wait on it. Untick them to leave it out." */
function tickedWords(ns: ReadonlyArray<number>): string {
  const list = ns.length > 1 ? `${ns.slice(0, -1).join(", ")} and ${ns.at(-1)}` : String(ns[0]);
  return `Ticked with ${list}: ${ns.length === 1 ? "it waits" : "they wait"} on it. Untick ${ns.length === 1 ? "it" : "them"} to leave it out.`;
}

function OpenCard({ items, selected, onToggle, onFile, filingAs, busy, readOnly, folded, order, defaultOpen: _, className, ...rest }: ProposalCardProps & { readonly folded?: boolean }) {
  const footId = useId();
  // What is ticked, with what the ticked items wait on (filing files it with them).
  const withBlockers = new Set<number>();
  items.forEach((item, i) => {
    if (!item.tickedWith?.length || item.filed || !item.canFile) return;
    if (item.tickedWith.some((n) => selected.has(items.findIndex((x, j) => (x.n ?? j + 1) === n)))) withBlockers.add(i);
  });
  const isTicked = (i: number) => (selected.has(i) || withBlockers.has(i)) && items[i]!.canFile && !items[i]!.filed;
  const ticked = items.flatMap((item, i) => (isTicked(i) ? [item] : []));
  const stays = items.filter((item) => !item.filed && !item.canFile && item.why);
  const numbered = items.some((i) => i.n !== undefined);
  let foot: string | null;
  if (readOnly) {
    foot = "You can read this session: filing is for its owner and members who can chat.";
  } else if (folded) {
    // Opened from its line, nothing on it to file: the line says what became of it.
    foot = null;
  } else if (!items.some((item) => item.canFile && !item.filed)) {
    foot = "Nothing here is yours to file now: each item says why.";
  } else if (ticked.length === 0) {
    foot = "Tick what to file.";
  } else {
    const staying = stays.length > 0 ? ` · ${stays.length} stay${stays.length === 1 ? "s" : ""} for whoever can` : "";
    const first = withBlockers.size > 0 ? `, ${[...withBlockers].map((i) => items[i]!.n ?? i + 1).join(" and ")} first` : "";
    const rels = relationCount(ticked);
    const withRels = rels > 0 ? `, with their ${rels} ${rels === 1 ? "relation" : "relations"}` : "";
    foot = `Filing as ${filingAs}: ${proposalSummary(ticked.map((t) => t.kind))}${first}${withRels}${staying}. Nothing starts.`;
  }
  return (
    <section className={cx(styles["card"], folded && styles["unfolded"], className)} aria-label="Proposed work"
      data-testid={folded ? "proposal-unfolded" : "proposal-card"} {...rest}>
      {folded ? null : (
        <header className={styles["cardHead"]}>
          <Icon name="list-check" size={14} />
          <b>Proposed work</b>
          <span className={styles["muted"]}>· {cardSummary(items)}</span>
          {order && order.length > 1 ? (
            <span className={styles["order"]} data-testid="proposal-order"
              aria-label={`Order: ${order.map((s) => (s.length > 1 ? `items ${s.join(" and ")}` : `item ${s[0]}`)).join(", then ")}`}>
              Order {order.map((step, i) => (
                <span key={i} className={styles["orderStep"]}>
                  {i > 0 ? <Icon name="arrow-right" size={12} /> : null}
                  {step.map((n) => <span key={n} className={styles["orderNo"]}>{n}</span>)}
                </span>
              ))}
            </span>
          ) : null}
        </header>
      )}
      <ul className={cx(styles["items"], numbered && styles["numbered"])}>
        {items.map((item, i) => {
          const blocked = !item.canFile && !item.filed;
          const tickedWith = withBlockers.has(i) ? item.tickedWith ?? [] : [];
          return (
            <li key={i} className={cx(styles["item"], item.child && styles["child"], blocked && styles["blocked"])}
              data-kind={item.kind} data-filed={item.filed ? "true" : undefined} data-item={item.n}>
              <span className={styles["tick"]}>
                {item.filed ? (
                  <Icon name="check" size={14} aria-label="filed" />
                ) : (
                  <Checkbox aria-label={`File ${numbered ? `item ${item.n ?? i + 1}: ` : ""}${typeof item.title === "string" ? item.title : "this item"}`}
                    checked={isTicked(i)} disabled={readOnly || !item.canFile || busy}
                    onCheckedChange={() => onToggle(i)} />
                )}
              </span>
              {numbered ? <span className={styles["itemNo"]} aria-hidden>{item.n ?? i + 1}</span> : null}
              <div className={styles["itemBody"]}>
                <div className={styles["itemTitle"]}>
                  {item.kind === "epic" ? <span className={styles["kind"]}>Epic</span> : null}
                  {item.kind === "note" ? <span className={styles["kind"]}><Icon name="pin" size={12} /> Note</span> : null}
                  {item.title}
                  {item.tag ? <span className={styles["tag"]}>{item.tag}</span> : null}
                </div>
                {item.detail ? <div className={styles["itemDetail"]}>{item.detail}</div> : null}
                {item.notes && item.notes.length > 0 ? <ProposedNotes notes={item.notes} /> : null}
                {item.relations?.length ? <ItemRelations relations={item.relations} /> : null}
                {item.before !== undefined || item.after !== undefined ? (
                  <div className={styles["beforeAfter"]}>
                    <div><div className="ds-label">Now</div>{item.before}</div>
                    <div><div className="ds-label">After</div><ins className={styles["ins"]}>{item.after}</ins></div>
                  </div>
                ) : null}
                {item.filed ? (
                  <div className={styles["note"]}>{item.filed.by} filed {item.filed.key}</div>
                ) : blocked && item.why ? (
                  <div className={styles["note"]} data-testid="cannot-file">{item.why}</div>
                ) : tickedWith.length > 0 ? (
                  <div className={styles["note"]} data-testid="ticked-with">{tickedWords(tickedWith)}</div>
                ) : null}
              </div>
              <span className={styles["where"]}>
                {item.project ? (
                  <><ProjectAvatar project={{ name: item.project.name, id: item.project.key }} size={16} />{item.project.name}</>
                ) : item.taskKey ? (
                  <code>{item.taskKey}</code>
                ) : null}
              </span>
            </li>
          );
        })}
      </ul>
      {foot === null ? null : (
        <footer className={styles["cardFoot"]}>
          <span className={styles["muted"]} id={footId}>{foot}</span>
          {/* Disabled, never gone, while nothing is ticked: the footer beside it says why. */}
          {readOnly ? null : (
            <Button variant="primary" size="sm" onClick={onFile} disabled={busy || ticked.length === 0} data-testid="file-proposal"
              aria-describedby={ticked.length === 0 ? footId : undefined}>
              {ticked.length === 0 ? "File" : `File ${ticked.length}`}
            </Button>
          )}
        </footer>
      )}
    </section>
  );
}

// ---------------------------------------------------------------------------
// A session in a list, and the shared marker
// ---------------------------------------------------------------------------

/** A proposed epic's notes, under its description: what every agent on it is told once it is filed, and only then. */
function ProposedNotes({ notes }: { readonly notes: ReadonlyArray<string> }) {
  return (
    <div className={styles["notes"]} data-testid="proposed-notes">
      <div className={styles["notesHead"]}><Icon name="pin" size={12} />
        {notes.length === 1 ? "1 epic note" : `${notes.length} epic notes`}, pinned when it is filed</div>
      <ol className={styles["notesList"]}>
        {notes.map((n, i) => <li key={i}><Markdown source={n} unmeasured /></li>)}
      </ol>
    </div>
  );
}

/**
 * A session someone else is in too: the shared glyph, and the owner's face
 * when it is not yours. Read without colour: the glyph and the face.
 */
export function SharedMark({ owner, label = "Shared" }: { readonly owner?: Person | undefined; readonly label?: string }) {
  return (
    <span className={styles["shared"]} title={owner ? `${label} · ${owner.name}'s` : label} aria-label={owner ? `${label}, ${owner.name}'s` : label}>
      <Icon name="shared" size={12} />
      {owner ? <PersonAvatar person={owner} size={16} /> : null}
    </span>
  );
}

export interface SessionRowProps extends Omit<HTMLAttributes<HTMLLIElement>, "title"> {
  readonly title: string;
  /** What it filed, in a line ("Filed 1 epic, 4 tasks"), or "Nothing filed yet". */
  readonly summary: ReactNode;
  readonly projects: ReadonlyArray<{ readonly key: string; readonly name: string; readonly repositories: number }>;
  /** "Talking", "Parked", "Waiting for you". */
  readonly state: ReactNode;
  readonly age?: ReactNode;
  readonly shared?: { readonly owner?: Person | undefined } | undefined;
  readonly onOpen: () => void;
}

/** A session in the list: its title, what it filed, what it reads, how it is. */
export function SessionRow({ title, summary, projects, state, age, shared, onOpen, className, ...rest }: SessionRowProps) {
  return (
    <li className={cx(styles["row"], className)} {...rest}>
      <span className={styles["rowFace"]}><Icon name="brainstorm" size={16} /></span>
      <button type="button" className={styles["rowOpen"]} onClick={onOpen}>
        <span className={styles["rowTitle"]}>
          {title}
          {shared ? <SharedMark owner={shared.owner} /> : null}
        </span>
        <span className={styles["muted"]}>{summary}</span>
      </button>
      <span className={styles["chips"]}>
        {projects.length === 0 ? <span className={styles["chip"]}>No project yet</span> : projects.map((p) => (
          <span key={p.key} className={styles["chip"]}>
            <ProjectAvatar project={{ name: p.name, id: p.key }} size={16} />
            <b>{p.name}</b> {p.repositories} {p.repositories === 1 ? "repo" : "repos"}
          </span>
        ))}
      </span>
      <span className={styles["rowState"]}>{state}{age ? <><br />{age}</> : null}</span>
    </li>
  );
}

// ---------------------------------------------------------------------------
// The rail: people, linked projects, what it can do
// ---------------------------------------------------------------------------

export type SessionRole = "owner" | "chat" | "read";

export const SESSION_ROLE_WORD: Record<SessionRole, string> = { owner: "Owner", chat: "Can chat", read: "Can read" };

export interface SessionMember {
  readonly person: Person;
  readonly role: SessionRole;
  /** Invited, not accepted yet. */
  readonly invited?: boolean | undefined;
  /** Has the session open now. */
  readonly open?: boolean | undefined;
  readonly you?: boolean | undefined;
}

/** Who is in the session, owner first; a breathing dot for those who have it open now. */
export function SessionPeople({ members }: { readonly members: ReadonlyArray<SessionMember> }) {
  return (
    <ul className={styles["people"]} data-testid="session-people">
      {members.map((m) => (
        <li key={m.person.id ?? m.person.name} className={styles["member"]} data-open={m.open ? "true" : undefined}>
          <PersonAvatar person={{ ...m.person, online: m.open ?? false }} size={24} />
          <b>{m.person.name}{m.you ? <span className={styles["muted"]}> · you</span> : null}</b>
          <span className={styles["muted"]}>
            {m.invited ? "Invited" : SESSION_ROLE_WORD[m.role]}
            {m.open ? <span className={styles["openWord"]}> · here</span> : null}
          </span>
        </li>
      ))}
    </ul>
  );
}

export interface LinkedProject {
  readonly key: string;
  readonly name: string;
  readonly repositories: ReadonlyArray<{ readonly name: string; readonly defaultBranch: string }>;
}

/** What the session reads: each linked project and its checked-out repositories. */
export function LinkedProjects({ projects }: { readonly projects: ReadonlyArray<LinkedProject> }) {
  if (projects.length === 0) return <p className={styles["muted"]}>No project linked yet: it can talk and search memory.</p>;
  return (
    <div className={styles["linked"]} data-testid="linked-projects">
      {projects.map((p) => (
        <div key={p.key} className={styles["project"]}>
          <div className={styles["projectHead"]}><ProjectAvatar project={{ name: p.name, id: p.key }} size={16} />{p.name}</div>
          {p.repositories.length === 0 ? <div className={styles["repo"]}><span className={styles["muted"]}>Tasks and epics only</span></div> : null}
          {p.repositories.map((r) => (
            <div key={r.name} className={styles["repo"]}>
              <Icon name="git-branch" size={12} /><code>{r.name}</code><span className={styles["muted"]}>{r.defaultBranch}</span>
            </div>
          ))}
        </div>
      ))}
      <p className={styles["muted"]}>Each repository is checked out at <code>repos/&lt;key&gt;/&lt;name&gt;</code>, read only.</p>
    </div>
  );
}

/** What a session's agent can and cannot do, each line marked by glyph as well as word. */
export function Capabilities({ can, cannot }: { readonly can: ReadonlyArray<string>; readonly cannot: ReadonlyArray<string> }) {
  return (
    <ul className={styles["can"]}>
      {can.map((c) => <li key={c} data-can="yes"><Icon name="check" size={12} />{c}</li>)}
      {cannot.map((c) => <li key={c} data-can="no"><Icon name="cross" size={12} />{c}</li>)}
    </ul>
  );
}
