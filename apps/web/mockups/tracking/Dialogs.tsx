/*
 * The lifecycle dialogs (close-dialog, archive-open-task, archive-task,
 * archive-epic-refused) and the task dialog's kind, priority and bug fields
 * (task-dialog-bug).
 * Every one is the design system's `Dialog` with its own fields:
 * ChoiceList for a decision whose options need a sentence, SearchPicker
 * for the duplicate, Textarea for a note, MarkdownEditor for the goal and
 * the steps.
 */

import { useCallback, useState } from "react";
import { Breadcrumb, RefLead, SearchPicker, StatusMark } from "@dude/design-system/components";
import { Button, Callout, Checkbox, ChoiceList, Dialog, Fieldset, FormStack, Input, KeyHint, MarkdownEditor, Select, Textarea, type CheckedState } from "@dude/design-system/primitives";
import { EPICS, FINISHED, KIND, PRIORITY, RESOLUTION, ROLE, TASKS, firstName, task, type Kind, type Priority, type Resolution, type Task } from "./data.ts";
import { KindMark } from "./Marks.tsx";
import { DisabledWithHint } from "./TaskPage.tsx";
import styles from "./pages.module.css";

const context = (t: Task) => (
  <Breadcrumb size="sm" current={false} items={[{ id: "p", label: "control-plane" }, ...(t.epic ? [{ id: "e", label: EPICS[t.epic].title, icon: "layers" as const }] : []), { id: "k", label: t.key, mono: true }]} />
);

// ---------------------------------------------------------------------------
// close-dialog
// ---------------------------------------------------------------------------

/**
 * Close, and Close and archive: Archive… on a task that hasn't ended opens
 * this same dialog with `andArchive`, no resolution chosen, and one primary
 * that does both in one transaction.
 */
