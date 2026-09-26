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

/**
 * Both re-read when `version` changes: the screen they sit in already
 * reloads on its own stream, and passes that on rather than each section
 * opening another.
 */

export function TaskMetricsSection({ client, taskId, live, version }: {
  client: ApiClient; taskId: string; live: boolean; version: number;
}) {
  const [m, setM] = useState<TaskMetrics | null>(null);
  useEffect(() => void client.taskMetrics(taskId).then(setM, () => {}), [client, taskId, version]);
  if (!m || m.runs.length === 0) return null;
  return (
    <Section title="Time & cost" data-testid="task-metrics">
      <MetricGroup joined>
        <MetricTile size="sm" label="Lead time" value={m.leadMs} unit="ms" live={live} sub="asked to done" />
        <MetricTile size="sm" label="Agents working" value={m.activeMs} unit="ms" live={live} />
        <MetricTile size="sm" label="Waiting on people" value={m.humanWaitMs} unit="ms" />
        <MetricTile size="sm" label="In review" value={m.reviewMs} unit="ms" />
        <MetricTile size="sm" label="Cost" value={m.costUsd} unit="usd" sub={`${m.runs.length} runs`} />
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
              <Td>{runLabel(r)}</Td>
              <Td align="right" mono><Duration ms={r.activeMs} /></Td>
              <Td align="right" mono muted={r.parkedMs === 0}><Duration ms={r.parkedMs} /></Td>
              <Td align="right" mono>
                <TokenCount tokens={r.tokens.input} /> / <TokenCount tokens={r.tokens.output} />
              </Td>
              <Td align="right" mono><CostDisplay usd={r.costUsd} /></Td>
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
      <MetricTile size="sm" label="Done" value={m.done} unit="count" sub={`of ${m.tasks} tasks`} />
      <MetricTile size="sm" label="Typical lead time" value={m.leadMsMedian ?? "—"} unit={m.leadMsMedian === null ? "none" : "ms"}
        sub="median, finished tasks" />
      <MetricTile size="sm" label="Agents working" value={m.activeMs} unit="ms" />
      <MetricTile size="sm" label="Waiting on people" value={m.humanWaitMs} unit="ms" />
      <MetricTile size="sm" label="Cost" value={m.costUsd} unit="usd" />
    </MetricGroup>
  );
}
