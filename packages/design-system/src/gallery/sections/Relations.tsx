import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { Block, Col, Label, Panes, Row, Section, type PaneMode } from "../Frame.tsx";
import { Board } from "../../components/Board.tsx";
import { ProposalCard, type ProposalCardItem } from "../../components/Brainstorm.tsx";
import { AutoStartDialog, RelationPicker, StartAnywayDialog, type PickableTask } from "../../components/RelationDialogs.tsx";
import {
  AutoStartMark, DependencyStrip, RelationChip, RelationList, StartGate, StartMenu, type RelatedTask, type RelationGroupData, type StripTask,
} from "../../components/Relations.tsx";
import { Icon } from "../../icons/index.tsx";
import { Button } from "../../primitives/Button.tsx";
import { boardColumns, dependencySteps, neighbourKeys } from "../../util/boardModel.ts";
import type { NavEpic, NavProject, NavTask } from "../../util/navModel.ts";

/*
 * Task relations (docs/design/task-relations.md): every piece in every
 * state, dark and light, both densities — the chip, the card's start and
 * its menu, start when unblocked, the refused start, the task's Relations,
 * the picker and its loop, the epic's order, and the proposal card's
 * order and relations.
 */

const ana = { id: "per_ana", name: "Ana Nunes" };
const tom = { id: "per_tom", name: "Tom Okafor" };
const marcio = { id: "per_marcio", name: "Márcio Martins" };

const meter: RelatedTask = { key: "CP-41", title: "Hourly usage meter", status: "review" };
const sync: RelatedTask = { key: "CP-43", title: "Stripe sync", status: "running" };
const failed: RelatedTask = { key: "CP-38", title: "Plan limits", status: "failed" };
const spike: RelatedTask = { key: "CP-37", title: "Pricing spike", status: "done", unmerged: true };
const web: RelatedTask = { key: "WEB-12", title: "Billing settings page", status: "review", project: "web-console" };
const waiting: RelatedTask[] = [
  { key: "CP-42", title: "Invoice line items", status: "received" },
  { key: "CP-44", title: "Billing page", status: "received" },
  { key: "CP-45", title: "Invoice email", status: "queued" },
  { key: "CP-46", title: "Usage chart", status: "received", project: "web-console" },
];

const t = (key: string, title: string, status: NavTask["status"], more: Partial<NavTask> = {}): NavTask =>
  ({ id: key, key, title, status, people: [ana], ...more });
const edges = (blockedBy: string[], blocks: string[], related: string[] = []) => ({ blockedBy, blocks, related });

const billingTasks: NavTask[] = [
  t("CP-40", "Meter schema", "done", { merged: true, edges: edges([], ["CP-41"]) }),
  t("CP-41", "Hourly usage meter", "review", { blocks: [waiting[0]!, waiting[1]!], edges: edges(["CP-40"], ["CP-42", "CP-44"]) }),
  t("CP-43", "Stripe sync", "running", { blocks: [waiting[1]!], edges: edges([], ["CP-44"]) }),
  t("CP-42", "Invoice line items", "received", { blockedBy: [meter], canStart: true, startable: false, people: [tom],
    autoStart: { how: "deliver", by: tom, at: Date.now() - 2 * 86_400_000 }, edges: edges(["CP-41"], []) }),
  t("CP-44", "Billing page", "received", { blockedBy: [meter, sync], canStart: true, startable: false, edges: edges(["CP-41", "CP-43"], ["CP-45"]) }),
  t("CP-45", "Invoice email", "queued", { blockedBy: [{ key: "CP-44", title: "Billing page", status: "received" }], canStart: true, startable: false, edges: edges(["CP-44"], []) }),
  t("CP-47", "Plan picker copy", "received", { canStart: true, startable: true }),
  t("CP-37", "Pricing spike", "done", { merged: false, blocks: [{ key: "CP-51", title: "Per-unit prices", status: "received" }], edges: edges([], ["CP-51"]) }),
  t("CP-51", "Per-unit prices", "received", { blockedBy: [spike], canStart: true, startable: false, edges: edges(["CP-37"], []) }),
];
const billingEpic: NavEpic = { id: "e_billing", title: "Billing v2", tasks: billingTasks };
const project: NavProject = { id: "prj_cp", name: "control-plane", epics: [billingEpic] };

