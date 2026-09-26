import { useContext, useState, type ReactNode } from "react";
import { Block, PaneDensityContext, Section, densitiesFor, type PaneMode } from "../Frame.tsx";
import styles from "../gallery.module.css";
import { AgentPlan, type PlanItem } from "../../components/AgentPlan.tsx";
import { Breadcrumb } from "../../components/Breadcrumb.tsx";
import { ChatComposer } from "../../components/ChatComposer.tsx";
import { ChatAside as Aside, ChatMessage } from "../../components/ChatMessage.tsx";
import { ChatTranscript } from "../../components/ChatTranscript.tsx";
import { QuestionCard } from "../../components/QuestionCard.tsx";
import { Sidebar } from "../../components/Sidebar.tsx";
import { ThinkingBlock } from "../../components/ThinkingBlock.tsx";
import { ToolCallCard } from "../../components/ToolCallCard.tsx";
import { Button, IconButton } from "../../primitives/Button.tsx";
import { Tab, TabList, TabPanel, Tabs } from "../../primitives/Tabs.tsx";
import type { ThemeMode } from "../../tokens/themes.ts";
import type { NavRef } from "../../util/navModel.ts";
import { navProjects } from "../navFixtures.ts";
import {
  CLIENT_DIFF,
  CLIENT_SOURCE,
  CONTEXT_WINDOW,
  LONG_TEST_OUTPUT_FAILED,
  LONG_TEST_OUTPUT_PASSED,
  MD_SHORT,
  MD_SUMMARY,
  MSG_1,
  MSG_2,
  MSG_3,
  QUESTION_OPTIONS,
  QUESTION_TEXT,
  THOUGHT_1,
  THOUGHT_2,
} from "../realisticTranscript.ts";

const SEC = 1_000;
const MIN = 60 * SEC;

const PLAN: PlanItem[] = [
  { content: "Map GithubClient.post and existing retry patterns", status: "completed", priority: "high" },
  { content: "Add exponential backoff (max 5 attempts) to GithubClient.post", status: "completed", priority: "high" },
  { content: "Run bun test and fix failures", status: "completed", priority: "medium" },
  { content: "Decide on the route's own retry before opening the PR", status: "in_progress", priority: "medium" },
  { content: "Open PR and hand off to reviewer", status: "pending", priority: "medium" },
];

/**
 * The app as it is used all day: the sidebar beside a session transcript,
 * laid out as `apps/web` lays out `.shell` (sidebar, then a padded main pane
 * holding breadcrumb, tabs and the transcript). One full-width frame per
 * theme and density so spacing is judged at real width.
 */
export function ShellSection({ mode }: { readonly mode: PaneMode }) {
  const paneDensity = useContext(PaneDensityContext);
  const themes: ThemeMode[] = mode === "both" ? ["dark", "light"] : [mode];
  const densities = densitiesFor(paneDensity);
  return (
    <Section
      id="shell"
      title="App shell"
      intro="The screen an operator has open all day: the sidebar with something waiting on them, beside the transcript of the session that is asking. Judge density, shade steps and hairlines here, at full width, before anywhere else."
    >
      <Block id="shell-session" title="Sidebar + session transcript" note="Sidebar selection is the orchestrator session of WI-2401, which is blocked on a question; the transcript shows its pinned plan, a run of turns from one author, a steer, reasoning, tool calls (one failed), the waiting question and the answer composer.">
        <div style={{ display: "flex", flexDirection: "column", gap: 16 }}>
          {densities.flatMap((d) =>
            themes.map((t) => (
              <div key={`${d}-${t}`} data-shot={`shell-${t}-${d}`} data-theme={t} data-density={d} style={{ colorScheme: t }} className={styles["shellFrame"]}>
                <span className={styles["paneLabel"]}>{`${t} · ${d}`}</span>
                <AppShell />
              </div>
            )),
          )}
        </div>
      </Block>
    </Section>
  );
}

function AppShell() {
  const [selected, setSelected] = useState<NavRef | null>({ kind: "session", id: "s_2401-orc" });
  return (
    <div className={styles["shell"]}>
      <Sidebar projects={navProjects} selected={selected} onSelect={setSelected} title="dude" />
      <main className={styles["shellMain"]}>
        <Breadcrumb
          items={[
            { id: "p", label: "control-plane", onSelect: () => undefined },
            { id: "e", label: "Webhook reliability", icon: "layers", onSelect: () => undefined },
            { id: "w", label: "WI-2401", mono: true, onSelect: () => undefined },
            { id: "s", label: "Orchestrator" },
          ]}
        />
        <Tabs defaultValue="chat" fill>
          <TabList>
            <Tab value="chat">Conversation</Tab>
            <Tab value="events" count={214}>
              Events
            </Tab>
          </TabList>
          <TabPanel value="chat" fill>
            <Transcript />
          </TabPanel>
        </Tabs>
      </main>
    </div>
  );
}

