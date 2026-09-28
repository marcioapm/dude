/**
 * Time and cost: a task's (and each of its Runs'), or an epic's. How long
 * the agents worked, how long they waited on people, how long the change
 * sat in review, and what it cost — read from what dude already records.
 */

import { useEffect, useState } from "react";
import { Cost, Duration, MetricGroup, MetricTile, TokenCount } from "@dude/design-system/components";
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

/**
 * A cost tile: the total with its split (model tokens today; machine time
 * once lux reports it), "—" when nothing was reported — never $0.00.
 */
function CostTile({ usd, sub, tokens }: { usd: number; sub?: string; tokens?: number }) {
  return (
    <MetricTile size="sm" label="Cost" value={<Cost tokensUsd={reportedCost(usd)} size="lg" {...(tokens ? { tokens } : {})} />} unit="none"
      {...(sub ? { sub } : {})} />
  );
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
        <CostTile usd={m.costUsd} sub={plural(m.runs.length, "run")} tokens={m.tokens.input + m.tokens.output} />
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
              <Td align="right"><Cost tokensUsd={reportedCost(r.costUsd)} size="sm" tokens={r.tokens.input + r.tokens.output} /></Td>
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
      <Cost tokensUsd={reportedCost(m.costUsd)} size="sm" tokens={m.tokens.input + m.tokens.output} />
    </div>
  );
}
