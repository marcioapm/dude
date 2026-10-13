/*
 * Child sessions in a Run's transcript (mockup). Five screens over three
 * harnesses, built from the real design system so the proposal is what the
 * web app would render. The bar above walks the screens; it is not part of
 * the proposal.
 */

import { StrictMode, useState, type CSSProperties } from "react";
import { createRoot } from "react-dom/client";
import "@dude/design-system/tokens.css";
import "@dude/design-system/base.css";
import "../../src/app.css";
import { ThemeProvider, useTheme, type ThemePreference, Icon } from "@dude/design-system";
import {
  AgentPlan, ChatMessage, ChatNotice, ChatThread, ChatTranscript, Segmented, Sidebar, SidebarLink, SidebarProfile, ToolCallCard,
} from "@dude/design-system/components";
import { Badge, Button, Callout, ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { navProjects } from "../../../../packages/design-system/src/gallery/navFixtures.ts";
import {
  BEFORE, CHILD_ID, CHILD_MESSAGES, CHILD_STUCK, CHILD_TOOLS, DONE, NOW, PARALLEL, RUN, STALL, TASK_CALL, WAITING, ago, at,
  type Harness, type Scene,
} from "./data.ts";

const P = { id: "u_marcio", name: "Márcio Martins", online: true };

/** Where the proposal differs per harness, in one line each for the note bar. */
const HARNESS_NOTE: Record<Harness, string> = {
  opencode: "OpenCode: child = a session with parentID; its events come from the HTTP bus (lux relays them). Parent's call arrives as kind `think` and is shown as `task`.",
  claude: "Claude Code: child lines carry parent_tool_use_id; system task_started/progress/notification give start, usage and the end. Already on the stream, today shown as the parent's own.",
  codex: "Codex: child = a thread with parentThreadId; subAgentActivity + collabAgentToolCall (spawn/wait) tie it to the parent's item. Several children can run at once.",
};

function Mock() {
  const theme = useTheme();
  const [scene, setScene] = useState<Scene>("today");
  const [harness, setHarness] = useState<Harness>("opencode");
  const [open, setOpen] = useState(true);
  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100%" }}>
      <div className="mockbar">
        <b>dude · What a subagent is doing (mockup)</b>
        <Segmented size="sm" label="Screen" value={scene} onChange={(s) => { setScene(s); if (s === "parallel") setHarness("codex"); }} options={[
          { value: "today", label: "1 · Today" },
          { value: "live", label: "2 · Child working" },
          { value: "waiting", label: "3 · Child stuck" },
          { value: "done", label: "4 · Child done" },
          { value: "parallel", label: "5 · Two at once" },
        ]} />
        <Segmented size="sm" label="Harness" value={harness} onChange={setHarness} options={[
          { value: "opencode", label: "OpenCode" }, { value: "claude", label: "Claude Code" }, { value: "codex", label: "Codex" },
        ]} />
        <Segmented size="sm" label="Theme" value={theme.resolved} onChange={(t) => theme.setPreference(t as ThemePreference)} options={[{ value: "dark", label: "Dark" }, { value: "light", label: "Light" }]} />
      </div>
      <div className="mocknote">{scene === "today" ? "What the transcript shows now: one open call, nothing inside it, for two hours. " : ""}{HARNESS_NOTE[harness]}</div>
      <div className="shell" style={{ flex: 1, minHeight: 0 }}>
        <Sidebar projects={navProjects} title="dude" footer={<><SidebarLink icon="building">Organisation settings</SidebarLink><SidebarProfile person={P} detail="marcio@acme.dev" onOpen={() => undefined} /></>} />
        <main className="main flush" style={{ display: "flex", flexDirection: "column", minHeight: 0 }}>
          {scene === "waiting" || scene === "today" ? <StalledBanner today={scene === "today"} /> : null}
          <Transcript scene={scene} harness={harness} open={open} onOpen={setOpen} />
        </main>
      </div>
    </div>
  );
}

