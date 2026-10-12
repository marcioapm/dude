/*
 * Where members see what an agent may do: a brainstorm session's header
 * and its rail, and a Run's header. The session and Run are the app's as
 * they are today (the welcome mockup's session, RunScreen's header); what
 * is new is the CapabilityChip and the rail's lines, read from the values
 * the agent was started with.
 */

import { useRef } from "react";
import {
  Capabilities,
  ChatComposer,
  ChatMessage,
  ChatTranscript,
  CostDisplay,
  LinkedProjects,
  MachineChip,
  ScreenHeader,
  SessionHeader,
  SessionPeople,
  SessionRail,
  SessionRailBlock,
  SessionFacts,
  SessionTitle,
  TierChip,
} from "@dude/design-system/components";
import { Button } from "@dude/design-system/primitives";
import { CapabilityChip } from "./components.tsx";
import { ACT_LABEL, HAS, INTERNET_LABEL, ORG_CAPS, P, PROJECT_OVERRIDES, RUN, SESSION, SETUP, resolve, type Role } from "./data.ts";
import styles from "./capabilities.module.css";

/** The chip's few words, and the full list for its tooltip and the rail. */
function words(role: Role, project: "control-plane" | "billing" | null) {
  const c = project ? resolve(role, PROJECT_OVERRIDES[project]) : resolve(role, null);
  const containersPossible = SETUP[role].imageCanRunContainers;
  const parts = [
    INTERNET_LABEL[c.internet.value],
    c.webSearch.value ? "Web search" : "No web search",
    c.containers.value && containersPossible ? "Containers" : "No containers",
  ];
  const acts = HAS[role].map((a) => ({ a, on: c.acts[a]!.value }));
  const label = (a: (typeof acts)[number]["a"]) => (role === "brainstorm" && a === "create" ? "Propose work" : ACT_LABEL[a]);
  const can = [
    c.internet.value === "any" ? "Reach any host on the internet" : c.internet.value === "lists" ? "Reach Acme’s hosts and the project’s" : "Reach only its model and dude",
    ...(c.webSearch.value ? ["Search the web"] : []),
    ...(c.containers.value && containersPossible ? ["Start containers"] : []),
    ...acts.filter((x) => x.on).map((x) => label(x.a)),
  ];
  const cannot = [
    ...(c.webSearch.value ? [] : ["Search the web"]),
    ...(c.containers.value && containersPossible ? [] : ["Start containers"]),
    ...acts.filter((x) => !x.on).map((x) => label(x.a)),
  ];
  return { parts, can, cannot };
}

function Tip({ can, cannot, note }: { readonly can: readonly string[]; readonly cannot: readonly string[]; readonly note: string }) {
  return (
    <span style={{ display: "flex", flexDirection: "column", gap: "var(--ds-space-6)" }}>
      <Capabilities can={can} cannot={cannot} />
      <span>{note}</span>
    </span>
  );
}

