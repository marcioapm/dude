import { useId, useState, type ReactNode } from "react";
import type { AgentRole } from "@dude/domain";
import { AUTO_START_HOW_LABEL, keyList } from "@dude/domain";
import { Icon } from "../icons/index.tsx";
import { Button } from "../primitives/Button.tsx";
import { ChoiceList } from "../primitives/ChoiceList.tsx";
import { Dialog } from "../primitives/Dialog.tsx";
import { Select } from "../primitives/Select.tsx";
import { Textarea } from "../primitives/Textarea.tsx";
import { PersonAvatar, type Person } from "./PersonAvatar.tsx";
import { RefLead } from "./RefLead.tsx";
import { SearchPicker } from "./SearchPicker.tsx";
import { StatusMark } from "./StatusMark.tsx";
import { stateWord, type RelatedTask } from "./Relations.tsx";
import styles from "./Relations.module.css";

/*
 * The dialogs a blocked task's start opens — Start anyway, and Start when
 * unblocked — and the picker that adds a relation.
 */

type How = "deliver" | "talk";

/** A blocker as the dialogs list it: its owner's face, with the agent at work on it. */
export interface DialogBlocker extends RelatedTask {
  readonly owner?: Person | null | undefined;
  readonly agent?: AgentRole | undefined;
}

function BlockerList({ blockers }: { readonly blockers: ReadonlyArray<DialogBlocker> }) {
  return (
    <ul className={styles["blockerList"]} aria-label="Blocked by">
      {blockers.map((b) => (
        <li key={b.key}>
          <RefLead type="task" taskKey={b.key} status={b.status} />
          <span className={styles["tipTitle"]}>{b.title}</span>
          {b.project ? <span className={styles["rowNote"]}>{b.project}</span> : null}
          {b.unmerged ? <span className={styles["rowNote"]}>{stateWord(b)}</span> : <StatusMark status={b.status} size="sm" />}
          {b.owner ? <PersonAvatar person={b.owner} size={20} agent={b.agent} live={b.agent ? true : undefined} /> : null}
        </li>
      ))}
    </ul>
  );
}

const HOW_OPTIONS = [
  { value: "talk" as const, label: "Talk it through", meta: "the conductor plans with you first" },
  { value: "deliver" as const, label: "Deliver", meta: "the automatic pipeline" },
];

export interface StartAnywayDialogProps {
  readonly open: boolean;
  readonly onOpenChange: (open: boolean) => void;
  readonly taskKey: string;
  readonly blockers: ReadonlyArray<DialogBlocker>;
  /** The way preset: the one the card's menu or Start over came from. */
  readonly how?: How | undefined;
  readonly onConfirm: (how: How, note: string) => void | Promise<void>;
  /** Why the last try failed. */
  readonly problem?: string | null | undefined;
  readonly busy?: boolean | undefined;
}

/**
 * Start anyway: names each blocker, says what starting now means, asks
 * which way. Keep waiting has the focus; the confirm is primary, not
 * danger — overriding is a person's legitimate call.
 */