/** A card's start the way the app draws it: the reader may start every card here. */
function cardStart(card: { task: NavTask }, focus: { tabIndex: 0 | -1 }, owner = true, open = false): ReactNode {
  const task = card.task;
  if (!task.canStart) return null;
  const blockers = task.blockedBy ?? [];
  return (
    <StartMenu taskKey={task.key!} blockers={blockers} ownerName="Ana" tabIndex={focus.tabIndex} open={open || undefined}
      onStart={() => undefined} onStartAnyway={blockers.length ? () => undefined : undefined}
      onStartWhenUnblocked={owner ? () => undefined : undefined}
      startWhenUnblockedRefused={blockers.some((b) => b.unmerged) ? "CP-37 was marked done with nothing merged and will never merge, so this would never start it." : null}
      auto={task.autoStart ? { how: task.autoStart.how, by: "Tom", ago: "2d", onCancel: owner ? () => undefined : undefined } : undefined} />
  );
}

function stripOf(epic: NavEpic): { steps: StripTask[][]; counts: Parameters<typeof DependencyStrip>[0]["counts"] } {
  const steps = dependencySteps(epic).map((s) => s.map((x): StripTask => ({
    key: x.key!, title: x.title, status: x.status, unmerged: x.status === "done" && x.merged === false,
    startable: Boolean(x.startable), blockedBy: (x.blockedBy ?? []).map((b) => b.key),
  })));
  const all = epic.tasks;
  return { steps, counts: {
    startable: all.filter((x) => x.startable).length,
    blocked: all.filter((x) => (x.blockedBy?.length ?? 0) > 0).length,
    working: all.filter((x) => ["running", "review", "ready_to_merge", "awaiting_input"].includes(x.status)).length,
    merged: all.filter((x) => x.status === "done" && x.merged).length,
    doneUnmerged: all.filter((x) => x.status === "done" && x.merged === false).length,
    failed: all.filter((x) => x.status === "failed").length,
  } };
}

function BoardDemo({ highlight, filter: initial = "all", owner = true, canStart = true }: { readonly highlight?: string; readonly filter?: "all" | "startable" | "blocked"; readonly owner?: boolean; readonly canStart?: boolean }) {
  const [filter, setFilter] = useState(initial);
  const [lit, setLit] = useState<string | null>(null);
  const focus = highlight ?? lit;
  const task = focus ? billingTasks.find((x) => x.key === focus) : null;
  const keys = task ? new Set([task.key!, ...neighbourKeys(task)]) : null;
  const strip = stripOf(billingEpic);
  return (
    <div style={{ height: 620, display: "flex" }}>
      <Board project={project} epic={billingEpic} filter={filter} onFilterChange={setFilter}
        highlight={highlight ? keys : undefined} onHighlightChange={setLit}
        overview={<DependencyStrip steps={strip.steps} counts={strip.counts} highlight={keys} />}
        cardStart={canStart ? (card, f) => cardStart(card, f, owner) : undefined} />
    </div>
  );
}

const groups = (hint?: boolean): RelationGroupData[] => [
  { kind: "blocked-by", rows: [
    { ...meter, owner: marcio, agent: "reviewer" },
    { ...sync, owner: tom, agent: "implementer" },
    { key: "CP-40", title: "Meter schema", status: "done", owner: ana, settled: true },
    { ...web, owner: tom },
  ] },
  { kind: "blocks", rows: [{ key: "CP-45", title: "Invoice email", status: "queued", owner: marcio,
    removeHint: hint ? "Only Márcio, CP-45's owner, can remove this" : undefined }] },
  { kind: "related", rows: [{ key: "WEB-9", title: "A long title that a phone truncates before anything else in the row is cut", status: "received", owner: ana, project: "web-console" }] },
];

const find = async (q: string): Promise<PickableTask[]> => ([
  { key: "CP-40", title: "Meter schema", status: "done", group: "This epic" },
  { key: "CP-41", title: "Hourly usage meter", status: "review", group: "This epic" },
  { key: "CP-44", title: "Billing page", status: "received", group: "This epic" },
  { key: "CP-12", title: "Retry webhooks", status: "running", group: "control-plane" },
  { key: "WEB-12", title: "Billing settings page", status: "review", group: "Other projects" },
] satisfies PickableTask[]).filter((x) => !q || `${x.key} ${x.title}`.toLowerCase().includes(q.toLowerCase()));