export function CloseDialog({ open, onOpenChange, t, initial = "duplicate", andArchive = false, refused }: { readonly open: boolean; readonly onOpenChange: (o: boolean) => void; readonly t: Task; readonly initial?: Resolution | null | undefined; readonly andArchive?: boolean; readonly refused?: string | undefined }) {
  const [why, setWhy] = useState<Resolution | null>(andArchive ? null : initial);
  const [dup, setDup] = useState<Task | null>(why === "duplicate" ? task("CP-62") : null);
  const [unrelate, setUnrelate] = useState<CheckedState>(false);
  const find = useCallback(async (q: string) => {
    const w = q.trim().toLowerCase();
    return TASKS.filter((x) => !x.archived && (!w || x.key.toLowerCase().includes(w) || x.title.toLowerCase().includes(w))).slice(0, 8);
  }, []);
  // What waits on it: CP-47 waited on CP-63 until it was closed.
  const waiting = t.key === "CP-63" ? [task("CP-47")] : TASKS.filter((x) => (x.blockedBy ?? []).includes(t.key));
  const isBug = t.kind === "bug";
  const waits = waiting.map((w) => w.key).join(", ");
  const one = waiting.length === 1;
  const label = andArchive ? "Close and archive"
    : why === "duplicate" && dup ? `Close as duplicate of ${dup.key}`
    : why ? `Close as ${RESOLUTION[why].label}` : "Close";
  return (
    <Dialog open={open} onOpenChange={onOpenChange} size="md" context={context(t)} title={andArchive ? `Close and archive ${t.key}` : `Close ${t.key}`}
      description={andArchive
        ? `${t.key} hasn't ended, so archiving it closes it first: say why. Both happen at once. To bring it back, Unarchive it, then Reopen it.`
        : "Closing says nobody will work on it, and why. It never ran, or it stopped: either way it ends here. Reopen brings it back to Backlog."}
      footerStart={refused ? <span className={styles["muted"]}>{refused}.</span> : undefined}
      footer={<><Button variant="quiet" onClick={() => onOpenChange(false)}>Cancel</Button>
        {refused
          ? <DisabledWithHint label={label} icon={andArchive ? "archive" : undefined} hint={refused} />
          : <Button variant="primary" leadingIcon={andArchive ? "archive" : undefined} disabled={!why || (why === "duplicate" && !dup)} data-testid="close-confirm">{label}</Button>}</>}>
      <FormStack>
        <ChoiceList<Resolution> label="Why it is closed" value={why ?? ("" as Resolution)} onChange={(r) => { setWhy(r); if (r === "duplicate" && !dup && !andArchive) setDup(task("CP-62")); }} data-testid="close-why"
          options={[
            { value: "wont_do", label: RESOLUTION.wont_do.label, icon: "circle-x", description: RESOLUTION.wont_do.description },
            { value: "duplicate", label: RESOLUTION.duplicate.label, icon: "copy", description: RESOLUTION.duplicate.description },
            { value: "cant_reproduce", label: RESOLUTION.cant_reproduce.label, icon: "question", description: RESOLUTION.cant_reproduce.description,
              disabledReason: isBug ? undefined : "Only for a bug: there is nothing to reproduce in a feature" },
            { value: "nothing_to_merge", label: RESOLUTION.nothing_to_merge.label, icon: "check",
              disabledReason: "Only for work that ran: Mark done gives this, once there is a write-up or an answer" },
          ]} />
        {why === "duplicate" ? (
          <div className={styles["stack"]} style={{ gap: 8 }} data-testid="duplicate-picker">
            {dup ? (
              <div className={styles["picked"]}>
                <RefLead type="task" taskKey={dup.key} status={dup.status} />
                <span className={styles["optionTitle"]}>{dup.title}</span>
                <KindMark kind={dup.kind} quietDefault />
                <StatusMark status={dup.status} size="sm" />
                <Button size="sm" variant="quiet" onClick={() => setDup(null)}>Change</Button>
              </div>
            ) : null}
            {!dup ? (
              <SearchPicker<Task> label="Duplicate of" placeholder="Find the task that covers it, by key or title" autoFocus={false} findOnEmpty find={find}
                optionKey={(x) => x.key}
                optionDisabled={(x) => (x.key === t.key ? "This task" : x.duplicateOf ? `Closed as a duplicate of ${x.duplicateOf}: pick that one` : null)}
                renderOption={(x) => (
                  <span className={styles["option"]}>
                    <RefLead type="task" taskKey={x.key} status={x.status} />
                    <span className={styles["optionTitle"]}>{x.title}</span>
                    <StatusMark status={x.status} size="sm" />
                  </span>
                )}
                onPick={setDup} empty={(q) => `No task in Acme matches “${q}”.`} />
            ) : null}
            {dup ? (
              <Callout tone="neutral" data-testid="duplicate-handover">
                {waiting.length
                  ? <>{waiting.map((w) => w.key).join(", ")} {waiting.length === 1 ? "waits" : "wait"} on {t.key}. Once closed, {waiting.length === 1 ? "it waits" : "they wait"} on <b>{dup.key}</b> instead, and {waiting.length === 1 ? "starts" : "start"} when {dup.key} is merged.</>
                  : <>Nothing waits on {t.key}.</>}
                {" "}{dup.key} gets “Duplicated by {t.key}” in its Relations.
              </Callout>
            ) : null}
          </div>
        ) : null}
        {why && why !== "duplicate" && waiting.length ? (
          <Callout tone="attention" data-testid="waits-on">
            <span className={styles["callTitle"]}>{waits} {one ? "waits" : "wait"} on this.</span>{" "}
            {unrelate === true
              ? <>Closing also removes the relation, so {one ? "it" : "they"} can start without {t.key}.</>
              : <>{one ? "It" : "They"} will keep waiting: a task closed without merging releases nothing.</>}
            <div className={styles["callActions"]} data-testid="remove-relations">
              <Checkbox checked={unrelate} onCheckedChange={setUnrelate}
                label="Remove the relation" description={`${waits} no longer ${one ? "waits" : "wait"} on ${t.key}; said in ${one ? "its" : "their"} Activity.`} />
            </div>
          </Callout>
        ) : null}
        <Textarea label="Note (optional)" rows={2} placeholder={andArchive ? "Said in Activity beside the close and the archive." : "Said in Activity beside the close."} />
      </FormStack>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// archive-task, archive-epic-refused
// ---------------------------------------------------------------------------

export function ArchiveDialog({ open, onOpenChange, t }: { readonly open: boolean; readonly onOpenChange: (o: boolean) => void; readonly t: Task }) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange} size="sm" context={context(t)} title={`Archive ${t.key}?`}
      description="It leaves the board, the sidebar, every count and Find work. A link to it still works, and Archived lists it. Unarchive puts it back as it was."
      footer={<><Button variant="quiet" onClick={() => onOpenChange(false)}>Keep it</Button><Button variant="primary" leadingIcon="archive" data-testid="archive-confirm">Archive</Button></>}>
      <ul className={styles["list"]}>
        <li><StatusMark status={t.status} size="sm" /><span className="ds-mono">{t.key}</span><span className={styles["optionTitle"]}>{t.title}</span></li>
      </ul>
    </Dialog>
  );
}

