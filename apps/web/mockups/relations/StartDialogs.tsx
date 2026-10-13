/*
 * The two dialogs a blocked task's start opens, from its page or from its
 * card's start menu: Start anyway (and its "Later: with agent comms"
 * variant), and Start when unblocked.
 */

import { useState } from "react";
import { PersonAvatar, RefLead, StatusMark } from "@dude/design-system/components";
import { Badge, Button, ChoiceList, Dialog, Select, Textarea } from "@dude/design-system/primitives";
import type { Task } from "./data.ts";
import { keyList, stateWord } from "./Relations.tsx";
import styles from "./pages.module.css";

type How = "deliver" | "talk";

function BlockerList({ blockers }: { readonly blockers: ReadonlyArray<Task & { readonly unmerged?: boolean }> }) {
  return (
    <ul className={styles["blockerList"]} aria-label="Blocked by">
      {blockers.map((b) => (
        <li key={b.key}>
          <RefLead type="task" taskKey={b.key} status={b.status} />
          <span className={styles["blockerTitle"]}>{b.title}</span>
          {b.unmerged ? <span className={styles["unmergedWord"]}>{stateWord(b)}</span> : <StatusMark status={b.status} size="sm" />}
          <PersonAvatar person={b.owner} size={20} agent={b.agent?.role} live={b.agent ? true : undefined} />
        </li>
      ))}
    </ul>
  );
}

const HOW_OPTIONS = [
  { value: "talk" as const, label: "Talk it through", meta: "the conductor plans with you first" },
  { value: "deliver" as const, label: "Deliver", meta: "the automatic pipeline" },
];

export interface OverrideDialogProps {
  readonly open: boolean;
  readonly onOpenChange: (o: boolean) => void;
  readonly here: Task;
  readonly blockers: ReadonlyArray<Task & { readonly unmerged?: boolean }>;
  /** "Later: with agent comms": a parallel start whose agents talk to the blocker's. */
  readonly comms?: boolean | undefined;
}

