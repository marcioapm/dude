/*
 * Children mockup 2: when the parent spawns subagents and keeps working.
 * One scenario (a Codex implementer, two children, twenty turns of its own
 * after the spawn), three treatments of where the child lives, and three
 * ways to open one. The switcher above the app is not part of the proposal.
 */

import { StrictMode, useState } from "react";
import { createRoot } from "react-dom/client";
import "@dude/design-system/tokens.css";
import "@dude/design-system/base.css";
import "../../src/app.css";
import { ThemeProvider, useTheme, type ThemePreference, Icon } from "@dude/design-system";
import {
  AgentAvatar, ChatMessage, ChatNotice, ChatThread, ChatTranscript, Segmented, SessionFacts, SessionRail, SessionRailBlock, Sidebar, SidebarLink, SidebarProfile, ToolCallCard,
} from "@dude/design-system/components";
import { Badge, Button, Dialog, IconButton, ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { navProjects } from "../../../../packages/design-system/src/gallery/navFixtures.ts";
import { KIDS, NOW, PARENT_AFTER, RUN, ago, type Kid } from "./data.ts";

const P = { id: "u_marcio", name: "Márcio Martins", online: true };

type Place = "inline" | "folded" | "rail";
type Open = "dialog" | "panel" | "route";

const PLACE_NOTE: Record<Place, string> = {
  inline: "A · Today (#123): the thread is a block in the flow, open while live. Scroll down: twenty parent turns later it is gone from view, and the waiting one with it.",
  folded: "B · Folded in the flow: one row per child at the spawn point, always one line, status and last event in the row. Still buried once the parent moves on; the rail's Subagents block is the way back.",
  rail: "C · Rail + folded rows: the rail's Subagents block is the quicklink — one line per child, live dot, waiting in attention tone — and stays put while the transcript scrolls. The row at the spawn point stays as the anchor.",
};
const OPEN_NOTE: Record<Open, string> = {
  dialog: "Open in a dialog over the transcript: the child's own scrollable transcript, header with cost/turns, Close returns to where you were.",
  panel: "Open in a panel beside the transcript (the rail's width grows): parent and child scroll independently; you watch both.",
  route: "Open on its own page: the child's transcript as a full session (breadcrumb back). Best for a long child; loses the parent from view.",
};

function Mock() {
  const theme = useTheme();
  const [place, setPlace] = useState<Place>("rail");
  const [open, setOpen] = useState<Open>("panel");
  const [opened, setOpened] = useState<string | null>(null);
  const compactRail = false;
  const kid = KIDS.find((k) => k.id === opened) ?? null;
  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100%" }}>
      <div className="mockbar">
        <b>dude · Where a subagent lives when its parent keeps working (mockup 2)</b>
        <Segmented size="sm" label="Where" value={place} onChange={(p) => { setPlace(p); setOpened(null); }} options={[
          { value: "inline", label: "A · Inline, open (today)" }, { value: "folded", label: "B · Folded row" }, { value: "rail", label: "C · Rail quicklink + row" },
        ]} />
        <Segmented size="sm" label="Open as" value={open} onChange={(o) => { setOpen(o); setOpened(null); }} options={[
          { value: "dialog", label: "Dialog" }, { value: "panel", label: "Side panel" }, { value: "route", label: "Own page" },
        ]} />
        <Segmented size="sm" label="Theme" value={theme.resolved} onChange={(t) => theme.setPreference(t as ThemePreference)} options={[{ value: "dark", label: "Dark" }, { value: "light", label: "Light" }]} />
      </div>
      <div className="mocknote">{PLACE_NOTE[place]} {opened ? OPEN_NOTE[open] : "Click a subagent (row, rail entry, or thread header) to open it."}</div>
      <div className="shell" style={{ flex: 1, minHeight: 0 }}>
        <Sidebar projects={navProjects} title="dude" footer={<><SidebarLink icon="building">Organisation settings</SidebarLink><SidebarProfile person={P} detail="marcio@acme.dev" onOpen={() => undefined} /></>} />
        <main className="main flush" style={{ display: "flex", flexDirection: "column", minHeight: 0 }}>
          {open === "route" && kid ? (
            <ChildPage kid={kid} onBack={() => setOpened(null)} />
          ) : (
            <div className="runChat" style={{ flex: 1, minHeight: 0 }}>
              <div className="runDrop" style={{ display: "flex", minHeight: 0 }}>
                <Transcript place={place} onOpen={setOpened} opened={open === "panel" ? opened : null} />
              </div>
              {open === "panel" && kid ? (
                <ChildPanel kid={kid} onClose={() => setOpened(null)} />
              ) : (
                <SessionRail className="runRail" aria-label="This session">
                  <SessionRailBlock label="Session">
                    <SessionFacts facts={[{ label: "Model", value: RUN.model, mono: true }, { label: "Cost", value: "$2.14" }, { label: "Running", value: "26m" }]} />
                  </SessionRailBlock>
                  {place === "rail" ? <SubagentsBlock onOpen={setOpened} opened={opened} compact={compactRail} /> : null}
                  <SessionRailBlock label="Files changed" live>
                    <SessionFacts facts={[{ label: "docs/runbooks/revenue-chart.md", value: "+5 −3", mono: true }, { label: "tests/suites/ui_previewer/conftest.py", value: "+1 −1", mono: true }]} />
                  </SessionRailBlock>
                </SessionRail>
              )}
            </div>
          )}
          {open === "dialog" && kid ? (
            <Dialog open onOpenChange={(o) => { if (!o) setOpened(null); }} size="xl" title={<KidTitle kid={kid} />} description={`${kid.kind} · started by the implementer's spawn_agent call · ${kid.status === "running" ? "running" : "done"}`}>
              <div className="dialogFill" style={{ flexDirection: "column" }}>
                <KidTranscript kid={kid} fill />
              </div>
            </Dialog>
          ) : null}
        </main>
      </div>
    </div>
  );
}