function Transcript() {
  const [t0] = useState(() => Date.now() - 14 * MIN);
  const ts = (offset: number) => new Date(t0 + offset).toISOString();
  return (
    <ChatTranscript
      fill
      live
      session={{
        id: "s_2401-orc",
        role: "orchestrator",
        status: "awaiting_input",
        model: "claude-opus-4",
        taskId: "WI-2401",
        title: "Add retry with backoff to the GitHub webhook handler",
        repo: "dude/control-plane",
        branch: "wi-2401-webhook-retry",
        startedAt: ts(0),
        costUsd: 1.84,
        budgetUsd: 5,
        tokens: 412_000,
      }}
      headerActions={
        <>
          <Button size="sm" variant="secondary">
            Pause
          </Button>
          <Button size="sm" variant="destructive-outline">
            Abort
          </Button>
        </>
      }
      pinned={<AgentPlan items={PLAN} defaultCollapsed sticky />}
      footer={
        <ChatComposer
          mode="answer"
          question={{ id: "q_2401", askedBy: "Orchestrator", askedAt: ts(12 * MIN), text: "Should I leave the route's retry in place, or fold it into this change?", options: QUESTION_OPTIONS }}
          onSubmit={() => undefined}
        />
      }
    >
      <ChatMessage role="system" content="Today" />
      <ChatMessage role="system" content="Session started on worker-03 · claude-opus-4" startedAt={ts(0)} />
      <Aside>
        <ThinkingBlock text={THOUGHT_1} startedAt={ts(1 * SEC)} endedAt={ts(5 * SEC)} />
      </Aside>
      <ChatMessage role="orchestrator" model="claude-opus-4" content={MSG_1} startedAt={ts(5 * SEC)} endedAt={ts(7 * SEC)} costUsd={0.012} contextTokens={18_300} contextWindowTokens={CONTEXT_WINDOW} outputTokens={60} />
      <Aside>
        <ToolCallCard name="read" status="completed" args={{ file_path: "apps/control-plane/src/integrations/github/client.ts" }} startedAt={ts(7_100)} endedAt={ts(7_141)} output={CLIENT_SOURCE} />
        <ToolCallCard name="edit" status="completed" args={{ file_path: "apps/control-plane/src/integrations/github/client.ts" }} startedAt={ts(10_200)} endedAt={ts(10_212)} diff={CLIENT_DIFF} />
      </Aside>
      <ChatMessage role="orchestrator" continued content={MSG_2} startedAt={ts(10_400)} endedAt={ts(14_000)} costUsd={0.031} contextTokens={31_200} contextWindowTokens={CONTEXT_WINDOW} outputTokens={410} />
      <ChatMessage role="orchestrator" continued content={MD_SHORT} startedAt={ts(14_020)} />
      <Aside>
        <ToolCallCard name="bash" status="failed" args={{ command: "bun test apps/control-plane" }} startedAt={ts(14_100)} endedAt={ts(18_310)} exitCode={1} error="1 failing: retries when response is 502" output={LONG_TEST_OUTPUT_FAILED} maxResultLines={8} />
        <ThinkingBlock text={THOUGHT_2} startedAt={ts(18_400)} endedAt={ts(29_800)} />
      </Aside>
      <ChatMessage role="human" name="marcio" intent="steer" content="Keep the jitter, but make it injectable so the tests can seed it." startedAt={ts(20 * SEC)} deliveredAt={ts(29_800)} />
      <ChatMessage
        role="orchestrator"
        model="claude-opus-4"
        content={MSG_3}
        startedAt={ts(29_800)}
        endedAt={ts(33 * SEC)}
        costUsd={0.024}
        contextTokens={52_800}
        contextWindowTokens={CONTEXT_WINDOW}
        outputTokens={380}
        actions={<IconButton icon="copy" label="Copy message" size="sm" onClick={() => void navigator.clipboard?.writeText(MSG_3)} />}
      />
      <Aside>
        <ToolCallCard name="bash" status="completed" args={{ command: "bun run typecheck && bun test apps/control-plane" }} startedAt={ts(38_200)} endedAt={ts(42_080)} exitCode={0} output={LONG_TEST_OUTPUT_PASSED} />
      </Aside>
      <ChatMessage role="orchestrator" continued content="All green: 65 pass, 0 fail. The diff is 118 lines." startedAt={ts(45_600)} />
      <ChatMessage data-shot-anchor="markdown" role="orchestrator" continued content={MD_SUMMARY} startedAt={ts(47 * SEC)} endedAt={ts(58 * SEC)} costUsd={0.021} contextTokens={63_900} contextWindowTokens={CONTEXT_WINDOW} outputTokens={520} />
      <QuestionCard role="orchestrator" text={QUESTION_TEXT} options={QUESTION_OPTIONS} askedAt={ts(12 * MIN)} />
    </ChatTranscript>
  );
}