/** Start anyway: names each blocker, says what starting now means, asks how. Keep waiting has the focus. */
export function OverrideDialog({ open, onOpenChange, here, blockers, comms }: OverrideDialogProps) {
  const [how, setHow] = useState<How>("talk");
  const [mode, setMode] = useState<"main" | "parallel">(comms ? "parallel" : "main");
  const [base, setBase] = useState<"main" | "stacked">("stacked");
  const one = blockers.length === 1 ? blockers[0]! : null;
  const keys = keyList(blockers.map((b) => b.key));
  const parallel = comms && mode === "parallel";
  return (
    <Dialog open={open} onOpenChange={onOpenChange} size="md" tone="attention"
      title={comms ? <span className={styles["laterTitle"]}>Start {here.key} anyway? <Badge size="sm" data-testid="later-badge">Later: with agent comms</Badge></span> : `Start ${here.key} anyway?`}
      footerStart={<span>Recorded in {here.key}'s history, with your note.</span>}
      footer={
        <>
          <Button variant="quiet" onClick={() => onOpenChange(false)} autoFocus>Keep waiting</Button>
          <Button variant="primary" leadingIcon={how === "deliver" ? "zap" : "message"} data-testid="confirm-start-anyway">
            {parallel ? (how === "deliver" ? "Deliver in parallel" : "Talk it through in parallel") : how === "deliver" ? "Deliver anyway" : "Talk it through anyway"}
          </Button>
        </>
      }>
      <div className={styles["dialogBody"]}>
        <p>
          It waits on {one ? "this, which isn't" : `these ${blockers.length}, which aren't`} merged.
          {comms ? null : <> Its agents start from <span className={styles["keyMono"]}>main</span> as it is now: what {one ? "it changes" : "they change"} won't be there.</>}
        </p>
        <BlockerList blockers={blockers} />
        {comms ? (
          <>
            <ChoiceList<"main" | "parallel"> label="How it starts" value={mode} onChange={setMode} data-testid="override-mode"
              options={[
                { value: "parallel", icon: "message", label: `Start in parallel — ${here.key}'s agents talk to ${keys}'s`,
                  description: `They tell each other what they are changing while both run. ${keys} still ${one ? "has" : "have"} to merge before ${here.key}'s pull request can.` },
                { value: "main", icon: "arrow-right", label: "Start on its own",
                  description: `Its agents start from main as it is now and don't hear from ${keys}'s.` },
              ]} />
            {parallel && one ? (
              <ChoiceList<"main" | "stacked"> label="Start from" value={base} onChange={setBase} data-testid="override-base"
                options={[
                  { value: "stacked", icon: "git-branch", label: <>{one.key}'s branch <span className={styles["stackedWord"]}>stacked</span></>,
                    description: `What ${one.key} has pushed is there from the start. When ${one.key} merges, ${here.key}'s branch is rebased onto main.` },
                  { value: "main", icon: "git-branch", label: <span className={styles["keyMono"]}>main</span>,
                    description: `${one.key}'s changes arrive only when it merges; until then its agents learn of them by asking.` },
                ]} />
            ) : null}
          </>
        ) : null}
        <Select label="Start with" value={how} onValueChange={(v) => setHow(v)} options={HOW_OPTIONS} />
        <Textarea label="Why (optional)" rows={2}
          placeholder={here.key === "CP-51" ? "We settled on per-unit prices in the spike's write-up" : here.key === "CP-42" ? "The meter's table shape is settled; the line items can be built against it" : "CP-43's sync is enough for the page's first version"}
          hint="The relations stay: it still shows as after them on the board." />
      </div>
    </Dialog>
  );
}

export interface AutoStartDialogProps {
  readonly open: boolean;
  readonly onOpenChange: (o: boolean) => void;
  readonly here: Task;
  readonly blockers: ReadonlyArray<Task>;
  /** The way chosen, for a static picture; otherwise nothing is chosen until the person picks. */
  readonly chosen?: How | undefined;
}

/**
 * Start when unblocked: the owner chooses now how it will start, as if
 * they had pressed it the moment the last blocker merges. Nothing is
 * preselected (starting is a choice), so Set stays disabled, saying why,
 * until one is picked.
 */
export function AutoStartDialog({ open, onOpenChange, here, blockers, chosen }: AutoStartDialogProps) {
  const [how, setHow] = useState<How | "">(chosen ?? "");
  const keys = keyList(blockers.map((b) => b.key));
  const one = blockers.length === 1;
  return (
    <Dialog open={open} onOpenChange={onOpenChange} size="md"
      title={`Start ${here.key} when it's unblocked?`}
      footerStart={<span id="auto-foot">{how ? `Recorded in ${here.key}'s history. You can cancel it until it starts.` : "Pick how it starts."}</span>}
      footer={
        <>
          <Button variant="quiet" onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button variant="primary" leadingIcon="play" disabled={!how} aria-describedby={how ? undefined : "auto-foot"} data-testid="confirm-auto-start">
            {how ? `${how === "deliver" ? "Deliver" : "Talk it through"} when ${one ? "it merges" : "they merge"}` : "Set"}
          </Button>
        </>
      }>
      <div className={styles["dialogBody"]}>
        <p>
          When {keys} {one ? "merges" : "have both merged"}, {here.key} starts the way you pick here, as if you had pressed it then.
          Every blocker is checked again at that moment: if one is open, nothing starts. Its agents start from <span className={styles["keyMono"]}>main</span> with {one ? "its" : "their"} changes in it.
        </p>
        <BlockerList blockers={blockers} />
        <ChoiceList<How | ""> label="How it starts" value={how} onChange={setHow} data-testid="auto-how"
          options={[
            { value: "talk", icon: "message", label: "Talk it through", description: "Its conductor starts, reads the task and the merged code, and waits for you in Chat. Nothing is built until you agree." },
            { value: "deliver", icon: "zap", label: "Deliver", description: "The automatic pipeline: an implementer, reviewers, a fixer, then a pull request." },
          ]} />
        <p className={styles["dialogNote"]}>{here.key}'s Chat and Activity say when it starts, and that you asked for it.</p>
      </div>
    </Dialog>
  );
}