/** The rail's quicklink: one line per child, live dot, waiting in attention tone. */
function SubagentsBlock({ onOpen, opened, compact }: { onOpen: (id: string) => void; opened: string | null; compact: boolean }) {
  const live = KIDS.some((k) => k.status === "running");
  return (
    <SessionRailBlock label={<>Subagents <span style={{ color: "var(--ds-color-text-muted)", fontWeight: 400 }}>· {KIDS.length}</span></>} live={live} data-testid="rail-subagents">
      <div style={{ display: "flex", flexDirection: "column", gap: 2 }}>
        {KIDS.map((k) => (
          <button key={k.id} type="button" onClick={() => onOpen(k.id)} aria-current={opened === k.id ? "true" : undefined}
            style={{ all: "unset", cursor: "pointer", display: "grid", gridTemplateColumns: "20px 1fr auto", alignItems: "center", gap: 8, padding: "6px 8px", borderRadius: 6,
              background: opened === k.id ? "var(--ds-color-wash)" : k.waiting ? "var(--ds-color-attention-bg)" : "transparent",
              borderLeft: `2px solid ${k.waiting ? "var(--ds-color-attention-mark)" : "var(--ds-role-subagent-solid, var(--ds-role-investigator-solid))"}` }}>
            <AgentAvatar role="subagent" size="sm" live={k.status === "running"} />
            <span style={{ minWidth: 0, display: "flex", flexDirection: "column", lineHeight: 1.25 }}>
              <span style={{ fontSize: 13, color: "var(--ds-color-text)", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{k.title}</span>
              {!compact ? (
                <span style={{ fontSize: 12, color: k.waiting ? "var(--ds-color-attention-fg)" : "var(--ds-color-text-muted)", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                  {k.waiting ? <><Icon name="pause" size={11} /> waiting <Dur since={k.waiting.since} /> on {k.waiting.on}</> : k.status === "completed" ? "done · " + k.summary : k.last}
                </span>
              ) : null}
            </span>
            <span style={{ fontSize: 12, color: "var(--ds-color-text-muted)", fontVariantNumeric: "tabular-nums" }}>{k.status === "completed" ? <Icon name="check" size={12} /> : <Dur since={k.startedAt} />}</span>
          </button>
        ))}
      </div>
    </SessionRailBlock>
  );
}

/** Treatment B/C: the row at the spawn point — one line, never a body. */
function KidRow({ kid, onOpen, opened }: { kid: Kid; onOpen: (id: string) => void; opened: string | null }) {
  return (
    <ChatThread sessionId={kid.id} role="subagent" status={kid.status} model={RUN.model} task={`${kid.title} · ${kid.kind}`}
      startedAt={kid.startedAt} endedAt={kid.endedAt} costUsd={kid.costUsd} turnCount={kid.turnCount}
      summary={kid.waiting ? undefined : kid.status === "completed" ? kid.summary : kid.last}
      waiting={kid.waiting ? { on: kid.waiting.on, since: kid.waiting.since, detail: kid.waiting.detail } : undefined}
      activity={kid.status === "completed" ? "completed" : "tool"} activityProps={kid.status === "completed" ? undefined : { tool: "grep", since: kid.waiting?.since ?? ago(0.3) }}
      collapsed onCollapsedChange={() => onOpen(kid.id)} onOpen={() => onOpen(kid.id)} data-opened={opened === kid.id ? "true" : undefined} />
  );
}

/** Treatment A: the thread as #123 draws it, open while live. */
function KidInline({ kid }: { kid: Kid }) {
  return (
    <ChatThread sessionId={kid.id} role="subagent" status={kid.status} model={RUN.model} task={`${kid.title} · ${kid.kind}`}
      startedAt={kid.startedAt} endedAt={kid.endedAt} costUsd={kid.costUsd} turnCount={kid.turnCount}
      summary={kid.status === "completed" ? kid.summary : undefined}
      waiting={kid.waiting ? { on: kid.waiting.on, since: kid.waiting.since, detail: kid.waiting.detail } : undefined}
      activity={kid.status === "completed" ? "completed" : "tool"} activityProps={kid.status === "completed" ? undefined : { tool: "grep", since: kid.waiting?.since ?? ago(0.3) }}
      defaultCollapsed={kid.status === "completed"} onOpen={() => undefined}>
      <KidTurns kid={kid} />
    </ChatThread>
  );
}

function KidTurns({ kid }: { kid: Kid }) {
  return (
    <>
      {kid.turns.map((t, i) => t.kind === "tool"
        ? <ToolCallCard key={i} name={t.tool} status={t.done ? "completed" : "running"} args={t.args} startedAt={t.at} durationMs={t.ms} result={t.result} slowAfterMs={30_000}
            note={!t.done && kid.waiting ? <span><Badge tone="attention" size="sm" icon="pause">waiting</Badge> asked for <code>{kid.waiting.detail}</code> — nothing in this Run can answer it.</span> : undefined} defaultExpanded={!t.done} />
        : <ChatMessage key={i} role="subagent" model={RUN.model} content={t.text} startedAt={t.at} endedAt={t.at + 6_000} costUsd={t.cost} />)}
      {kid.waiting ? <ChatNotice kind="notice" at={kid.waiting.since} text={`Waiting on ${kid.waiting.on}: ${kid.waiting.detail}. Nothing in this Run can answer it.`} /> : null}
      {kid.status === "completed" ? <ChatMessage role="subagent" model={RUN.model} content={kid.summary} startedAt={kid.endedAt! - 5_000} endedAt={kid.endedAt!} costUsd={0.02} activity="completed" /> : null}
    </>
  );
}

function KidTitle({ kid }: { kid: Kid }) {
  return <span style={{ display: "inline-flex", alignItems: "center", gap: 8 }}><AgentAvatar role="subagent" size="md" live={kid.status === "running"} /> {kid.title}</span>;
}

/** The child's own transcript: a full ChatTranscript, scrolling on its own. */
function KidTranscript({ kid, fill }: { kid: Kid; fill?: boolean }) {
  return (
    <ChatTranscript id={kid.id} role="subagent" status={kid.status} model={RUN.model} title={kid.title} taskKey={RUN.taskKey} repo={RUN.repo} branch={RUN.branch}
      startedAt={kid.startedAt} endedAt={kid.endedAt} costUsd={kid.costUsd} live={kid.status === "running"} fill={fill}
      subtitle={<>{kid.kind} · subagent of the implementer · spawned by <code>spawn_agent</code> {kid.callId}</>}>
      <ChatMessage role="implementer" model={RUN.model} intent="prompt" name="implementer" content={`Research only, do not modify files. ${kid.title}: determine exactly how the UI tests create their Playwright contexts, where record_video / screenshot options are set, and which conftest hooks run.`} startedAt={kid.startedAt - 2_000} />
      <KidTurns kid={kid} />
    </ChatTranscript>
  );
}

/** Open as: a panel in the rail's place, wider, parent and child scrolling apart. */
function ChildPanel({ kid, onClose }: { kid: Kid; onClose: () => void }) {
  return (
    <aside className="runRail" aria-label="Subagent" style={{ width: 560, display: "flex", flexDirection: "column", minHeight: 0, borderLeft: "1px solid var(--ds-color-border-subtle)" }} data-testid="child-panel">
      <div style={{ display: "flex", alignItems: "center", gap: 8, padding: "8px 12px", borderBottom: "1px solid var(--ds-color-border-subtle)" }}>
        <span className="ds-label" style={{ marginRight: "auto" }}>Subagent</span>
        <Button size="sm" variant="quiet" trailingIcon="arrow-right">Open page</Button>
        <IconButton size="sm" variant="quiet" icon="close" label="Close" onClick={onClose} />
      </div>
      <div style={{ flex: 1, minHeight: 0, display: "flex" }}>
        <KidTranscript kid={kid} fill />
      </div>
    </aside>
  );
}

/** Open as: its own page. */
function ChildPage({ kid, onBack }: { kid: Kid; onBack: () => void }) {
  return (
    <div style={{ display: "flex", flexDirection: "column", flex: 1, minHeight: 0 }}>
      <div style={{ display: "flex", alignItems: "center", gap: 8, padding: "8px 16px", borderBottom: "1px solid var(--ds-color-border-subtle)", fontSize: 13, color: "var(--ds-color-text-secondary)" }}>
        <Button size="sm" variant="quiet" leadingIcon="chevron-left" onClick={onBack}>Implementer</Button> <span>›</span> <span>Subagent · {kid.title}</span>
      </div>
      <div style={{ flex: 1, minHeight: 0, display: "flex" }}>
        <KidTranscript kid={kid} fill />
      </div>
    </div>
  );
}

function Transcript({ place, onOpen, opened }: { place: Place; onOpen: (id: string) => void; opened: string | null }) {
  return (
    <ChatTranscript id={RUN.id} role="implementer" status="running" model={RUN.model} title={RUN.title} taskKey={RUN.taskKey} repo={RUN.repo} branch={RUN.branch}
      startedAt={RUN.startedAt} costUsd={2.14} owner={P} live fill subtitle={<>implement · round 3 · Codex</>}>
      <ChatMessage role="implementer" model={RUN.model} content="Two things to find out before I touch the fixture: how contexts are created, and where the dataset comes from. Spawning one agent for each and carrying on with the runbook meanwhile." startedAt={ago(19.5)} endedAt={ago(19.1)} costUsd={0.07} />
      <ChatMessage role="implementer" model={RUN.model} startedAt={ago(19)} costUsd={null}
        attachments={
          <>
            {KIDS.map((k) => (
              <div key={k.id}>
                <ToolCallCard name="spawn_agent" status="completed" args={{ agent_path: `/root/${k.callId}`, prompt: `Research only. ${k.title}.`, model: RUN.model }} startedAt={k.startedAt - 1_000} durationMs={600} summary={`${k.title} · ${k.kind}`} />
                {place === "inline" ? <KidInline kid={k} /> : <KidRow kid={k} onOpen={onOpen} opened={opened} />}
              </div>
            ))}
          </>
        } />
      {PARENT_AFTER.map((t, i) => t.kind === "tool"
        ? <ChatMessage key={i} role="implementer" model={RUN.model} startedAt={t.at} costUsd={null} activity={t.tool === "wait" ? "tool" : undefined} activityProps={t.tool === "wait" ? { tool: "wait", since: t.at } : undefined}
            attachments={<ToolCallCard name={t.tool} status={t.tool === "wait" ? "running" : "completed"} args={t.args} startedAt={t.at} durationMs={t.ms || undefined} result={t.result} summary={t.tool === "wait" ? "waiting for 1 of 2 agents" : undefined} />} />
        : <ChatMessage key={i} role="implementer" model={RUN.model} content={t.text} startedAt={t.at} endedAt={t.at + 8_000} costUsd={t.cost} />)}
    </ChatTranscript>
  );
}

function Dur({ since }: { since: number }) {
  const mins = Math.max(1, Math.round((NOW - since) / 60_000));
  const h = Math.floor(mins / 60), m = mins % 60;
  return <span style={{ fontVariantNumeric: "tabular-nums" }}>{h > 0 ? `${h}h ${m}m` : `${m}m`}</span>;
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <ThemeProvider defaultPreference="dark" storageKey={null} densityStorageKey={null}>
      <TooltipProvider>
        <ToastProvider>
          <Mock />
        </ToastProvider>
      </TooltipProvider>
    </ThemeProvider>
  </StrictMode>,
);