export function SessionScreen() {
  const at = useRef(Date.now()).current;
  const w = words("brainstorm", null);
  return (
    <div className="screen sessionScreen" data-testid="session-screen">
      <ScreenHeader fillTitle title={<SessionTitle title={SESSION.title} onRename={async () => undefined} />}
        meta={<span className={styles["meta"]}><span>Brainstorm · claude-opus-5-5</span>
          <CapabilityChip parts={w.parts} data-testid="session-capabilities"
            tooltip={<Tip can={w.can} cannot={w.cannot} note="Acme’s Brainstorm capabilities when this agent started; a change applies from the next agent." />} /></span>} />
      <div className="runScreen" data-view="chat">
        <div className="runView">
          <div className="runChat">
            <ChatTranscript fill revision={2} turns={2}
              footer={<ChatComposer mode="chat" placeholder="Message the brainstorm…" onSubmit={() => true} to={<>To <b>Brainstorm</b></>} />}>
              <ChatMessage role="human" person={P["marcio"]} name="Márcio Martins" intent="message" startedAt={at - 3_600_000}
                content="Compare Stripe Billing, Orb and Metronome for usage-based billing. We meter API calls per organisation; what would each need from billing?" />
              <ChatMessage role="brainstorm" startedAt={at - 3_540_000} costUsd={0.21}
                content={"I read the three providers’ docs and `billing/meter/` in the linked repository.\n\n- **Stripe Billing** takes meter events per customer; our `UsageEvent` maps to it almost as is.\n- **Orb** wants an idempotency key per event; `meter/flush.go` would need to keep one.\n- **Metronome** prices in its own contracts; our plan table would move there.\n\nShall I propose an epic for Stripe first, behind a flag?"} />
            </ChatTranscript>
            <SessionRail className="runRail" aria-label="The session">
              <SessionRailBlock label={<span className="sessionRailHead">People<Button size="sm" variant="quiet">Share</Button></span>}>
                <SessionPeople members={[{ person: P["marcio"]!, role: "owner", open: true, you: true }, { person: P["ana"]!, role: "chat" }]} />
              </SessionRailBlock>
              <SessionRailBlock label={<span className="sessionRailHead">Linked<Button size="sm" variant="quiet">Link</Button></span>}>
                <LinkedProjects projects={[{ key: "BL", name: "billing", repositories: [{ name: "acme/billing", defaultBranch: "main" }] }]} />
              </SessionRailBlock>
              <SessionRailBlock label="It can" data-testid="rail-capabilities">
                <Capabilities can={["Read the linked projects’ code, tasks, pull requests and findings", ...w.can]} cannot={["Change code, push, start or steer work", ...w.cannot]} />
                <p className={styles["railNote"]}>Acme’s Brainstorm settings, when this agent started.</p>
              </SessionRailBlock>
              <SessionRailBlock label="Cost"><SessionFacts facts={[{ label: "Session", value: <CostDisplay usd={0.42} /> }]} /></SessionRailBlock>
            </SessionRail>
          </div>
        </div>
      </div>
    </div>
  );
}

export function RunScreen() {
  const at = useRef(Date.now()).current;
  const w = words("implementer", "control-plane");
  const s = SETUP.implementer;
  return (
    <div className="screen sessionScreen" data-testid="run-screen">
      <SessionHeader session={{
        id: "run_cp212_impl",
        role: "implementer",
        status: "running",
        owner: P["ana"],
        title: "Implement",
        taskKey: RUN.key,
        startedAt: at - 754_000,
        costUsd: 0.86,
        tokens: 412_000,
        subtitle: (
          <>
            <span>for Ana</span>
            <TierChip tier={s.tier} model={s.model} effort="high" harness="Claude Code" />
            <MachineChip name={s.machine} spec="8 CPUs · 16 GiB · 80 GiB" />
            <MachineChip icon="cube" name={s.image} spec="v7" />
            <CapabilityChip parts={w.parts} data-testid="run-capabilities"
              tooltip={<Tip can={w.can} cannot={w.cannot} note="What this Run was started with: control-plane’s Implementer capabilities. Resuming keeps them." />} />
          </>
        ),
      }} />
      <div className="runScreen" data-view="chat">
        <div className="runView">
          <div className="runChat">
            <ChatTranscript fill live revision={2} turns={2}
              footer={<ChatComposer mode="steer" onSubmit={() => true} sentAs="Ana" landsHint="Lands at the agent's next step" />}>
              <ChatMessage role="system" intent="prompt" content={`## ${RUN.title}\n\nWebhook deliveries that fail are dropped. Retry them with exponential backoff, up to 6 attempts over 2 hours.`} startedAt={at - 754_000} />
              <ChatMessage role="implementer" activity="tool" startedAt={at - 700_000}
                content={"Searched for how GitHub and Stripe space their webhook retries; both cap near a day. Writing `deliveries/retry.go` with a capped backoff table."} />
            </ChatTranscript>
          </div>
        </div>
      </div>
    </div>
  );
}
