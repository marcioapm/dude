import { useEffect, useMemo, useState } from "react";
import { Block, Caption, Col, Label, Panes, Row, Section, States, type PaneMode } from "../Frame.tsx";
import styles from "../gallery.module.css";
import { StatusBadge } from "../../components/StatusBadge.tsx";
import { AgentAvatar } from "../../components/AgentAvatar.tsx";
import { CostDisplay, Duration, TokenCount } from "../../components/Numbers.tsx";
import { MetricGroup, MetricTile } from "../../components/MetricTile.tsx";
import { EventDayDivider, EventRow, EventStream } from "../../components/EventRow.tsx";
import { SessionTree, SessionTreeNode } from "../../components/SessionTreeNode.tsx";
import { DiffView, parseUnifiedDiff } from "../../components/DiffView.tsx";
import { LogStream, type LogLine } from "../../components/LogStream.tsx";
import { Button, IconButton } from "../../primitives/Button.tsx";
import { Card, CardBody, CardHeader } from "../../primitives/Card.tsx";
import { ScrollArea } from "../../primitives/ScrollArea.tsx";
import { AGENT_ROLE_NAMES } from "../../tokens/palette.ts";
import { RUN_STATUSES, SESSION_STATUSES, WORK_ITEM_STATUSES } from "../../tokens/status.ts";
import { at, bulkLog, events, logLines, sessionTree, sessionTreeWaiting, unifiedDiff } from "../fixtures.tsx";

