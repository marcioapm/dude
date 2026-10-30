import type { ReactNode } from "react";
import { Block, Panes, Section, type PaneMode } from "../Frame.tsx";
import { AgentAvatar } from "../../components/AgentAvatar.tsx";
import { Cost } from "../../components/Cost.tsx";
import { Disclosure } from "../../components/Disclosure.tsx";
import { Duration } from "../../components/Numbers.tsx";
import { MetricGroup, MetricTile } from "../../components/MetricTile.tsx";
import { RunningList, RunningRow } from "../../components/RunningRow.tsx";
import { StartChoice } from "../../components/StartChoice.tsx";
import { StatusMark } from "../../components/StatusMark.tsx";
import { Button } from "../../primitives/Button.tsx";
import { Callout } from "../../primitives/Layout.tsx";

/** Sample Acme figures: lux still settling the latest, so each is an estimate. */
const settling = (tokensUsd: number | null, machineUsd: number | null, size: "md" | "lg" = "md") =>
  <Cost tokensUsd={tokensUsd} machineUsd={machineUsd} tokensFrom="lux" machineFrom="lux" settled={false} size={size} />;
const MIN = 60_000;
const since = (min: number) => new Date(Date.now() - min * MIN).toISOString();

const open = (label: string) => <Button size="sm" variant="quiet" trailingIcon="arrow-right" aria-label={`Open ${label}'s session`}>Open session</Button>;

function Reviewers() {
  return (
    <RunningList aria-label="Running now">
      <RunningRow avatar={<AgentAvatar role="reviewer" size="lg" live />} label="Quality review"
        detail={<>claude-opus-4-1 · <StatusMark status="running" size="sm" /></>}
        activity="Checking retry ordering against concurrent deliveries · step 3 of 5"
        cost={settling(1.14, 0.08)} elapsed={<Duration since={since(6)} live tone="muted" />} action={open("Quality review")} />
      <RunningRow avatar={<AgentAvatar role="reviewer" size="lg" live />} label="Security review"
        detail={<>gpt-5 · <StatusMark status="running" size="sm" /></>}
        activity="bash · rg -n verifySignature src/webhooks"
        cost={settling(0.69, 0.06)} elapsed={<Duration since={since(5)} live tone="muted" />} action={open("Security review")} />
    </RunningList>
  );
}

type State = "active" | "needs-you" | "ready" | "not-started" | "ended";

/** The figures as the Overview draws them: every attempt's spend, the attempt's, lead time, agents, unresolved. */
function Figures({ state }: { readonly state: State }) {
  const idle = state === "not-started";
  const ended = state === "ended";
  return (
    <MetricGroup joined fill>
      <MetricTile label="Task spend" value={idle ? <span>Not reported</span> : settling(5.31, 0.54, "lg")} sub={idle ? "no runs yet" : "all attempts · model + machine"} />
      {idle ? null : <MetricTile label={ended ? "Attempt 1 spend" : "Attempt 2 spend"} value={ended ? settling(1.12, 0.18, "lg") : settling(4.19, 0.36, "lg")} sub={ended ? "set aside" : "current attempt"} />}
      <MetricTile label="Lead time" {...(idle ? { value: "Not started" } : { value: ended ? 15 * MIN : 46 * MIN, unit: "ms" as const, sub: ended ? "start to set aside" : "so far" })} />
      <MetricTile label="Active agents" value={state === "active" ? 2 : 0} unit="count" sub={state === "active" ? "attempt 2" : "none at work"} />
      <MetricTile label="Unresolved" value={state === "active" || state === "needs-you" ? 2 : 0} unit="count"
        sub={state === "active" || state === "needs-you" ? "1 open finding · 1 feedback holding" : "nothing open"} />
    </MetricGroup>
  );
}

/** The two ways to start, as the app offers them in Running now's place: StartChoice's equal cards, no default. */
function Start() {
  return (
    <StartChoice options={[
      { id: "talk", icon: "message", title: "Talk it through",
        description: "The conductor reads the task and the code, asks what it needs, proposes a plan, and starts the agents when you agree.",
        points: ["Nothing is built until you agree", "It asks you before the pull request"], foot: "The conductor, in Chat",
        action: <Button size="sm">Talk it through</Button> },
      { id: "deliver", icon: "zap", title: "Deliver",
        description: "The automatic pipeline: an implementer, reviewers, a fixer if they find problems, a simplifier, then a pull request. Nobody needs to be here.",
        points: ["Stops only on a question or an escalation", "You can write in Chat later and take over"], foot: "The project's pipeline",
        action: <Button size="sm">Deliver</Button> },
    ]} />
  );
}