const proposal: ProposalCardItem[] = [
  { kind: "task", n: 1, title: "Record limit crossings", detail: "Write a row when an organisation's usage crosses 80% and 100% of its plan.",
    project: { key: "CP", name: "control-plane" }, canFile: true, tickedWith: [2, 3], relations: [{ kind: "blocked-by", task: { key: "CP-41", status: "review" } }] },
  { kind: "task", n: 2, title: "Email owners at 80%", detail: "Tell an organisation's owners when it nears its plan's limit.",
    project: { key: "CP", name: "control-plane" }, canFile: true, relations: [{ kind: "blocked-by", item: 1 }] },
  { kind: "task", n: 3, title: "Mark the limit on the usage chart", detail: "A line on CP-44's chart where the plan's limit is.",
    project: { key: "CP", name: "control-plane" }, canFile: true, relations: [{ kind: "blocked-by", item: 1 }, { kind: "related", task: { key: "CP-44", status: "received" } }] },
];

const relationBlockIds: ReadonlySet<string> = new Set([
  "rel-icons", "rel-chip", "rel-board", "rel-board-lit", "rel-board-filter", "rel-startmenu", "rel-gate",
  "rel-auto", "rel-dialogs", "rel-list", "rel-picker", "rel-strip", "rel-proposal",
]);

const noAnchors: ReadonlySet<string> = new Set();

/**
 * Mounts its children once they near the viewport. The section's blocks are
 * about 8,000 elements and 190 running animations: drawn from the start they
 * slow every other section's animations, enough that a tooltip elsewhere in the
 * gallery could take seconds to fade in. A gallery without IntersectionObserver
 * draws them at once.
 */
export function WhenNear({ children, anchors = noAnchors, pendingTestId = "relations-pending" }: {
  readonly children: ReactNode;
  readonly anchors?: ReadonlySet<string>;
  /** Each deferred section's placeholder needs its own id: tests count them. */
  readonly pendingTestId?: string;
}) {
  const [near, setNear] = useState(() => typeof IntersectionObserver === "undefined");
  const placeholder = useRef<HTMLDivElement>(null);
  const pendingTarget = useRef<string | null>(null);
  useEffect(() => {
    if (near) return undefined;
    const navigate = () => {
      let id: string;
      try { id = decodeURIComponent(window.location.hash.slice(1)); }
      catch { pendingTarget.current = null; return; }
      pendingTarget.current = anchors.has(id) ? id : null;
      if (pendingTarget.current) setNear(true);
    };
    window.addEventListener("hashchange", navigate);
    navigate();
    return () => window.removeEventListener("hashchange", navigate);
  }, [near, anchors]);
  useLayoutEffect(() => {
    if (!near || !pendingTarget.current) return;
    // Native fragment navigation ran before the deferred target existed.
    document.getElementById(pendingTarget.current)?.scrollIntoView();
    pendingTarget.current = null;
  }, [near]);
  useEffect(() => {
    if (near || !placeholder.current) return undefined;
    const seen = new IntersectionObserver((entries) => {
      if (entries.some((e) => e.isIntersecting)) setNear(true);
    }, { rootMargin: "1500px 0px" });
    seen.observe(placeholder.current);
    return () => seen.disconnect();
  }, [near]);
  return near ? <>{children}</> : <div ref={placeholder} data-testid={pendingTestId} style={{ minHeight: 6000 }} />;
}

