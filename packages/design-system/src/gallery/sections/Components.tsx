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
import { FindingGroup, FindingRow, FINDING_SEVERITIES, FINDING_SEVERITY_SPECS, FINDING_STATUSES } from "../../components/FindingRow.tsx";
import { Breadcrumb } from "../../components/Breadcrumb.tsx";
import { ArtifactGroup, ArtifactRow, type ArtifactChange } from "../../components/ArtifactRow.tsx";
import { ArtifactPreview } from "../../components/ArtifactPreview.tsx";
import { Badge } from "../../primitives/Badge.tsx";
import { Callout } from "../../primitives/Layout.tsx";
import { SessionItem, SessionList } from "../../components/SessionList.tsx";
import { StatusMark } from "../../components/StatusMark.tsx";
import { Button, IconButton } from "../../primitives/Button.tsx";
import { Icon } from "../../icons/index.tsx";
import { Card, CardBody, CardHeader } from "../../primitives/Card.tsx";
import { ScrollArea } from "../../primitives/ScrollArea.tsx";
import { AGENT_ROLE_NAMES } from "../../tokens/palette.ts";
import { RUN_STATUSES, SESSION_STATUSES, TASK_STATUSES } from "../../tokens/status.ts";
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
            Every Run, Session and Task status. Meaning is carried by tone <em>and</em> glyph <em>and</em> text. Only <code>awaiting_input</code> / <code>awaiting_input</code> default to solid, with a slow expanding ring; that is the one thing on a busy screen that should pull the eye.
          </>
        }
      >
        <Panes mode={mode}>
          <Col>
            <Label>Task</Label>
            <Row>
              {TASK_STATUSES.map((s) => (
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
              {TASK_STATUSES.map((s) => (
                <StatusBadge key={s} status={s} size="sm" />
              ))}
            </Row>
            <Label>Icon only (label in title + sr-only)</Label>
            <Row>
              {TASK_STATUSES.map((s) => (
                <StatusBadge key={s} status={s} iconOnly />
              ))}
            </Row>
            <Label>Dot variant — shape carries meaning: hollow=pending, round=active, square=terminal, diamond=needs you</Label>
            <Row style={{ gap: 16 }}>
              {TASK_STATUSES.map((s) => (
                <StatusBadge key={s} status={s} variant="dot" />
              ))}
            </Row>
            <Label>Emphasis override (avoid — shown for completeness)</Label>
            <States
              items={(["subtle", "tinted", "solid"] as const).map((e) => [
                e,
                <>
                  {(["queued", "running", "awaiting_input", "done", "failed", "aborted"] as const).map((s) => (
                    <StatusBadge key={s} status={s} emphasis={e} />
                  ))}
                </>,
              ])}
            />
            <Label>Grayscale check — the same row with color removed</Label>
            <Row style={{ filter: "grayscale(1)" }}>
              {(["queued", "running", "awaiting_input", "review", "ready_to_merge", "done", "failed", "aborted"] as const).map((s) => (
                <StatusBadge key={s} status={s} />
              ))}
            </Row>
          </Col>
        </Panes>
      </Block>

      <Block id="c-avatar" title="AgentAvatar" note="Who did it. Each role has a glyph and a hue; the conductor is additionally round. Humans are round with a ring, system is hollow, integrations are outlined. The live dot means 'running right now'.">
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

      <Block id="c-sessions" title="SessionList · a stalled Run" note="A task's sessions. A Run dude reported as making no progress — a tool call open for its window, its agent silent for it, or a Run that changes code whose files did not change — carries a 'stalled' Badge before its status, dude's facts as its title, until it makes progress or ends. Its owner's banner on a plain delivery is a Callout with the facts and three choices: Restart it (a fresh agent in its place, an optional note), Leave it, Stop the task.">
        <Panes mode={mode} surface>
          <Col>
            <SessionList style={{ maxWidth: 360 }}>
              <SessionItem onOpen={() => undefined} avatar={<AgentAvatar role="reviewer" size="lg" live />} title="Review · frontend"
                detail="gpt-5.6-sol · 31m" trailing={<>
                  <Badge tone="attention" size="sm" icon="warning" title="Your review Run has made no progress for 30 min.">stalled</Badge>
                  <StatusMark status="running" size="sm" />
                </>} />
              <SessionItem onOpen={() => undefined} avatar={<AgentAvatar role="reviewer" size="lg" live />} title="Review · correctness"
                detail="gpt-5.6-sol · 31m" trailing={<StatusMark status="running" size="sm" />} />
              <SessionItem onOpen={() => undefined} avatar={<AgentAvatar role="implementer" size="lg" />} title="Implement"
                detail="claude-opus-5-5 · 12m" trailing={<StatusMark status="completed" size="sm" iconOnly />} />
              <SessionItem onOpen={() => undefined} avatar={<AgentAvatar role="reviewer" size="lg" />} title="Review · frontend"
                detail="gpt-5.6-sol · 30m" trailing={<StatusMark status="aborted" label="Restarted" size="sm" />}>
                <a href="#c-sessions">Open replacement Run</a>
              </SessionItem>
            </SessionList>
            <Label>its owner's banner, on a plain delivery</Label>
            <Callout tone="attention">
              <p style={{ margin: 0 }}>
                <strong>Review has made no progress.</strong> Your frontend review Run (round 3, Sol, running 2.1 h) has made no progress for
                2.0 h. It has 1 tool call open: <code>task</code> "Review worker state behavior", for 2.0 h; <code>task</code> runs inside the
                agent: no separate process is expected. Processes: opencode acp (1.2 % CPU). CPU over 2.0 h: 18 s; network: 0 B.
              </p>
              <Row>
                <Button size="sm" variant="primary">Restart it</Button>
                <Button size="sm" variant="secondary">Leave it</Button>
                <Button size="sm" variant="quiet">Stop the task</Button>
              </Row>
            </Callout>
          </Col>
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

      <Block
        id="c-finding"
        title="FindingRow / FindingGroup"
        note="One review finding per 28px row: severity as glyph + word in its tone (never hue alone, never an uppercase enum), the category, the title, the file:line in mono, then the status as a neutral badge — so a resolved blocking finding still reads as blocking and as resolved. Description and suggested fix expand under the row. Settled rows dim; nothing is struck through. The group sorts open first, most severe first, and counts what is still open. 'fixed in ›' is a slot the app fills with a link to the fix run."
      >
        <Panes mode={mode}>
          <Col>
            <Label>Severities</Label>
            <Row style={{ gap: 16 }}>
              {FINDING_SEVERITIES.map((s) => (
                <span key={s} style={{ display: "inline-flex", alignItems: "center", gap: 4, fontSize: 12, color: `var(--ds-tone-${FINDING_SEVERITY_SPECS[s].tone}-fg)` }}>
                  <Icon name={FINDING_SEVERITY_SPECS[s].glyph} size={12} />
                  {FINDING_SEVERITY_SPECS[s].label}
                </span>
              ))}
            </Row>
            <Label>Every status, one severity</Label>
            <ul style={{ listStyle: "none", margin: 0, padding: 0, display: "flex", flexDirection: "column", gap: 2 }}>
              {FINDING_STATUSES.map((st) => (
                <FindingRow key={st} severity="high" status={st} category="security" title={`Token stored in localStorage (${st})`} file="apps/web/src/auth/session.ts" line={42} description="The refresh token is written to `localStorage`, readable by any script on the origin." suggestedFix="Keep it in an `HttpOnly` cookie scoped to `/auth`." fixedIn={st === "resolved" ? <a href="#c-finding">Fix 2</a> : undefined} fixAttempts={st === "resolved" ? 2 : 0} resolutionNote={st === "accepted" ? "Accepted for the prototype; tracked in CP-88." : st === "superseded" ? "Replaced by a broader finding on the auth module." : undefined} />
              ))}
            </ul>
            <Label>Grouped: open first</Label>
            <FindingGroupDemo />
            <Label>Grayscale</Label>
            <div style={{ filter: "grayscale(1)" }}>
              <ul style={{ listStyle: "none", margin: 0, padding: 0, display: "flex", flexDirection: "column", gap: 2 }}>
                <FindingRow severity="blocking" status="open" category="correctness" title="Retry loop never terminates on 4xx" file="apps/control-plane/src/webhooks.ts" line={118} />
                <FindingRow severity="note" status="resolved" category="style" title="Prefer `for…of` over `forEach` here" file="apps/control-plane/src/webhooks.ts" line={130} />
              </ul>
            </div>
          </Col>
        </Panes>
      </Block>

      <Block
        id="c-artifact"
        title="ArtifactRow / ArtifactGroup / ArtifactPreview"
        note="Files an agent publishes — a design doc, a report, a screenshot, a JSON result. One 28px row each: a glyph for the kind, the path in mono, the size, who produced it (role avatar and phase), age, and the app's download link; 'New' / 'Updated' since the previous run is a neutral badge. The leading part of the row is a disclosure button; the content opens under it. The preview renders by kind — Markdown as a document, text and JSON in mono (JSON pretty-printed when it parses, as-is when it does not), images on a checkerboard, and 'No preview' plus the download link for anything else. Long content clamps at 400px behind 'Show all'. The group only says 'No artifacts yet' when the screen asks for it."
      >
        <Panes mode={mode}>
          <Col>
            <Label>Group, each kind, expanded and collapsed</Label>
            <ArtifactDemo />
            <Label>Preview states</Label>
            <ArtifactPreviewStates />
            <Label>Empty: only with `empty`</Label>
            <ArtifactGroup artifacts={[]} renderRow={() => null} empty="Agents publish documents and results here." />
            <Caption>(without `empty`, an empty group renders nothing)</Caption>
            <ArtifactGroup artifacts={[]} renderRow={() => null} />
          </Col>
        </Panes>
      </Block>

      <Block
        id="c-breadcrumb"
        title="Breadcrumb"
        note="Where you are: Project › Epic › KEY. Every crumb but the last is a link or a button (text-coloured until hovered); the last is the current place and is aria-current. Long middle crumbs elide in the middle so the head and the tail both survive, with the full text in the title; the last crumb is never elided. Use it in the task header and the transcript header instead of a Back button."
      >
        <Panes mode={mode}>
          <Col>
            <States
              items={[
                ["project › epic › key", <Breadcrumb items={[{ id: "p", label: "Customer Portal", onSelect: () => {} }, { id: "e", label: "OAuth migration", icon: "layers", onSelect: () => {} }, { id: "w", label: "CP-41", mono: true }]} />],
                ["no epic", <Breadcrumb items={[{ id: "p", label: "Customer Portal", href: "#c-breadcrumb" }, { id: "w", label: "CP-52", mono: true }]} />],
                ["long middle, maxChars 20", <Breadcrumb maxChars={20} items={[{ id: "p", label: "control-plane", href: "#c-breadcrumb" }, { id: "e", label: "Webhook reliability and delivery guarantees", icon: "layers", href: "#c-breadcrumb" }, { id: "w", label: "WI-2401", mono: true }]} />],
                ["small, four levels", <Breadcrumb size="sm" maxChars={16} items={[{ id: "o", label: "acme", href: "#c-breadcrumb" }, { id: "p", label: "Customer Portal", href: "#c-breadcrumb" }, { id: "e", label: "OAuth migration", icon: "layers", href: "#c-breadcrumb" }, { id: "w", label: "CP-41", mono: true }]} />],
                ["settings screen", <Breadcrumb items={[{ id: "p", label: "Customer Portal", href: "#c-breadcrumb" }, { id: "s", label: "Settings" }]} />],
                ["in a header", <Row style={{ gap: 12 }}><Breadcrumb items={[{ id: "p", label: "Customer Portal", href: "#c-breadcrumb" }, { id: "e", label: "OAuth migration", icon: "layers", href: "#c-breadcrumb" }, { id: "w", label: "CP-41", mono: true }]} /><StatusBadge status="review" size="sm" /><Badge tone="neutral" size="sm">reconnecting</Badge></Row>],
              ]}
            />
          </Col>
        </Panes>
      </Block>

      <Block id="c-composed" title="Composed: task header" note="A quick sanity check that the pieces sit together at real density.">
        <Panes mode={mode}>
          <Card>
            <CardHeader
              title={
                <Row>
                  <span className="ds-mono" style={{ color: "var(--ds-color-text-muted)", fontSize: 12 }}>
                    WI-2481
                  </span>
                  <span>Add retry with backoff to the GitHub webhook handler</span>
                  <StatusBadge status="awaiting_input" />
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

const FINDINGS = [
  { id: "f1", severity: "note", status: "open", category: "style", title: "Inconsistent naming between `deliveryId` and `delivery_id`", file: "apps/control-plane/src/webhooks.ts", line: 12, description: "Both spellings appear in this module." },
  { id: "f2", severity: "blocking", status: "resolved", category: "correctness", title: "Signature verified after the body is parsed", file: "apps/control-plane/src/webhooks.ts", line: 44, description: "An attacker can trigger JSON parsing of arbitrary payloads before rejection.", suggestedFix: "Verify the HMAC over the raw body first; parse only on success.", resolutionNote: "Fixed in round 1: verification moved ahead of parsing.", fixAttempts: 1 },
  { id: "f3", severity: "high", status: "open", category: "database", title: "Dedupe table has no TTL; grows unbounded", file: "migrations/0007_deliveries.sql", line: 3, description: "Every delivery id is kept forever.", suggestedFix: "Add `expires_at` and a nightly sweep, or a partial index on the last 7 days." },
  { id: "f4", severity: "medium", status: "accepted", category: "performance", title: "Retry backoff is computed with `Math.pow` per attempt", file: "apps/control-plane/src/retry.ts", line: 27, resolutionNote: "Negligible at our volumes; accepted." },
  { id: "f5", severity: "blocking", status: "open", category: "security", title: "Webhook secret read from an unset env var falls back to empty string", file: "apps/control-plane/src/config.ts", line: 88, description: "With no secret, every signature verifies.", suggestedFix: "Fail startup when `GITHUB_WEBHOOK_SECRET` is unset." },
  { id: "f6", severity: "low", status: "superseded", category: "api", title: "Replay endpoint returns 200 for unknown ids", file: "apps/control-plane/src/replay.ts", line: 15, resolutionNote: "Superseded by the endpoint's redesign in CP-60." },
] as const;

const ARTIFACT_MD = `# Login flow: PKCE migration

## Summary

Every login flow moves to the authorization-code flow with PKCE. The implicit
flow is removed; refresh tokens move to an \`HttpOnly\` cookie scoped to \`/auth\`.

## Changes

- Web: \`apps/web/src/auth/*\` — code verifier in \`sessionStorage\`, exchanged once.
- Mobile: the app scheme redirect stays; the verifier lives in the keychain.
- CLI: loopback redirect on a random port, as \`gh auth login\` does.

## Risks

| Area | Risk | Mitigation |
|---|---|---|
| Sessions | Existing tokens expire at cut-over | Rolling deploy; refresh both kinds for 24h |
| Mobile | Old builds cannot log in | Force-update gate on the API |

## Rollout

1. Ship the server side behind a flag.
2. Move the web client.
3. Move mobile and CLI.
4. Remove the implicit flow.

## Open questions

- Do we keep the 30-day refresh lifetime, or shorten it to 7 days now that
  rotation is cheap?
- Which teams still call the token endpoint directly?
`;
const ARTIFACT_JSON = `{"suite":"auth","passed":42,"failed":1,"failures":[{"name":"refresh rotates token","file":"apps/web/src/auth/session.test.ts","line":88,"message":"expected 200, got 401"}],"durationMs":8123}`;
const ARTIFACT_LOG = Array.from({ length: 80 }, (_, i) => `${String(i + 1).padStart(3, " ")}  ${["GET", "POST", "GET", "PUT"][i % 4]} /auth/${["token", "callback", "refresh", "logout"][i % 4]} ${[200, 200, 302, 401][i % 4]} ${(12 + ((i * 37) % 180)).toFixed(0)}ms`).join("\n");
const PNG_URL =
  "data:image/svg+xml;utf8," +
  encodeURIComponent(
    '<svg xmlns="http://www.w3.org/2000/svg" width="360" height="200"><rect x="20" y="20" width="320" height="160" rx="8" fill="#ffffff" stroke="#c7cbcf"/><rect x="44" y="52" width="272" height="28" rx="4" fill="#f2f4f6" stroke="#c7cbcf"/><rect x="44" y="92" width="272" height="28" rx="4" fill="#f2f4f6" stroke="#c7cbcf"/><rect x="44" y="136" width="120" height="28" rx="4" fill="#126db1"/><text x="104" y="155" font-family="sans-serif" font-size="12" fill="#fff" text-anchor="middle">Sign in</text></svg>',
  );

const ARTIFACTS: ReadonlyArray<{ id: string; name: string; contentType: string; sizeBytes: number; sha256: string; role: "implementer" | "reviewer" | "simplifier"; phase: string; publishedAt: string; change?: ArtifactChange; text?: string; url?: string; open?: boolean }> = [
  { id: "a1", name: "design.md", contentType: "text/markdown", sizeBytes: 1_284, sha256: "9f2c…e41b", role: "implementer", phase: "write", publishedAt: at(-2 * 3_600_000), change: "updated", text: ARTIFACT_MD, open: true },
  { id: "a2", name: "screens/login.png", contentType: "image/png", sizeBytes: 48_213, sha256: "0a1b…77cd", role: "implementer", phase: "implement", publishedAt: at(-3 * 3_600_000), change: "new", url: PNG_URL, open: true },
  { id: "a3", name: "results/tests.json", contentType: "application/json", sizeBytes: 812, sha256: "beef…0042", role: "reviewer", phase: "review", publishedAt: at(-45 * 60_000), text: ARTIFACT_JSON, open: true },
  { id: "a4", name: "logs/requests.log", contentType: "text/plain", sizeBytes: 6_140, sha256: "1234…abcd", role: "simplifier", phase: "simplify", publishedAt: at(-26 * 3_600_000), text: ARTIFACT_LOG, open: true },
  { id: "a5", name: "bundle.tar.gz", contentType: "application/gzip", sizeBytes: 3_401_772, sha256: "77aa…bb10", role: "implementer", phase: "implement", publishedAt: at(-3 * 86_400_000), open: true },
  { id: "a6", name: "notes.txt", contentType: "text/plain", sizeBytes: 302, sha256: "5f5f…0a0a", role: "reviewer", phase: "review", publishedAt: at(-9 * 86_400_000), change: "new", text: "Left the retry helper alone: it is covered by the integration suite and not on the hot path." },
];

function ArtifactDemo() {
  return (
    <ArtifactGroup
      artifacts={ARTIFACTS}
      actions={
        <Button size="sm" variant="quiet" leadingIcon="download">
          Download all
        </Button>
      }
      renderRow={(a) => (
        <ArtifactRow
          key={a.id}
          name={a.name}
          contentType={a.contentType}
          sizeBytes={a.sizeBytes}
          sha256={a.sha256}
          producer={{ role: a.role, phase: a.phase }}
          publishedAt={a.publishedAt}
          change={a.change}
          defaultExpanded={a.open}
          download={
            <a href={a.url ?? "#c-artifact"} download={a.name.split("/").pop()}>
              <Icon name="download" size={12} /> Download
            </a>
          }
          preview={<ArtifactPreview contentType={a.contentType} name={a.name} text={a.text} url={a.url} download={<a href="#c-artifact" download>Download</a>} />}
        />
      )}
    />
  );
}

function ArtifactPreviewStates() {
  return (
    <States
      items={[
        ["loading", <ArtifactPreview contentType="text/markdown" name="design.md" loading />],
        ["error", <ArtifactPreview contentType="text/markdown" name="design.md" error="Could not fetch the artifact (404)." />],
        ["invalid JSON, shown as-is", <ArtifactPreview contentType="application/json" name="partial.json" text={'{"passed": 4, "failed": '} />],
        ["type from the name", <ArtifactPreview contentType="application/octet-stream" name="README.md" text={"# Title\n\nThe media type was generic; the `.md` decided it."} />],
        ["no preview", <ArtifactPreview contentType="application/pdf" name="report.pdf" download={<a href="#c-artifact" download>Download report.pdf</a>} />],
      ]}
    />
  );
}

function FindingGroupDemo() {
  return (
    <FindingGroup
      findings={FINDINGS}
      actions={
        <Button size="sm" variant="quiet" leadingIcon="reviewer">
          Review run
        </Button>
      }
      renderRow={(f) => <FindingRow key={f.id} {...f} fixedIn={f.status === "resolved" ? <a href="#c-finding">Fix 1</a> : undefined} onOpenLocation={() => {}} />}
    />
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
        <Button size="sm" variant="quiet" onClick={() => setRunning((v) => !v)}>
          {running ? "Stop" : "Stream"}
        </Button>
      }
    />
  );
}