function StalledBanner({ today }: { readonly today: boolean }) {
  return (
    <div style={{ padding: "12px 16px 0" }}>
      <Callout tone="attention">
        <div className="escalation">
          <p>
            <strong>Your fix Run has made no progress.</strong> {today ? STALL.today : STALL.after}{" "}
            <Button size="sm" variant="quiet" trailingIcon="arrow-right">Open fix Run</Button>
          </p>
          <div className="escalationActions">
            <Button size="sm" variant="primary">Restart</Button>
            <Button size="sm" variant="secondary">Leave it</Button>
            <Button size="sm" variant="danger">Stop</Button>
          </div>
        </div>
      </Callout>
    </div>
  );
}

function Transcript({ scene, harness, open, onOpen }: { readonly scene: Scene; readonly harness: Harness; readonly open: boolean; readonly onOpen: (o: boolean) => void }) {
  const call = TASK_CALL[harness];
  const live = scene === "live" || scene === "waiting" || scene === "today" || scene === "parallel";
  const endedAt = scene === "done" ? DONE.endedAt : null;
  const costUsd = scene === "today" ? 0.728 : scene === "done" ? 0.728 : 0.47;
  return (
    <ChatTranscript
      id={RUN.id} role="implementer" status={live ? "running" : "completed"} model={RUN.model} title={RUN.title}
      taskKey={RUN.taskKey} repo={RUN.repo} branch={RUN.branch} startedAt={RUN.startedAt} endedAt={endedAt}
      costUsd={costUsd} owner={P} live={live} fill
      subtitle={<>fix · round 3 · {harness === "opencode" ? "OpenCode" : harness === "claude" ? "Claude Code" : "Codex"}</>}
      pinned={<AgentPlan sticky items={BEFORE.plan} />}
    >
      <ChatMessage role="human" person={P} name="Márcio Martins" intent="steer" content={BEFORE.steer.text} startedAt={BEFORE.steer.at} deliveredAt={BEFORE.steer.at + 26_000} read />
      <ChatMessage role="implementer" model={RUN.model} content={BEFORE.message} startedAt={at("2026-10-13T01:43:54Z")} endedAt={at("2026-10-13T01:44:06Z")} costUsd={0.068} />
      {scene === "today" ? <Today /> : null}
      {scene !== "today" && scene !== "parallel" ? (
        <ChatMessage role="implementer" model={RUN.model} startedAt={at("2026-10-13T01:44:06Z")} endedAt={endedAt} costUsd={null}
          activity={live ? "tool" : undefined} activityProps={live ? { tool: call.tool, since: at("2026-10-13T01:44:06Z") } : undefined}
          attachments={
            <>
              <ToolCallCard name={call.tool} status={scene === "done" ? "completed" : "running"} args={call.input} startedAt={at("2026-10-13T01:44:06Z")} endedAt={endedAt}
                summary={<>{call.title} <span style={{ color: "var(--ds-color-text-muted)" }}>· {call.kind}</span></>}
                result={scene === "done" ? DONE.summary : undefined} />
              <Child scene={scene} harness={harness} open={open} onOpen={onOpen} />
            </>
          } />
      ) : null}
      {scene === "parallel" ? <Parallel /> : null}
      {scene === "done" ? (
        <ChatMessage role="implementer" model={RUN.model} startedAt={at("2026-10-13T01:46:41Z")} endedAt={at("2026-10-13T01:46:58Z")} costUsd={0.11}
          content="The captures are 1280×720 because nothing sets a viewport. Adding `viewport` and `record_video_size` 1920×1080 to the `ui_previewer` fixture, then re-running the chart capture." />
      ) : null}
    </ChatTranscript>
  );
}

/** Screen 1: what the operator saw on 2026-10-13 — `think`, open, empty. */
function Today() {
  return (
    <ChatMessage role="implementer" model={RUN.model} startedAt={at("2026-10-13T01:44:06Z")} costUsd={null} activity="tool" activityProps={{ tool: "think", since: at("2026-10-13T01:44:06Z") }}
      attachments={<ToolCallCard name="think" status="running" args={TASK_CALL.opencode.input} startedAt={at("2026-10-13T01:44:06Z")} summary="Investigate UI artifact capture" slowAfterMs={60_000} />} />
  );
}