export function RelationsSection({ mode }: { readonly mode: PaneMode }) {
  return (
    <Section id="relations" title="Task relations: blocked by, blocks, related"
      intro="A task that can't start until another is merged says so, in one verb pair everywhere — Blocked by, Blocks — and every way to start it refuses for the same reason. Blocked waits on work, not a person: neutral ink with a lock and words, never the attention wash, and never a triage bucket.">
      <WhenNear anchors={relationBlockIds}>
      <Block id="rel-icons" title="Icons: lock, blocks, link"
        note="lock marks a task that can't start until another is merged, blocks one that holds another up, link a relation that gates nothing. They take the ink of their words, never a tone.">
        <Panes mode={mode}>
          <Row>{(["lock", "blocks", "link"] as const).map((n) => <span key={n} style={{ display: "inline-flex", gap: 6, alignItems: "center" }}><Icon name={n} size={16} />{n}</span>)}</Row>
        </Panes>
      </Block>

      <Block id="rel-chip" title="RelationChip"
        note="Blocked by one (each status: in review, running, failed, done with nothing merged, another project — said in the tooltip only), blocked by three, blocks one, blocks four, both on one card, and a narrow card where the key truncates last.">
        <Panes mode={mode} surface>
          <Col style={{ gap: 8 }}>
            <Row>{[meter, sync, failed, spike, web].map((b) => <RelationChip key={b.key} kind="blocked-by" tasks={[b]} />)}</Row>
            <Row>
              <RelationChip kind="blocked-by" tasks={[meter, sync, failed]} />
              <RelationChip kind="blocks" tasks={[waiting[1]!]} />
              <RelationChip kind="blocks" tasks={waiting} />
            </Row>
            <Row><RelationChip kind="blocked-by" tasks={[meter]} /><RelationChip kind="blocks" tasks={waiting.slice(0, 2)} /></Row>
            <div style={{ width: 120 }}><RelationChip kind="blocked-by" tasks={[{ key: "CONTROL-1041", title: "x", status: "review" }]} /></div>
            <div style={{ filter: "grayscale(1)" }}><Row><RelationChip kind="blocked-by" tasks={[meter]} /><RelationChip kind="blocks" tasks={waiting} /></Row></div>
          </Col>
        </Panes>
      </Block>

      <Block id="rel-board" title="Board: card marks, the card's start, the filter"
        note="The epic board with the order strip as its overview. A blocked card carries its chip; a blocking card the quieter one; CP-42 says when it starts on its own. Backlog cards have a start beside them — a sibling of the card, tabbable only beside the card with the board's focus: card → Tab → its start → Enter → menu → Escape → start → Shift+Tab → card. Hover a card to light its relations; a live region says it in words.">
        <Panes mode={mode}><BoardDemo /></Panes>
      </Block>

      <Block id="rel-board-lit" title="Board: lit on a blocker, on a blocked task, on a card with none"
        note="CP-41 lit: CP-40 in Done, CP-42 and CP-44 in Backlog. Then CP-44, and CP-47, which relates to nothing: everything else dims.">
        <Panes mode={mode}><BoardDemo highlight="CP-41" /></Panes>
        <Panes mode={mode}><BoardDemo highlight="CP-44" /></Panes>
        <Panes mode={mode}><BoardDemo highlight="CP-47" /></Panes>
      </Block>

      <Block id="rel-board-filter" title="Board: Can start, Blocked, and a reader with no right to start"
        note="The filter is a view, not a triage bucket: blocked needs nobody. A reader who may not start sees no starts; the chips still say why a card waits.">
        <Panes mode={mode}><BoardDemo filter="startable" /></Panes>
        <Panes mode={mode}><BoardDemo filter="blocked" owner={false} /></Panes>
        <Panes mode={mode}><BoardDemo canStart={false} /></Panes>
      </Block>

      <Block id="rel-startmenu" title="StartMenu"
        note="Unblocked; blocked (ways disabled with the reason) as owner and as not; set to start when unblocked as owner and as not; a blocker that will never merge; open.">
        <Panes mode={mode} surface>
          {(theme) => (
            <Col style={{ gap: 12 }}>
              <Row>
                <StartMenu taskKey="CP-47" blockers={[]} onStart={() => undefined} ownerName="Ana" />
                <StartMenu taskKey="CP-44" blockers={[meter, sync]} onStart={() => undefined} onStartAnyway={() => undefined} onStartWhenUnblocked={() => undefined} ownerName="Ana" />
                <StartMenu taskKey="CP-44" blockers={[meter, sync]} onStart={() => undefined} onStartAnyway={() => undefined} ownerName="Ana" />
                <StartMenu taskKey="CP-51" blockers={[spike]} onStart={() => undefined} onStartAnyway={() => undefined} onStartWhenUnblocked={() => undefined}
                  startWhenUnblockedRefused="CP-37 was marked done with nothing merged and will never merge, so this would never start it." ownerName="Ana" />
              </Row>
              <MenuOpen theme={theme} />
            </Col>
          )}
        </Panes>
      </Block>

      <Block id="rel-gate" title="StartGate"
        note="One blocker, two, a failed blocker, a blocker done with nothing merged; as owner and not; without Start anyway (a reader with no right to start).">
        <Panes mode={mode} surface>
          <Col style={{ gap: 12 }}>
            <StartGate blockers={[meter]} starts={STARTS} onStartAnyway={() => undefined} onStartWhenUnblocked={() => undefined} />
            <StartGate blockers={[meter, sync]} starts={STARTS} onStartAnyway={() => undefined} />
            <StartGate blockers={[failed]} starts={STARTS} onStartAnyway={() => undefined} onStartWhenUnblocked={() => undefined} />
            <StartGate blockers={[spike]} starts={STARTS} onStartAnyway={() => undefined} onStartWhenUnblocked={() => undefined}
              startWhenUnblockedRefused="CP-37 was marked done with nothing merged and will never merge, so this would never start it." />
            <StartGate blockers={[meter]} starts={STARTS} />
          </Col>
        </Panes>
      </Block>

      <Block id="rel-auto" title="AutoStartMark"
        note="One blocker, two; as owner (Cancel) and not; compact, as on a card.">
        <Panes mode={mode} surface>
          <Col style={{ gap: 8 }}>
            <AutoStartMark blockers={[meter]} auto={{ how: "deliver", by: "Ana", ago: "10m", onCancel: () => undefined }} />
            <AutoStartMark blockers={[meter, sync]} auto={{ how: "talk", by: "Ana", ago: "2d" }} />
            <div style={{ width: 220 }}><AutoStartMark compact blockers={[meter]} auto={{ how: "deliver", by: "Tom", ago: "2d" }} /></div>
          </Col>
        </Panes>
      </Block>

      <Block id="rel-dialogs" title="Start anyway / AutoStartDialog"
        note="Start anyway names each blocker and what starting now means; Keep waiting has the focus, the confirm is primary. Start when unblocked preselects nothing: Set stays disabled until a way is picked. Press to open.">
        <Panes mode={mode}><DialogDemos /></Panes>
      </Block>

      <Block id="rel-list" title="RelationList / RelationRow"
        note="All three groups, a settled blocker (1 of 2 open… here 3 of 4), a cross-project row, a long title; read by someone who isn't the owner (the hint showing in the group's head); read only (no onRemove); empty.">
        <Panes mode={mode} surface>
          <Col style={{ gap: 24 }}>
            <RelationList groups={groups()} onRemove={() => undefined} onAdd={() => undefined} />
            <RelationList groups={groups(true)} onRemove={() => undefined} onAdd={() => undefined} hinting="blocks:CP-45" />
            <RelationList groups={groups()} />
            <Label>Full main column (wide)</Label>
            <RelationList wide groups={groups()} onRemove={() => undefined} onAdd={() => undefined} />
            <RelationList groups={[]} onAdd={() => undefined} />
          </Col>
        </Panes>
      </Block>

      <Block id="rel-picker" title="RelationPicker"
        note="Empty with suggestions (type to see groups and disabled options), and the loop refused with Make it Related instead.">
        <Panes mode={mode} surface>
          <Col style={{ gap: 16 }}>
            <RelationPicker here={{ key: "CP-44" }} related={new Set(["CP-41"])} find={find} onAdd={async () => undefined} onClose={() => undefined} autoFocus={false} />
            <RelationPicker here={{ key: "CP-40" }} related={new Set()} find={find} onAdd={async () => undefined} onClose={() => undefined}
              initialLoop={["CP-40", "CP-41", "CP-44", "CP-40"]} autoFocus={false} />
          </Col>
        </Panes>
      </Block>

      <Block id="rel-strip" title="DependencyStrip"
        note="A chain in steps with what can start; a single step is no strip (nothing drawn); a twelve-task epic scrolls sideways.">
        <Panes mode={mode}>
          <Col style={{ gap: 12 }}>
            <DependencyStrip {...stripOf(billingEpic)} />
            <div style={{ maxWidth: 480 }}><DependencyStrip {...stripOf(long)} /></div>
          </Col>
        </Panes>
      </Block>

      <Block id="rel-proposal" title="ProposalCard: order and relations"
        note="Numbered items in dependency order, each item's relations under its goal, the order in the head; ticking 2 and 3 ticks 1, saying why; the blocker unfiled; read only.">
        <Panes mode={mode}>
          <Col style={{ gap: 16 }}>
            <ProposalCard items={proposal} selected={new Set([1, 2])} onToggle={() => undefined} onFile={() => undefined} filingAs="Márcio" order={[[1], [2, 3]]} />
            <ProposalCard items={proposal.map((p, i) => ({ ...p, canFile: false, why: i === 0 ? "data-pipeline is no longer linked to this session."
              : "Waits on item 1, which can't be filed: link data-pipeline again, or ask the brainstorm to propose it without item 1." }))}
              selected={new Set()} onToggle={() => undefined} onFile={() => undefined} filingAs="Márcio" order={[[1], [2, 3]]} />
            <ProposalCard items={proposal} selected={new Set()} onToggle={() => undefined} onFile={() => undefined} filingAs="Márcio" order={[[1], [2, 3]]} readOnly defaultOpen />
          </Col>
        </Panes>
      </Block>
      </WhenNear>
    </Section>
  );
}