export function ArchiveEpicDialog({ open, onOpenChange }: { readonly open: boolean; readonly onOpenChange: (o: boolean) => void }) {
  const tasks = TASKS.filter((t) => t.epic === "webhooks" && !t.archived);
  const unfinished = tasks.filter((t) => !FINISHED.has(t.status));
  const finished = tasks.filter((t) => FINISHED.has(t.status));
  return (
    <Dialog open={open} onOpenChange={onOpenChange} size="md" title="Webhook reliability can't be archived yet"
      context={<Breadcrumb size="sm" current={false} items={[{ id: "p", label: "control-plane" }, { id: "e", label: "Webhook reliability", icon: "layers" }]} />}
      description={`${unfinished.length} of its tasks haven't ended. Archiving the epic would hide work that is still going on, so dude won't: finish them, close them, or move them to another epic first.`}
      footer={<><Button variant="quiet" onClick={() => onOpenChange(false)}>Cancel</Button>
        {ROLE === "owner"
          ? <Button variant="secondary" leadingIcon="archive" data-testid="archive-finished" data-lifecycle-action="">Archive its {finished.length} ended tasks</Button>
          : <DisabledWithHint label={`Archive its ${finished.length} ended tasks`} icon="archive" hint="Only Ana, who owns these tasks, or an admin can archive them" />}</>}>
      <div className={styles["stack"]}>
        <Fieldset legend={`Not ended · ${unfinished.length}`}>
          <ul className={styles["list"]} data-testid="unfinished">
            {unfinished.map((t) => (
              <li key={t.key}><StatusMark status={t.status} size="sm" /><span className="ds-mono">{t.key}</span><span className={styles["optionTitle"]}>{t.title}</span><span className={styles["muted"]}>{firstName(t.owner)}</span></li>
            ))}
          </ul>
        </Fieldset>
        <Fieldset legend={`Ended · ${finished.length}`} hint="These can go now: the epic stays, with only its unfinished tasks on the board.">
          <ul className={styles["list"]}>
            {finished.map((t) => (
              <li key={t.key}><StatusMark status={t.status} size="sm" /><span className="ds-mono">{t.key}</span><span className={styles["optionTitle"]}>{t.title}</span></li>
            ))}
          </ul>
        </Fieldset>
      </div>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// task-dialog-bug: the task dialog with kind = bug.
// ---------------------------------------------------------------------------

export function TaskDialogBug({ open, onOpenChange }: { readonly open: boolean; readonly onOpenChange: (o: boolean) => void }) {
  const [kind, setKind] = useState<Kind>("bug");
  const [priority, setPriority] = useState<Priority>("P1");
  const [steps, setSteps] = useState("1. Rename an organisation to “Acme, Inc.”.\n2. Export usage as CSV from the billing page.");
  const [goal, setGoal] = useState("Customers with a comma in their organisation's name get a CSV with **rows missing**. The export must quote every field (RFC 4180).");
  const aside = (
    <div className={styles["stack"]}>
      <Select label="Kind" value={kind} onValueChange={(v) => setKind(v as Kind)} data-testid="kind-select"
        options={(Object.keys(KIND) as Kind[]).map((k) => ({ value: k, label: KIND[k].label, description: KIND[k].line }))}
        hint={kind === "bug" ? "The implementer reproduces it and lands a failing test first." : undefined} />
      <Select label="Priority" value={priority} onValueChange={(v) => setPriority(v as Priority)}
        options={(Object.keys(PRIORITY) as Priority[]).map((p) => ({ value: p, label: p, meta: PRIORITY[p].line }))} />
      <Select label="Epic" value="billing" options={[{ value: "billing", label: "Billing v2" }, { value: "webhooks", label: "Webhook reliability" }, { value: "none", label: "No epic" }]} />
      <Fieldset legend="Introduced by" hint="Optional. An agent can propose it from what merged when; you confirm it.">
        <Input aria-label="Introduced by" placeholder="A task or pull request, CP-40 or #412" size="sm" />
      </Fieldset>
    </div>
  );
  return (
    <Dialog open={open} onOpenChange={onOpenChange} size="document" title="New task" asideLabel="Where it goes"
      context={<Breadcrumb size="sm" current={false} items={[{ id: "p", label: "control-plane" }, { id: "e", label: "Billing v2", icon: "layers" }]} />}
      aside={aside}
      footerStart={<KeyHint keys={["mod", "Enter"]}>create</KeyHint>}
      footer={<><Button variant="quiet" onClick={() => onOpenChange(false)}>Cancel</Button><Button variant="secondary">Create</Button><Button variant="primary" leadingIcon="zap">Create and deliver</Button></>}>
      <FormStack fill>
        <Input label="Title" labelNote="required" size="title" defaultValue="CSV export drops rows when an organisation's name has a comma" />
        <MarkdownEditor label="Goal" labelNote="required" hint="What is wrong for whom, and what right looks like." value={goal} onChange={setGoal} minRows={3} breaks />
        {kind === "bug" ? (
          <Fieldset legend="The bug" data-testid="bug-fields">
            <div className={styles["stack"]}>
              <MarkdownEditor label="Steps to reproduce" labelNote="required for a bug" hint="One numbered step a line." value={steps} onChange={setSteps} minRows={3} breaks />
              <div className={styles["row2"]}>
                <Textarea label="Expected" rows={2} defaultValue="Every hour of usage, one row each." />
                <Textarea label="Actual" rows={2} defaultValue="Rows for Acme, Inc. are missing; those lines have one column too many." />
              </div>
              <Input label="Environment" labelNote="optional" hint="Version, where it was seen. Short." mono defaultValue="control-plane 1.18.0, staging" maxLength={120} />
            </div>
          </Fieldset>
        ) : null}
        <MarkdownEditor label="Acceptance criteria" hint="One list item per criterion." value={"- [ ] A name with a comma, a quote or a newline exports every row\n- [ ] The failing test from the reproduction passes"} onChange={() => undefined} minRows={2} />
      </FormStack>
    </Dialog>
  );
}