/** Screens 2–4: the child nested under the parent's call. */
function Child({ scene, harness, open, onOpen }: { readonly scene: Scene; readonly harness: Harness; readonly open: boolean; readonly onOpen: (o: boolean) => void }) {
  const call = TASK_CALL[harness];
  const stuck = scene === "waiting";
  const done = scene === "done";
  const status = done ? "completed" : "running";
  const childModel = harness === "codex" ? "gpt-5.6-sol" : RUN.model;
  const header = stuck
    ? <WaitingLine />
    : done ? DONE.summary.split(";")[0] + "…" : "reading the installed pytest-playwright plugin";
  return (
    <ChatThread
      sessionId={CHILD_ID[harness]} role="investigator" status={status} model={childModel} task={`${call.title} · ${call.kind}`}
      startedAt={at("2026-10-13T01:44:06Z")} endedAt={done ? DONE.endedAt : null} costUsd={done ? DONE.costUsd : 0.37}
      turnCount={done ? 9 : undefined} summary={header}
      activity={stuck ? "tool" : done ? "completed" : "tool"}
      activityProps={stuck ? { since: WAITING.since, label: "waiting on a permission", tool: "grep" } : done ? undefined : { tool: "read", since: ago(0.2) }}
      collapsed={!open} onCollapsedChange={(c) => onOpen(!c)} onOpen={() => undefined}
      data-waiting={stuck ? "permission" : undefined}
      style={stuck ? ({ "--rail": "var(--ds-color-attention-mark)", "--rail-bg": "var(--ds-color-attention-bg)" } as CSSProperties) : undefined}
    >
      {!stuck ? CHILD_TOOLS.slice(0, 3).map((t) => <ToolCallCard key={t.id} name={t.tool} status="completed" args={t.args} startedAt={t.at} durationMs={t.ms} result={t.result} />) : null}
      {!stuck ? <ChatMessage role="investigator" model={childModel} content={CHILD_MESSAGES[0]!.text} startedAt={CHILD_MESSAGES[0]!.at} endedAt={CHILD_MESSAGES[0]!.at + 9_000} costUsd={0.04} /> : null}
      {stuck ? <ChatNotice kind="notice" at={CHILD_TOOLS[0]!.at} text="7 earlier calls and 1 message folded" /> : null}
      {CHILD_TOOLS.slice(stuck ? 6 : 3).map((t) => <ToolCallCard key={t.id} name={t.tool} status="completed" args={t.args} startedAt={t.at} durationMs={t.ms} result={t.result} />)}
      <ChatMessage role="investigator" model={childModel} content={CHILD_MESSAGES[1]!.text} startedAt={CHILD_MESSAGES[1]!.at} endedAt={CHILD_MESSAGES[1]!.at + 7_000} costUsd={0.03} />
      {stuck ? (
        <>
          {CHILD_STUCK.map((t) => (
            <ToolCallCard key={t.id} name={t.tool} status="running" args={t.args} startedAt={t.at} slowAfterMs={30_000}
              note={t.id === "c8" ? <WaitingNote /> : undefined} defaultExpanded={t.id === "c8"} />
          ))}
          <ChatNotice kind="notice" at={WAITING.since} text={`Waiting on a permission: ${WAITING.permission} for ${WAITING.pattern}. Nothing in this Run can answer it.`} />
        </>
      ) : null}
      {scene === "live" ? (
        <ToolCallCard name="read" status="running" args={{ filePath: "/opt/ui-tests/lib/python3.14/site-packages/pytest_playwright/pytest_playwright.py" }} startedAt={ago(0.2)} />
      ) : null}
      {done ? (
        <ChatMessage role="investigator" model={childModel} content={DONE.summary} startedAt={at("2026-10-13T01:46:20Z")} endedAt={DONE.endedAt} costUsd={0.06} activity="completed" />
      ) : null}
    </ChatThread>
  );
}