export function ComponentsSection({ mode }: { readonly mode: PaneMode }) {
  const files = useMemo(() => parseUnifiedDiff(unifiedDiff), []);
  return (
    <Section
      id="components"
      title="Factory components"
      intro="The domain vocabulary. These are the pieces an operator reads all day; each one is tuned to answer one of: what is running, what is stuck, what needs me, what did it cost, what did the agent do."
    >
      <Block
        id="c-status"
        title="StatusBadge"
        note={
          <>
            Every Run, Session and Work item status. Meaning is carried by tone <em>and</em> glyph <em>and</em> text. Only <code>awaiting_human</code> / <code>waiting_on_human</code> default to solid, with a slow expanding ring; that is the one thing on a busy screen that should pull the eye.
          </>
        }
      >
        <Panes mode={mode}>
          <Col>
            <Label>Work item</Label>
            <Row>
              {WORK_ITEM_STATUSES.map((s) => (
                <StatusBadge key={s} status={s} />
              ))}
            </Row>
            <Label>Run</Label>
            <Row>
              {RUN_STATUSES.map((s) => (
                <StatusBadge key={s} status={s} />
              ))}
            </Row>
            <Label>Session</Label>
            <Row>
              {SESSION_STATUSES.map((s) => (
                <StatusBadge key={s} status={s} />
              ))}
            </Row>
            <Label>Small</Label>
            <Row>
              {WORK_ITEM_STATUSES.map((s) => (
                <StatusBadge key={s} status={s} size="sm" />
              ))}
            </Row>
            <Label>Icon only (label in title + sr-only)</Label>
            <Row>
              {WORK_ITEM_STATUSES.map((s) => (
                <StatusBadge key={s} status={s} iconOnly />
              ))}
            </Row>
            <Label>Dot variant — shape carries meaning: hollow=pending, round=active, square=terminal, diamond=needs you</Label>
            <Row style={{ gap: 16 }}>
              {WORK_ITEM_STATUSES.map((s) => (
                <StatusBadge key={s} status={s} variant="dot" />
              ))}
            </Row>
            <Label>Emphasis override (avoid — shown for completeness)</Label>
            <States
              items={(["subtle", "tinted", "solid"] as const).map((e) => [
                e,
                <>
                  {(["queued", "running", "awaiting_human", "done", "failed", "aborted"] as const).map((s) => (
                    <StatusBadge key={s} status={s} emphasis={e} />
                  ))}
                </>,
              ])}
            />
            <Label>Grayscale check — the same row with color removed</Label>
            <Row style={{ filter: "grayscale(1)" }}>
              {(["queued", "running", "awaiting_human", "review", "ready_to_merge", "done", "failed", "aborted"] as const).map((s) => (
                <StatusBadge key={s} status={s} />
              ))}
            </Row>
          </Col>
        </Panes>
      </Block>

      <Block id="c-avatar" title="AgentAvatar" note="Who did it. Each role has a glyph and a hue; the orchestrator is additionally round. Humans are round with a ring, system is hollow, integrations are outlined. The live dot means 'running right now'.">
        <Panes mode={mode}>
          <Col>
            <Row style={{ gap: 16 }}>
              {AGENT_ROLE_NAMES.map((r) => (
                <AgentAvatar key={r} role={r} size="md" name={r} />
              ))}
            </Row>
            <Row style={{ gap: 16 }}>
              <AgentAvatar role="human" size="md" name="marcio" showRole />
              <AgentAvatar role="system" size="md" name="control-plane" showRole />
              <AgentAvatar role="integration" size="md" name="github" showRole />
            </Row>
            <States
              items={[
                ["xs / sm / md / lg", <>{(["xs", "sm", "md", "lg"] as const).map((s) => <AgentAvatar key={s} role="implementer" size={s} />)}</>],
                ["solid", <>{AGENT_ROLE_NAMES.map((r) => <AgentAvatar key={r} role={r} size="md" solid />)}</>],
                ["initial", <>{AGENT_ROLE_NAMES.map((r) => <AgentAvatar key={r} role={r} size="md" mark="initial" />)}</>],
                ["live", <>{AGENT_ROLE_NAMES.map((r) => <AgentAvatar key={r} role={r} size="md" live />)}</>],
                ["grayscale", <span style={{ filter: "grayscale(1)", display: "inline-flex", gap: 8 }}>{AGENT_ROLE_NAMES.map((r) => <AgentAvatar key={r} role={r} size="md" />)}</span>],
              ]}
            />
          </Col>
        </Panes>
      </Block>

      <Block id="c-numbers" title="CostDisplay · TokenCount · Duration" note="One formatter each, used everywhere. Tabular numerals always; full precision in the title. Two significant units for time, never three.">
        <Panes mode={mode}>
          <div className={styles["grid3"]}>
            <Col>
              <Label>CostDisplay</Label>
              <States
                items={[
                  ["0", <CostDisplay usd={0} />],
                  ["sub-cent", <CostDisplay usd={0.0042} />],
                  ["cents", <CostDisplay usd={0.42} />],
                  ["dollars", <CostDisplay usd={12.345} />],
                  ["thousands", <CostDisplay usd={1284.1} />],
                  ["compact", <CostDisplay usd={1284.1} compact />],
                  ["budget ok", <CostDisplay usd={1.2} budgetUsd={2.5} />],
                  ["budget 80%", <CostDisplay usd={2.12} budgetUsd={2.5} />],
                  ["over budget", <CostDisplay usd={2.71} budgetUsd={2.5} />],
                  ["live", <CostDisplay usd={1.284} live />],
                  ["muted mono", <CostDisplay usd={0.084} tone="muted" mono />],
                ]}
              />
            </Col>
            <Col>
              <Label>TokenCount</Label>
              <States
                items={[
                  ["< 1k", <TokenCount tokens={842} />],
                  ["k", <TokenCount tokens={12_400} />],
                  ["M", <TokenCount tokens={1_234_000} />],
                  ["exact", <TokenCount tokens={412_300} exact />],
                ]}
              />
            </Col>
            <Col>
              <Label>Duration</Label>
              <States
                items={[
                  ["ms", <Duration ms={420} />],
                  ["s", <Duration ms={42_100} />],
                  ["m s", <Duration ms={192_000} />],
                  ["h m", <Duration ms={7_440_000} />],
                  ["d h", <Duration ms={266_400_000} />],
                  ["clock", <Duration ms={7_440_000} format="clock" />],
                  ["long", <Duration ms={192_000} format="long" />],
                  ["live (ticking)", <Duration since={Date.now() - 754_000} />],
                  ["since/until", <Duration since={at(0)} until={at(1_640_000)} />],
                ]}
              />
            </Col>
          </div>
        </Panes>
      </Block>

      <Block id="c-metric" title="MetricTile" note="One number that matters. Delta color follows goodDirection: cost going up is bad, throughput going up is good, tokens are neutral. A max renders a thin budget bar that turns at 80% and 100%.">
        <Panes mode={mode}>
          <Col>
            <MetricGroup>
              <MetricTile label="Cost today" icon="dollar" value={48.21} unit="usd" delta={{ value: 0.12, kind: "percent", goodDirection: "down", label: "vs yesterday" }} />
              <MetricTile label="Tokens" icon="zap" value={8_412_000} unit="tokens" delta={{ value: 312_000 }} live />
              <MetricTile label="Median run" value={1_640_000} unit="ms" delta={{ value: -120_000, goodDirection: "down" }} />
              <MetricTile label="Merged this week" icon="merge" value={17} unit="count" delta={{ value: 4, goodDirection: "up" }} sub="3 awaiting review" />
            </MetricGroup>
            <MetricGroup joined>
              <MetricTile label="Running" value={6} unit="count" live />
              <MetricTile label="Needs you" value={2} unit="count" icon="hand" />
              <MetricTile label="Failed (24h)" value={1} unit="count" delta={{ value: -2, goodDirection: "down" }} />
              <MetricTile label="Success rate" value={0.94} unit="percent" delta={{ value: 0.02, kind: "percent", goodDirection: "up" }} />
            </MetricGroup>
            <Row top>
              <MetricTile label="Run budget" value={2.12} unit="usd" max={2.5} sub="of $2.50" size="sm" style={{ width: 180 }} />
              <MetricTile label="Run budget" value={2.71} unit="usd" max={2.5} sub="over by $0.21" size="sm" style={{ width: 180 }} />
              <MetricTile label="Run budget" value={0.6} unit="usd" max={2.5} size="sm" style={{ width: 180 }} />
              <MetricTile label="Flat, large" value="a3f9c1e" size="lg" flat unitLabel="sha" />
            </Row>
          </Col>
        </Panes>
      </Block>

      <Block
        id="c-event"
        title="EventRow / EventStream"
        note="One line per ledger event, fixed columns so hundreds align: time · actor · type · summary · trailing. Only success/attention/danger rows get a left stripe; everything else is quiet so the stripes are scannable. Click a row with a chevron to expand."
      >
        <Panes mode={mode} surface>
          <ScrollArea style={{ height: 380, border: "1px solid var(--ds-color-border-subtle)", borderRadius: 6 }}>
            <EventStream>
              {events.slice(0, 13).map((e) => (
                <EventRow key={e.id} occurredAt={e.occurredAt} actor={e.actor} eventType={e.eventType} summary={e.summary} severity={e.severity} trailing={e.trailing} detail={e.detail} meta={e.meta} />
              ))}
              <EventDayDivider date={at(1_520_000)} />
              {events.slice(13).map((e) => (
                <EventRow key={e.id} occurredAt={e.occurredAt} actor={e.actor} eventType={e.eventType} summary={e.summary} severity={e.severity} trailing={e.trailing} detail={e.detail} meta={e.meta} />
              ))}
            </EventStream>
          </ScrollArea>
        </Panes>
        <div style={{ height: 8 }} />
        <Panes mode={mode} surface>
          <Col>
            <Label>Compact, expanded by default, new-row flash</Label>
            <EventStream header={false}>
              <EventRow compact occurredAt={events[9]?.occurredAt ?? at(0)} actor={{ type: "agent", role: "implementer" }} eventType="tool.call.failed" summary={events[9]?.summary} severity="danger" detail={events[9]?.detail} defaultExpanded />
              <EventRow compact isNew occurredAt={at(0)} actor={{ type: "system" }} eventType="workflow.transition" summary="running → review" />
            </EventStream>
            <LiveStreamDemo />
          </Col>
        </Panes>
      </Block>

      <Block id="c-tree" title="SessionTreeNode" note="Orchestrator at depth 0, subagents nested. Finished sessions demote to a status dot and lighter text so live work stands out; the right column keeps tokens · cost · duration aligned so you can sum a tree by eye. Arrow keys expand/collapse.">
        <Panes mode={mode} surface>
          <TreeDemo />
        </Panes>
      </Block>

      <Block id="c-diff" title="DiffView" note="Unified diff per file. Add/remove are distinguished by background, gutter color and the sign column, so it survives grayscale. Line numbers stick on horizontal scroll; big files collapse by default; renamed/added/deleted/binary get a badge.">
        <Panes mode={mode}>
          <DiffView files={files} />
        </Panes>
        <div style={{ height: 8 }} />
        <Panes mode={mode}>
          <Row>
            <Caption>grayscale:</Caption>
            <div style={{ filter: "grayscale(1)", flex: 1 }}>
              <DiffView files={files.slice(1, 2)} summary={false} />
            </div>
          </Row>
        </Panes>
      </Block>

      <Block id="c-log" title="LogStream" note="Monospace, follows the tail while you are at the bottom; scroll up and it stops and offers 'Jump to latest'. Levels change ink, never background. Window of 2000 rendered lines by default.">
        <Panes mode={mode}>
          <Col>
            <LogStream lines={logLines} title="bun test" timestamps live maxHeight={300} />
            <BulkLogDemo />
            <LogStream lines={[]} title="stdout" maxHeight={80} />
          </Col>
        </Panes>
      </Block>

      <Block id="c-composed" title="Composed: work item header" note="A quick sanity check that the pieces sit together at real density.">
        <Panes mode={mode}>
          <Card>
            <CardHeader
              title={
                <Row>
                  <span className="ds-mono" style={{ color: "var(--ds-color-text-muted)", fontSize: 12 }}>
                    WI-2481
                  </span>
                  <span>Add retry with backoff to the GitHub webhook handler</span>
                  <StatusBadge status="awaiting_human" />
                </Row>
              }
              actions={
                <>
                  <Button size="sm" variant="secondary" leadingIcon="hand">
                    Answer
                  </Button>
                  <IconButton icon="more" label="More" size="sm" />
                </>
              }
            />
            <CardBody padding="dense">
              <MetricGroup joined>
                <MetricTile label="Cost" value={1.284} unit="usd" max={2.5} size="sm" live />
                <MetricTile label="Tokens" value={412_300} unit="tokens" size="sm" />
                <MetricTile label="Elapsed" value={1_640_000} unit="ms" size="sm" />
                <MetricTile label="Sessions" value={5} unit="count" size="sm" sub="2 running" />
                <MetricTile label="Attempt" value={2} unit="count" size="sm" sub="of 3" />
              </MetricGroup>
            </CardBody>
          </Card>
        </Panes>
      </Block>
    </Section>
  );
}

