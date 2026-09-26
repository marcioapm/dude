/**
 * Time and cost: a task's (and each of its Runs'), or an epic's. How long
 * the agents worked, how long they waited on people, how long the change
 * sat in review, and what it cost — read from what dude already records.
 */

import { useEffect, useState } from "react";
import { CostDisplay, Duration, MetricGroup, MetricTile, TokenCount } from "@dude/design-system/components";
import { Section, Table, TBody, Td, Th, THead, Tr } from "@dude/design-system/primitives";
import { runLabel } from "@dude/domain";
import type { ApiClient, EpicMetrics, TaskMetrics } from "../api/client.ts";
import { reportedCost } from "../api/client.ts";

/**
 * Both re-read when `version` changes: the screen they sit in already
 * reloads on its own stream, and passes that on rather than each section
 * opening another.
 */

const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

/** A cost tile: "—", said to be not reported, for a cost of zero — as the run's header shows it. */
function CostTile({ usd, sub }: { usd: number; sub?: string }) {
  const known = reportedCost(usd);
  return known === null
    ? <MetricTile size="sm" label="Cost" value="—" title="Cost not reported" {...(sub ? { sub } : {})} />
    : <MetricTile size="sm" label="Cost" value={known} unit="usd" {...(sub ? { sub } : {})} />;
}

export function TaskMetricsSection({ client, taskId, live, done, version }: {
  client: ApiClient; taskId: string; live: boolean;
  /** Finished (done, failed or aborted): its lead time is whole, not so far. */
  done: boolean;
  version: number;
}) {
  const [m, setM] = useState<TaskMetrics | null>(null);
  useEffect(() => void client.taskMetrics(taskId).then(setM, () => {}), [client, taskId, version]);
  if (!m || m.runs.length === 0) return null;
  return (
    <Section title="Time & cost" data-testid="task-metrics">
      <MetricGroup joined>
        <MetricTile size="sm" label="Lead time" value={m.leadMs} unit="ms" live={live} sub={done ? "asked to done" : "so far"} />
        <MetricTile size="sm" label="Agents working" value={m.activeMs} unit="ms" live={live} />
        <MetricTile size="sm" label="Waiting on people" value={m.humanWaitMs} unit="ms" />
        <MetricTile size="sm" label="In review" value={m.reviewMs} unit="ms" />
        <CostTile usd={m.costUsd} sub={plural(m.runs.length, "run")} />
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
              <Td align="right" mono><CostDisplay usd={reportedCost(r.costUsd)} /></Td>
            </Tr>
          ))}
        </TBody>
      </Table>
    </Section>
  );
}

export function EpicMetricsSection({ client, epicId, version }: { client: ApiClient; epicId: string; version: number }) {
  const [m, setM] = useState<EpicMetrics | null>(null);
  useEffect(() => void client.epicMetrics(epicId).then(setM, () => {}), [client, epicId, version]);
  if (!m || m.tasks === 0) return null;
  return (
    <MetricGroup joined data-testid="epic-metrics">
      <MetricTile size="sm" label="Done" value={m.done} unit="count" sub={`of ${plural(m.tasks, "task")}`} />
      <MetricTile size="sm" label="Typical lead time" value={m.leadMsMedian ?? "—"} unit={m.leadMsMedian === null ? "none" : "ms"}
        sub="median, finished tasks" />
      <MetricTile size="sm" label="Agents working" value={m.activeMs} unit="ms" />
      <MetricTile size="sm" label="Waiting on people" value={m.humanWaitMs} unit="ms" />
      <CostTile usd={m.costUsd} />
    </MetricGroup>
  );
}