function Running({ state }: { readonly state: State }) {
  const words: Record<Exclude<State, "active" | "not-started">, ReactNode> = {
    "needs-you": "Waiting on Sam Rivera to answer.",
    ready: <><StatusMark status="ready_to_merge" size="sm" /> Ready to merge: nothing is running.</>,
    ended: "Attempt 1 · set aside: nothing in it runs again, and its records are only to read.",
  };
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: "var(--ds-space-12)", padding: "var(--ds-space-16)", background: "var(--ds-color-raised)" }}>
      {/* As the app's region head: the title, and the count right-aligned and muted. */}
      <div style={{ display: "flex", alignItems: "baseline", justifyContent: "space-between", gap: "var(--ds-space-12)" }}>
        <strong>Running now</strong>
        <span style={{ color: "var(--ds-color-text-muted)" }}>{state === "active" ? "2 agents · attempt 2" : "No agent at work"}</span>
      </div>
      {state === "active" ? <Reviewers /> : state === "not-started" ? <Start /> : <span style={{ color: "var(--ds-color-text-muted)" }}>{words[state]}</span>}
    </div>
  );
}

/** One Overview state, composed of the parts the app composes. */
function OverviewState({ state }: { readonly state: State }) {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: "var(--ds-space-12)", minWidth: 0 }}>
      {state === "needs-you" ? (
        // The app's needs-you region: the question quoted, who can answer, and the way to where it is answered.
        <Callout tone="attention" fill>
          <div style={{ display: "flex", flexWrap: "wrap", alignItems: "center", gap: "var(--ds-space-12) var(--ds-space-20)" }}>
            <div style={{ flex: "1 1 280px", minWidth: 0 }}>
              <strong>Needs Sam Rivera · Conductor asks</strong>
              <div>Should retries stop after 5 attempts or after 24 hours?</div>
            </div>
            <Button variant="primary" size="sm" trailingIcon="arrow-right">Answer in Chat</Button>
          </div>
        </Callout>
      ) : null}
      <div style={{ padding: "var(--ds-space-12) var(--ds-space-16)", background: "var(--ds-color-chrome)" }}><Figures state={state} /></div>
      <Running state={state} />
    </div>
  );
}

export function OverviewSection({ mode }: { readonly mode: PaneMode }) {
  return (
    <Section id="overview" title="Task overview" intro="What a task opens on: the figures, who needs a person, the agents at work, the details folded under them. Sample Acme data; every figure in the app is read from what dude recorded.">
      <Block id="ov-running" title="RunningRow · two reviewers at work" note="Face with the live dot, label, model and status, the recorded plan step or latest tool call, cost (an estimate while lux settles), elapsed, the way to its session. Figures wrap under the words on a narrow pane.">
        <Panes mode={mode}>{() => <Reviewers />}</Panes>
      </Block>
      <Block id="ov-disclosure" title="Disclosure · closed, open" note="A real details on the chrome shade: chevron, title, a muted tabular fact. Keyboard: Enter or Space on the summary; announced expanded or collapsed.">
        <Panes mode={mode}>{() => (
          <div style={{ display: "flex", flexDirection: "column", gap: "var(--ds-space-8)" }}>
            <Disclosure title="Relations" meta="1">Waits on ACME-131 · Store webhook receipts · merged.</Disclosure>
            <Disclosure title="Goal & acceptance criteria" meta="6 criteria" defaultOpen>
              Make Acme webhook delivery resilient to transient failures without delivering the same event twice.
            </Disclosure>
          </div>
        )}</Panes>
      </Block>
      <Block id="ov-states" title="Overview states · active, needs you, ready, not started, ended; costs settling, zero, missing">
        <Panes mode={mode} stackNarrow>{() => (
          <div style={{ display: "flex", flexDirection: "column", gap: "var(--ds-space-24)" }}>
            {(["active", "needs-you", "ready", "not-started", "ended"] as const).map((s) => <OverviewState key={s} state={s} />)}
            <MetricGroup joined fill>
              <MetricTile label="Settling" value={settling(2.36, 0.22, "lg")} sub="estimate · lux settling" />
              <MetricTile label="Zero, priced" value={<Cost tokensUsd={0} machineUsd={0} tokensFrom="lux" machineFrom="lux" settled size="lg" />} sub="lux reported $0.00" />
              <MetricTile label="Missing" value={<Cost tokensUsd={null} machineUsd={null} size="lg" />} sub="not reported, never $0.00" />
            </MetricGroup>
          </div>
        )}</Panes>
      </Block>
      <Block id="ov-phone" title="Phone · 360px" note="Rows wrap: the figures and the way in go under the words, never off the side.">
        <Panes mode={mode}>{() => <div style={{ maxWidth: 360 }}><OverviewState state="active" /></div>}</Panes>
      </Block>
    </Section>
  );
}
