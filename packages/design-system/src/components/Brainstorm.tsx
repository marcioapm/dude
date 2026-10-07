import type { HTMLAttributes, ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Checkbox } from "../primitives/Checkbox.tsx";
import { Button } from "../primitives/Button.tsx";
import { Icon } from "../icons/index.tsx";
import { PersonAvatar, type Person } from "./PersonAvatar.tsx";
import { ProjectAvatar } from "./ProjectAvatar.tsx";
import styles from "./Brainstorm.module.css";

/*
 * A brainstorm session's parts: the proposal card its agent fills and a
 * member files from, a session's row in the list, its people and what it
 * reads in the rail, and the marker for a session someone else is in too.
 */

// ---------------------------------------------------------------------------
// The proposal card
// ---------------------------------------------------------------------------

export type ProposalKind = "epic" | "task" | "edit" | "comment";

export interface ProposalCardItem {
  readonly kind: ProposalKind;
  /** The title: a new epic's or task's, or what an edit or comment is on ("Edit BL-58 · Daily rollup"). */
  readonly title: ReactNode;
  /** One line under it: a task's goal, an epic's description, the comment's words. */
  readonly detail?: ReactNode;
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
  /** Why not, said on the item ("Only Márcio can file this: it's his task"). */
  readonly why?: string | undefined;
  /** Filed already: by whom, as what ("Ana filed BL-61"). */
  readonly filed?: { readonly by: string; readonly key: string } | undefined;
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
}

const KIND_WORD: Record<ProposalKind, [string, string]> = {
  epic: ["epic", "epics"], task: ["task", "tasks"], edit: ["edit", "edits"], comment: ["comment", "comments"],
};

/** "2 tasks, 1 edit": what a set of items is, by kind, in the card's order. */
export function proposalSummary(kinds: ReadonlyArray<ProposalKind>): string {
  const order: ProposalKind[] = ["epic", "task", "edit", "comment"];
  return order
    .map((k) => [k, kinds.filter((x) => x === k).length] as const)
    .filter(([, n]) => n > 0)
    .map(([k, n]) => `${n} ${KIND_WORD[k][n === 1 ? 0 : 1]}`)
    .join(", ");
}

/**
 * What the session's agent proposes, for a member to file with a click.
 * Each item says where it goes; an edit shows its text before and after.
 * The person looking ticks what they keep and presses File: it files as
 * them, so an item only someone else may file stays, saying who. Filed
 * items say who filed them as what. Nothing here mentions the session: the
 * work filed is theirs, as if they had typed it.
 */
export function ProposalCard({ items, selected, onToggle, onFile, filingAs, busy, readOnly, className, ...rest }: ProposalCardProps) {
  const ticked = items.flatMap((item, i) => (selected.has(i) && item.canFile && !item.filed ? [item] : []));
  const left = items.filter((item, i) => !item.filed && !(selected.has(i) && item.canFile));
  const stays = left.filter((item) => !item.canFile && item.why);
  const foot = readOnly
    ? "You can read this session: filing is for its owner and members who can chat."
    : ticked.length === 0
      ? "Tick what to file."
      : `Filing as ${filingAs}: ${proposalSummary(ticked.map((t) => t.kind))}${stays.length > 0 ? ` · ${stays.length} stay${stays.length === 1 ? "s" : ""} for whoever can` : ""}. Nothing starts.`;
  return (
    <section className={cx(styles["card"], className)} aria-label="Proposed work" data-testid="proposal-card" {...rest}>
      <header className={styles["cardHead"]}>
        <Icon name="list-check" size={14} />
        <b>Proposed work</b>
        <span className={styles["muted"]}>· {proposalSummary(items.map((i) => i.kind))}</span>
      </header>
      <ul className={styles["items"]}>
        {items.map((item, i) => {
          const blocked = !item.canFile && !item.filed;
          return (
            <li key={i} className={cx(styles["item"], item.child && styles["child"], blocked && styles["blocked"])}
              data-kind={item.kind} data-filed={item.filed ? "true" : undefined}>
              <span className={styles["tick"]}>
                {item.filed ? (
                  <Icon name="check" size={14} aria-label="filed" />
                ) : (
                  <Checkbox aria-label={`File ${typeof item.title === "string" ? item.title : "this item"}`}
                    checked={selected.has(i) && item.canFile} disabled={readOnly || !item.canFile || busy}
                    onCheckedChange={() => onToggle(i)} />
                )}
              </span>
              <div className={styles["itemBody"]}>
                <div className={styles["itemTitle"]}>
                  {item.kind === "epic" ? <span className={styles["kind"]}>Epic</span> : null}
                  {item.title}
                  {item.tag ? <span className={styles["tag"]}>{item.tag}</span> : null}
                </div>
                {item.detail ? <div className={styles["itemDetail"]}>{item.detail}</div> : null}
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
      <footer className={styles["cardFoot"]}>
        <span className={styles["muted"]}>{foot}</span>
        {readOnly ? null : (
          <Button variant="primary" size="sm" onClick={onFile} disabled={busy || ticked.length === 0} data-testid="file-proposal">
            {ticked.length === 0 ? "Nothing to file" : `File ${ticked.length}`}
          </Button>
        )}
      </footer>
    </section>
  );
}

// ---------------------------------------------------------------------------
// A session in a list, and the shared marker
// ---------------------------------------------------------------------------

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