const STARTS = [{ id: "talk", label: "Talk it through", icon: "message" as const }, { id: "deliver", label: "Deliver", icon: "zap" as const }];

const long: NavEpic = { id: "e_long", title: "Long", tasks: Array.from({ length: 12 }, (_, i) => t(`CP-${60 + i}`, `Step ${i + 1}`, i < 3 ? "done" : i === 3 ? "review" : "received", {
  merged: i < 3, startable: i === 4, blockedBy: i > 4 ? [{ key: `CP-${59 + i}`, title: "", status: "received" }] : undefined,
  edges: edges(i > 0 ? [`CP-${59 + i}`] : [], i < 11 ? [`CP-${61 + i}`] : []) })) };

/** The menu drawn open, in place, in its pane's theme. */
function MenuOpen({ theme }: { readonly theme: string }) {
  const [host, setHost] = useState<HTMLElement | null>(null);
  return (
    <div ref={setHost} data-theme={theme} style={{ minHeight: 260, position: "relative" }}>
      {host ? <StartMenu taskKey="CP-42" blockers={[meter]} onStart={() => undefined} onStartAnyway={() => undefined} ownerName="Tom"
        auto={{ how: "deliver", by: "Tom", ago: "2d" }} open forceMount container={host} /> : null}
    </div>
  );
}