function WaitingLine() {
  return (
    <span style={{ display: "inline-flex", alignItems: "center", gap: 6, color: "var(--ds-color-attention-fg)" }}>
      <Icon name="pause" size={14} /> waiting <Duration since={WAITING.since} /> on a permission nothing can answer
    </span>
  );
}

function WaitingNote() {
  return (
    <span>
      <Badge tone="attention" size="sm" icon="pause">waiting</Badge>{" "}
      asked for <code>{WAITING.permission}</code> on <code>{WAITING.pattern}</code> at 01:45:09 — the harness never forwarded it, so no one can allow or refuse it.
    </span>
  );
}

function Duration({ since }: { readonly since: number }) {
  const mins = Math.max(1, Math.round((NOW - since) / 60_000));
  const h = Math.floor(mins / 60), m = mins % 60;
  return <span style={{ fontVariantNumeric: "tabular-nums" }}>{h > 0 ? `${h}h ${m}m` : `${m}m`}</span>;
}

/** Screen 5: Codex, two children of one turn, each its own thread. */
function Parallel() {
  return (
    <ChatMessage role="implementer" model="gpt-5.6-sol" startedAt={ago(4.2)} costUsd={null} activity="tool" activityProps={{ tool: "wait", since: ago(0.4) }}
      content="Two things to find out before I touch the fixture: how contexts are created, and where the dataset comes from. Spawning one agent for each."
      attachments={
        <>
          <ToolCallCard name="spawn_agent" status="completed" args={TASK_CALL.codex.input} startedAt={ago(4.1)} durationMs={600} summary="Inspect test capture setup · explorer" />
          <ToolCallCard name="spawn_agent" status="completed" args={{ agent_path: "/root/find_fixture", prompt: "Find where the test dataset (exposures.psv.gz) comes from in CI.", model: "gpt-5.6-sol" }} startedAt={ago(3.9)} durationMs={500} summary="Find the chart fixture dataset · explorer" />
          {PARALLEL.map((c) => (
            <ChatThread key={c.id} sessionId={c.id} role="investigator" status={c.status} model="gpt-5.6-sol" task={`${c.title} · explorer`} startedAt={c.startedAt} endedAt={c.status === "completed" ? ago(0.8) : null} costUsd={c.costUsd}
              summary={c.status === "completed" ? c.summary : "searching tests/ for viewport settings"} activity={c.status === "completed" ? "completed" : "tool"}
              activityProps={c.status === "completed" ? undefined : { tool: "shell", since: c.tool!.since }} defaultCollapsed={c.status === "completed"} onOpen={() => undefined} turnCount={c.status === "completed" ? 4 : undefined}>
              {c.status === "running" ? (
                <>
                  <ToolCallCard name="shell" status="completed" args={{ command: "find tests -name conftest.py" }} startedAt={ago(3.5)} durationMs={320} result="tests/conftest.py\ntests/suites/ui_previewer/conftest.py" />
                  <ChatMessage role="investigator" model="gpt-5.6-sol" content="Two conftests; the previewer one sets record_video_dir. Looking for a viewport." startedAt={ago(2.5)} endedAt={ago(2.3)} costUsd={0.08} />
                  <ToolCallCard name="shell" status="running" args={c.tool!.args} startedAt={c.tool!.since} />
                </>
              ) : (
                <ChatMessage role="investigator" model="gpt-5.6-sol" content={c.summary!} startedAt={ago(1)} endedAt={ago(0.8)} costUsd={0.09} activity="completed" />
              )}
            </ChatThread>
          ))}
          <ToolCallCard name="wait" status="running" args={{ agents: PARALLEL.map((c) => c.id) }} startedAt={ago(0.4)} summary="waiting for 1 of 2 agents" />
        </>
      } />
  );
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
