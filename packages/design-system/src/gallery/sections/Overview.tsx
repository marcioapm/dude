import type { ReactNode } from "react";
import { Block, Panes, Section, type PaneMode } from "../Frame.tsx";
import { AgentAvatar } from "../../components/AgentAvatar.tsx";
import { Cost } from "../../components/Cost.tsx";
import { Disclosure } from "../../components/Disclosure.tsx";
import { Duration } from "../../components/Numbers.tsx";
import { MetricGroup, MetricTile } from "../../components/MetricTile.tsx";
import { RunningList, RunningRow } from "../../components/RunningRow.tsx";
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

function Running({ state }: { readonly state: State }) {
  const words: Record<Exclude<State, "active">, ReactNode> = {
    "needs-you": "Waiting on Sam Rivera to answer.",
    ready: <><StatusMark status="ready_to_merge" size="sm" /> Ready to merge: nothing is running.</>,
    "not-started": "Not started: choose how it begins.",
    ended: "Attempt 1 · set aside: nothing in it runs again, and its records are only to read.",
  };
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: "var(--ds-space-12)", padding: "var(--ds-space-16)", background: "var(--ds-color-raised)" }}>
      <strong>Running now{state === "active" ? " · 2 agents" : ""}</strong>
      {state === "active" ? <Reviewers /> : <span style={{ color: "var(--ds-color-text-muted)" }}>{words[state]}</span>}
      {state === "not-started" ? <div style={{ display: "flex", gap: "var(--ds-space-8)" }}><Button size="sm" variant="secondary">Talk it through</Button><Button size="sm" variant="secondary">Deliver</Button></div> : null}
    </div>
  );
}

/** One Overview state, composed of the parts the app composes. */
function OverviewState({ state }: { readonly state: State }) {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: "var(--ds-space-12)", minWidth: 0 }}>
      {state === "needs-you" ? (
        <Callout tone="attention" fill>
          <strong>Needs Sam Rivera · Conductor asks</strong>
          <div>Should retries stop after 5 attempts or after 24 hours?</div>
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