function TreeDemo() {
  const [selected, setSelected] = useState<string | null>("ses_01J9K6");
  return (
    <Col>
      <SessionTree aria-label="Sessions">
        <SessionTreeNode node={sessionTree} selectedId={selected} onSelect={setSelected} trailing={() => <IconButton icon="external" label="Open" size="sm" />} />
      </SessionTree>
      <Label>Waiting on human, with an aborted child</Label>
      <SessionTree aria-label="Sessions (waiting)">
        <SessionTreeNode node={sessionTreeWaiting} selectedId={selected} onSelect={setSelected} />
      </SessionTree>
    </Col>
  );
}

function LiveStreamDemo() {
  const [rows, setRows] = useState<Array<{ id: number; at: string; text: string }>>([]);
  const [running, setRunning] = useState(false);
  useEffect(() => {
    if (!running) return;
    const id = setInterval(() => {
      setRows((r) => [...r.slice(-7), { id: Date.now(), at: new Date().toISOString(), text: `tool.call.completed read src/file-${r.length + 1}.ts` }]);
    }, 900);
    return () => clearInterval(id);
  }, [running]);
  return (
    <Col>
      <Row>
        <Label>Streaming rows (new rows flash once)</Label>
        <Button size="sm" onClick={() => setRunning((v) => !v)}>
          {running ? "Stop" : "Start stream"}
        </Button>
      </Row>
      <EventStream header={false}>
        {rows.map((r) => (
          <EventRow key={r.id} compact isNew occurredAt={r.at} actor={{ type: "agent", role: "investigator" }} eventType="tool.call.completed" summary={r.text} trailing="3ms" />
        ))}
      </EventStream>
    </Col>
  );
}

function BulkLogDemo() {
  const [lines, setLines] = useState<LogLine[]>(() => bulkLog(400));
  const [running, setRunning] = useState(false);
  useEffect(() => {
    if (!running) return;
    const id = setInterval(() => {
      setLines((l) => {
        const next = bulkLog(3, (l[l.length - 1]?.seq ?? 0) + 1);
        return [...l, ...next];
      });
    }, 250);
    return () => clearInterval(id);
  }, [running]);
  return (
    <LogStream
      lines={lines}
      title="worker-03 · stdout"
      nowrap
      live={running}
      maxHeight={240}
      toolbar={
        <Button size="sm" variant="ghost" onClick={() => setRunning((v) => !v)}>
          {running ? "Stop" : "Stream"}
        </Button>
      }
    />
  );
}