export function StartAnywayDialog({ open, onOpenChange, taskKey, blockers, how: preset, onConfirm, problem, busy }: StartAnywayDialogProps) {
  const [how, setHow] = useState<How>(preset ?? "talk");
  const [note, setNote] = useState("");
  const one = blockers.length === 1;
  return (
    <Dialog open={open} onOpenChange={onOpenChange} size="md" tone="attention" title={`Start ${taskKey} anyway?`}
      footerStart={<span>Recorded in {taskKey}'s history, with your note.</span>} footerProblem={problem}
      footer={
        <>
          <Button variant="quiet" onClick={() => onOpenChange(false)} autoFocus data-testid="keep-waiting">Keep waiting</Button>
          <Button variant="primary" leadingIcon={how === "deliver" ? "zap" : "message"} disabled={busy}
            onClick={() => void onConfirm(how, note.trim())} data-testid="confirm-start-anyway">
            {how === "deliver" ? "Deliver anyway" : "Talk it through anyway"}
          </Button>
        </>
      }>
      <div className={styles["dialogBody"]}>
        <p>
          It waits on {one ? "this, which isn't" : `these ${blockers.length}, which aren't`} merged. Its agents start
          from <span className={styles["mono"]}>main</span> as it is now: what {one ? "it changes" : "they change"} won't be there.
        </p>
        <BlockerList blockers={blockers} />
        <Select label="Start with" value={how} onValueChange={(v) => setHow(v)} options={HOW_OPTIONS} />
        <Textarea label="Why (optional)" rows={2} value={note} onChange={(e) => setNote(e.target.value)}
          hint="The relations stay: it still shows as after them on the board." />
      </div>
    </Dialog>
  );
}

export interface AutoStartDialogProps {
  readonly open: boolean;
  readonly onOpenChange: (open: boolean) => void;
  readonly taskKey: string;
  readonly blockers: ReadonlyArray<DialogBlocker>;
  readonly onConfirm: (how: How) => void | Promise<void>;
  /** A way chosen already, for a static picture; otherwise nothing is chosen until the person picks. */
  readonly chosen?: How | undefined;
  readonly problem?: string | null | undefined;
  readonly busy?: boolean | undefined;
}

/**
 * Start when unblocked: the owner chooses now how it will start, as if they
 * pressed it the moment the last blocker merges. Nothing is preselected
 * (starting is a choice), so the confirm stays disabled, saying why, until
 * a way is picked.
 */
export function AutoStartDialog({ open, onOpenChange, taskKey, blockers, onConfirm, chosen, problem, busy }: AutoStartDialogProps) {
  const [how, setHow] = useState<How | "">(chosen ?? "");
  const footId = useId();
  const keys = keyList(blockers.map((b) => b.key));
  const one = blockers.length === 1;
  return (
    <Dialog open={open} onOpenChange={onOpenChange} size="md" title={`Start ${taskKey} when it's unblocked?`} footerProblem={problem}
      footerStart={<span id={footId}>{how ? `Recorded in ${taskKey}'s history. You can cancel it until it starts.` : "Pick how it starts."}</span>}
      footer={
        <>
          <Button variant="quiet" onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button variant="primary" leadingIcon="play" disabled={!how || busy} aria-describedby={how ? undefined : footId}
            onClick={() => how && void onConfirm(how)} data-testid="confirm-auto-start">
            {how ? `${AUTO_START_HOW_LABEL[how]} when ${one ? "it merges" : "they merge"}` : "Set"}
          </Button>
        </>
      }>
      <div className={styles["dialogBody"]}>
        <p>
          When {keys} {one ? "merges" : "have all merged"}, {taskKey} starts the way you pick here, as if you had pressed it then.
          Every blocker is checked again at that moment: if one is open, nothing starts. Its agents start
          from <span className={styles["mono"]}>main</span> with {one ? "its" : "their"} changes in it.
        </p>
        <BlockerList blockers={blockers} />
        <ChoiceList<How | ""> label="How it starts" value={how} onChange={setHow} data-testid="auto-how"
          options={[
            { value: "talk", icon: "message", label: "Talk it through", description: "Its conductor starts, reads the task and the merged code, and waits for you in Chat. Nothing is built until you agree." },
            { value: "deliver", icon: "zap", label: "Deliver", description: "The automatic pipeline: an implementer, reviewers, a fixer, then a pull request." },
          ]} />
        <p className={styles["muted"]}>{taskKey}'s Chat and Activity say when it starts, and that you asked for it.</p>
      </div>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// The picker
// ---------------------------------------------------------------------------

export type RelationSide = "blocked_by" | "blocks" | "related";

/** A task the picker offers. */
export interface PickableTask extends RelatedTask {
  /** Where it sits: "This epic", its project's name, "Other projects". */
  readonly group?: string | undefined;
}

export interface RelationPickerProps {
  /** The task relations are added to. */
  readonly here: { readonly key: string };
  readonly find: (query: string) => Promise<ReadonlyArray<PickableTask>>;
  /** Keys already related: shown, disabled "Already related". */
  readonly related: ReadonlySet<string>;
  /** Adds the relation; a `{loop}` answer is a refused cycle, named under the field. */
  readonly onAdd: (side: RelationSide, key: string) => Promise<void | { readonly loop: ReadonlyArray<string> } | { readonly error: string }>;
  readonly onClose: () => void;
  /** The kind chosen at first (the gallery's static pictures). */
  readonly initialSide?: RelationSide | undefined;
  /** A loop shown from the start (the gallery's static picture). */
  readonly initialLoop?: ReadonlyArray<string> | undefined;
  /** Focus the search on mount (default). Off in the gallery: a static picture must not take focus or scroll the page. */
  readonly autoFocus?: boolean | undefined;
}

const SIDE_OPTIONS = [
  { value: "blocked_by" as const, label: "Blocked by", meta: "This can't start until it is merged" },
  { value: "blocks" as const, label: "Blocks", meta: "That can't start until this is merged" },
  { value: "related" as const, label: "Related to", meta: "Gates nothing" },
];

/**
 * Picks what a task relates to — never a field of keys: a kind, then a
 * search over the organisation's tasks, grouped by this epic, this project
 * and the others, with this task and those already related shown but not
 * pickable. Enter adds and the picker stays open for the next; Escape
 * closes the list, then the picker. A loop is refused in words that name
 * it, with the one fix offered: make it Related instead.
 */
export function RelationPicker({ here, find, related, onAdd, onClose, initialSide, initialLoop, autoFocus = true }: RelationPickerProps) {
  const [side, setSide] = useState<RelationSide>(initialSide ?? "blocked_by");
  const [loop, setLoop] = useState<{ keys: ReadonlyArray<string>; key: string } | null>(initialLoop ? { keys: initialLoop, key: initialLoop[1] ?? "" } : null);
  const [error, setError] = useState<string | null>(null);
  const add = async (s: RelationSide, key: string) => {
    setError(null);
    setLoop(null);
    const out = await onAdd(s, key);
    if (out && "loop" in out) setLoop({ keys: out.loop, key });
    else if (out && "error" in out) setError(out.error);
  };
  let named: ReactNode = null;
  if (loop) {
    const label = `${loop.keys[0]} blocks ${loop.keys.slice(1).join(", which blocks ")}`;
    named = (
      <div className={styles["loop"]} role="alert" data-testid="relation-loop">
        <span>That would make a loop:</span>
        <span className={styles["loopKeys"]} aria-label={label}>
          {loop.keys.map((k, i) => (
            <span key={`${k}-${i}`} className={styles["loopKeys"]}>{i > 0 ? <Icon name="blocks" size={12} className={styles["glyph"]} /> : null}{k}</span>
          ))}
        </span>
        <span>A task can't wait on itself.</span>
        <Button variant="quiet" size="sm" onClick={() => void add("related", loop.key)} data-testid="make-related">Make it Related instead</Button>
      </div>
    );
  }
  return (
    <div className={styles["picker"]} data-testid="relation-picker">
      <div className={styles["pickerRow"]}>
        <div className={styles["pickerKind"]}>
          <Select label={`${here.key} is`} value={side} onValueChange={(v) => setSide(v)} options={SIDE_OPTIONS} size="sm" />
        </div>
        <div className={styles["pickerFind"]}>
          <SearchPicker<PickableTask> label="Task" size="sm" autoFocus={autoFocus} findOnEmpty clearOnPick placeholder="Search by key or title"
            find={find} optionKey={(t) => t.key} group={(t) => t.group ?? ""}
            optionDisabled={(t) => (t.key === here.key ? "This task" : related.has(t.key) ? "Already related" : null)}
            renderOption={(t) => (
              <span className={styles["option"]}>
                <RefLead type="task" taskKey={t.key} status={t.status} />
                <span className={styles["tipTitle"]}>{t.title}</span>
                <StatusMark status={t.status} size="sm" />
              </span>
            )}
            onPick={(t) => void add(side, t.key)} onCancel={onClose}
            empty={(q) => `No task matches “${q}”.`} />
        </div>
        <Button variant="quiet" size="sm" onClick={onClose}>Done</Button>
      </div>
      {named}
      {error ? <div className={styles["loop"]} role="alert">{error}</div> : null}
    </div>
  );
}