function DialogDemos() {
  const [open, setOpen] = useState<"anyway" | "anywayDeliver" | "auto" | "autoPicked" | null>(null);
  const blockers = [{ ...meter, owner: marcio, agent: "reviewer" as const }, { ...sync, owner: tom, agent: "implementer" as const }];
  return (
    <Row>
      <Button variant="secondary" onClick={() => setOpen("anyway")}>Start anyway (talk)</Button>
      <Button variant="secondary" onClick={() => setOpen("anywayDeliver")}>Start anyway (deliver)</Button>
      <Button variant="secondary" onClick={() => setOpen("auto")}>Start when unblocked</Button>
      <Button variant="secondary" onClick={() => setOpen("autoPicked")}>…a way picked</Button>
      <StartAnywayDialog open={open === "anyway" || open === "anywayDeliver"} onOpenChange={(o) => setOpen(o ? open : null)} taskKey="CP-44"
        blockers={blockers} how={open === "anywayDeliver" ? "deliver" : "talk"} onConfirm={() => setOpen(null)} />
      <AutoStartDialog open={open === "auto" || open === "autoPicked"} onOpenChange={(o) => setOpen(o ? open : null)} taskKey="CP-44"
        blockers={blockers} chosen={open === "autoPicked" ? "deliver" : undefined} onConfirm={() => setOpen(null)} />
    </Row>
  );
}

/** For tests: every card on the demo board. */
export const relationsBoardCards = () => boardColumns(project, billingEpic).flatMap((c) => c.cards);
