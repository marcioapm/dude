/**
 * Time and cost: a task's (and each of its Runs'), or an epic's. How long
 * the agents worked, how long they waited on people, how long the change
 * sat in review, and what it cost — read from what dude already records.
 */

import { useEffect, useState } from "react";
import { Cost, Duration, MetricGroup, MetricTile, TokenCount } from "@dude/design-system/components";
import { Section, Table, TBody, Td, Th, THead, Tr } from "@dude/design-system/primitives";
import { runLabel } from "@dude/domain";
import { plural } from "@dude/design-system";
import type { ApiClient, CostSplit, EpicMetrics, TaskMetrics } from "../api/client.ts";
import { modelCostShown } from "../api/client.ts";

/**
 * Both re-read when `version` changes: the screen they sit in already
 * reloads on its own stream, and passes that on rather than each section
 * opening another.
 */

/**
 * A cost as a total of model tokens and machine time. The harness's zero
 * is "not reported", never $0.00 — as the run's header shows it; lux's
 * zero is a price. The tooltip says who priced each half, when known.
 */
function CostOf({ cost, tokens, activeMs, size }: { cost: CostSplit; tokens?: number; activeMs?: number; size?: "sm" | "md" | "lg" }) {
  const origin = cost.origin;
  const luxMachine = origin?.machine === "lux";
  return (
    <Cost tokensUsd={modelCostShown(cost.tokensUsd, origin?.tokens ?? "agent")}
      machineUsd={luxMachine || cost.machineUsd > 0 ? cost.machineUsd : null}
      {...(origin ? { tokensFrom: origin.tokens, machineFrom: origin.machine, settled: origin.settled } : {})}
      {...(tokens !== undefined ? { tokens } : {})} {...(activeMs !== undefined ? { machineMs: activeMs } : {})} size={size} />
  );
}

/** A cost tile: the total, split under it. */
function CostTile({ cost, tokens, activeMs, sub }: { cost: CostSplit; tokens: number; activeMs: number; sub?: string }) {
  return (
    <MetricTile size="sm" label="Cost" data-testid="cost-tile"
      value={<CostOf cost={cost} tokens={tokens} activeMs={activeMs} size="lg" />} {...(sub ? { sub } : {})} />
  );
}

export function TaskMetricsSection({ client, taskId, attempt, setAside, live, done, version }: {
  client: ApiClient; taskId: string;
  /** One attempt's figures alone; none, the whole task's. */
  attempt?: number | undefined;
  /** An attempt set aside: its lead time ran until the next one began. */
  setAside?: boolean | undefined;
  live: boolean;
  /** Finished (done, failed or aborted): its lead time is whole, not so far. */
  done: boolean;
  version: number;
}) {
  const [read, setRead] = useState<{ attempt: number | undefined; metrics: TaskMetrics } | null>(null);
  useEffect(() => {
    let current = true;
    void client.taskMetrics(taskId, attempt).then((metrics) => current && setRead({ attempt, metrics }), () => {});
    return () => {
      current = false;
    };
  }, [client, taskId, attempt, version]);
  // Another attempt's figures are never shown for this one, even for the moment before its own arrive.
  if (!read || read.attempt !== attempt || read.metrics.runs.length === 0) return null;
  const m = read.metrics;
  return (
    <Section title="Time & cost" data-testid="task-metrics">
      <MetricGroup joined>
        <MetricTile size="sm" label="Lead time" value={m.leadMs} unit="ms" live={live} sub={setAside ? "start to set aside" : done ? "asked to done" : "so far"} />
        <MetricTile size="sm" label="Agents working" value={m.activeMs} unit="ms" live={live} />
        <MetricTile size="sm" label="Waiting on people" value={m.humanWaitMs} unit="ms" />
        <MetricTile size="sm" label="In review" value={m.reviewMs} unit="ms" />
        <CostTile cost={m.cost} tokens={m.tokens.input + m.tokens.output} activeMs={m.activeMs} sub={plural(m.runs.length, "run")} />
      </MetricGroup>
      <Table density="compact" data-testid="run-metrics">
        <THead>
          <Tr>
            <Th>Run</Th>
            <Th align="right">Working</Th>
            <Th align="right">Parked</Th>
            <Th align="right">Tokens in / out</Th>
            <Th align="right">Cost</Th>
          </Tr>
        </THead>
        <TBody>
          {m.runs.map((r) => (
            <Tr key={r.id}>
              <Td fit>{runLabel(r)}</Td>
              <Td align="right" mono><Duration ms={r.activeMs} /></Td>
              <Td align="right" mono muted={r.parkedMs === 0}><Duration ms={r.parkedMs} /></Td>
              <Td align="right" mono>
                <TokenCount tokens={r.tokens.input} /> / <TokenCount tokens={r.tokens.output} />
              </Td>
              <Td align="right" mono><CostOf cost={r.cost} tokens={r.tokens.input + r.tokens.output} activeMs={r.activeMs} /></Td>
            </Tr>
          ))}
        </TBody>
      </Table>
    </Section>
  );
}

/** An epic's figures, as one quiet line under its header: how far along, how long, what it cost. */
export function EpicMetricsSection({ client, epicId, version }: { client: ApiClient; epicId: string; version: number }) {
  const [m, setM] = useState<EpicMetrics | null>(null);
  useEffect(() => void client.epicMetrics(epicId).then(setM, () => {}), [client, epicId, version]);
  if (!m || m.tasks === 0) return null;
  return (
    <div className="figures" data-testid="epic-metrics">
      <span>{m.done} of {plural(m.tasks, "task")} done</span>
      {m.leadMsMedian !== null ? <span title="Median lead time of its finished tasks">typically <Duration ms={m.leadMsMedian} /> each</span> : null}
      <span>agents <Duration ms={m.activeMs} /></span>
      {m.humanWaitMs > 0 ? <span>waiting on people <Duration ms={m.humanWaitMs} /></span> : null}
      <CostOf cost={m.cost} tokens={m.tokens.input + m.tokens.output} activeMs={m.activeMs} size="sm" />
    </div>
  );
}
