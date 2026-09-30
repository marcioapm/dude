/**
 * The servers a task or a run has, as one section: the run they live on
 * (the agent's, or a branch preview's), a notice when the run moved host,
 * a preview's stages, the list with each server's log folding open under
 * it, and Preview opening the page in a new tab. It is the task's Servers
 * tab: a session has no servers panel of its own.
 */

import { memo, useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import {
  AgentAvatar,
  Duration,
  PersonAvatar,
  PreviewAlsoRunning,
  PreviewStages,
  ServerList,
  ServerRow,
  ServerStateDot,
  ServersMoved,
  ServersPanel,
  ServersRecipesPreview,
  ServersRunLine,
  ShortId,
  StatusMark,
  TerminalLink,
} from "@dude/design-system/components";
import { anyMoving, canStartAny, canStop, canStopAny, describeServer, firstName, formatTimestamp, isMoving, PREVIEW_IDLE_TIMEOUT_DEFAULT_MINUTES, serverLogLines, summarizeServers, toggled, useNow, type ServersRun } from "@dude/design-system";
import { Button, Callout, Dialog, EmptyState, FormActions, RowMenu, Spinner, TabCount } from "@dude/design-system/primitives";
import { ALL_STATUSES } from "@dude/design-system/tokens";
import type { RunStatus, TaskServers } from "@dude/domain";
import type { LogLine } from "@dude/design-system/components";
import type { ApiClient } from "../api/client.ts";
import { runIsLive, type ServersState } from "../hooks/useServers.ts";
import { usePeople } from "../people.tsx";
import { AddServerDialog } from "./AddServerDialog.tsx";

export interface ServersSectionProps {
  client: ApiClient;
  servers: ServersState;
  /** The task, for Preview branch (its branch is what a preview checks out). */
  taskId?: string | undefined;
}

/** The run status as StatusMark says it: a preview's own word until its servers are up; a word the vocabulary lacks, as it came. */
function runMark(run: ServersRun) {
  if (run.kind === "preview" && run.previewStage && run.previewStage !== "ready" && runIsLive(run)) return <StatusMark status="starting" size="sm" />;
  const known = (ALL_STATUSES as readonly string[]).includes(run.state);
  return <StatusMark status={known ? (run.state as RunStatus) : "running"} size="sm" label={known ? undefined : run.state} />;
}

export const ServersSection = memo(function ServersSection({ client, servers, taskId }: ServersSectionProps) {
  const { data, problem, busy } = servers;
  const people = usePeople();
  const now = useNow(Boolean(data && anyMoving(data.servers)), 30_000);
  const [openLogs, setOpenLogs] = useState<ReadonlySet<string>>(() => new Set());
  // Each open server's log as LogStream draws it, converted once when read.
  const [logs, setLogs] = useState<Record<string, LogLine[] | "loading">>({});
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<string | null>(null);

  const run = data?.run ?? null;
  const runId = run?.id ?? null;

  // A server's log is read when its row opens, and again while it is open
  // each time that server changes: the log is not on the stream, and a
  // change elsewhere (another row folding, another server moving) is not
  // a reason to read it again.
  const readLog = useCallback(async (name: string) => {
    if (!runId) return;
    setLogs((l) => (l[name] ? l : { ...l, [name]: "loading" }));
    try {
      const { lines } = await client.serverLog(runId, name);
      setLogs((l) => ({ ...l, [name]: serverLogLines(lines) }));
    } catch {
      setLogs((l) => ({ ...l, [name]: [] }));
    }
  }, [client, runId]);
  const wasOpen = useRef<ReadonlySet<string>>(new Set());
  useEffect(() => {
    for (const name of openLogs) if (!wasOpen.current.has(name)) void readLog(name);
    wasOpen.current = openLogs;
  }, [openLogs, readLog]);
  // What each open server looked like at its last read: a change re-reads.
  const seen = useRef<Map<string, string>>(new Map());
  useEffect(() => {
    for (const s of data?.servers ?? []) {
      const mark = `${s.state}|${s.since}|${s.epoch}`;
      if (openLogs.has(s.name) && seen.current.get(s.name) !== undefined && seen.current.get(s.name) !== mark) void readLog(s.name);
      seen.current.set(s.name, mark);
    }
  }, [data, openLogs, readLog]);

  // An exited server opens its log by itself, once: the error is never
  // behind a click, and a person who folds it keeps it folded.
  const shown = useRef<Set<string>>(new Set());
  useEffect(() => {
    const fresh = (data?.servers ?? []).filter((s) => s.state === "exited" && !shown.current.has(s.name)).map((s) => s.name);
    if (fresh.length === 0) return;
    for (const name of fresh) shown.current.add(name);
    setOpenLogs((s) => new Set([...s, ...fresh]));
  }, [data]);

  const startPreview = () => taskId && void servers.act("*", () => client.startPreview(taskId));
  const stopPreview = () => taskId && void servers.act("*", () => client.stopPreview(taskId));

  if (!data) return <div className="centered">{problem ?? <Spinner label="Loading…" />}</div>;

  // No run serves the task: the way to a preview, and what one starts.
  if (!run) {
    const auto = data.recipes.filter((r) => r.autostartInPreviews).map((r) => r.name);
    return (
      <ServersPanel data-testid="servers-panel" data-run="none">
        <EmptyState
          icon="globe"
          title="Nothing is serving this task"
          description="Servers live on a run. When an agent's run ends, its servers end with it. Preview the branch to bring up the project’s servers on a lightweight run of their own — no agent, just the checkout."
          className="serversEmpty"
          action={taskId ? (
            <FormActions note={data.recipes.length > 0
              ? `${auto.length > 0 ? `${auto.join(", ")} start${auto.length === 1 ? "s" : ""} automatically` : "nothing starts automatically"} · parks after ${PREVIEW_IDLE_TIMEOUT_DEFAULT_MINUTES}m idle`
              : "The project defines no servers yet."}>
              <Button variant="primary" leadingIcon="play" disabled={busy !== null || data.recipes.length === 0} onClick={startPreview} data-testid="preview-branch">
                {busy ? "Starting…" : "Preview branch"}
              </Button>
            </FormActions>
          ) : undefined}
        />
        {problem ? <Callout tone="danger">{problem}</Callout> : null}
        {data.recipes.length > 0 ? <ServersRecipesPreview recipes={data.recipes} note="Defined in project settings → Servers." /> : null}
      </ServersPanel>
    );
  }

  const isPreview = run.kind === "preview";
  const owner = run.startedBy ? (people.byId.get(run.startedBy.id) ?? run.startedBy) : null;
  const live = runIsLive(run);
  const setup = data.recipes.filter((r) => r.autostartInPreviews && r.setup).map((r) => r.setup).join(", ");

  const host = run.host ? <> · {run.host}</> : null;
  const started = run.startedAt ? <> · started <Duration ms={Math.max(0, now - Date.parse(run.startedAt))} format="age" tone="muted" /> ago</> : null;
  const detail = isPreview ? (
    <>
      <code>{run.branch}{run.commit ? ` @ ${run.commit.slice(0, 7)}` : ""}</code>
      {host}
      {started}
      {run.parksAfterMinutes ? <> · parks after {run.parksAfterMinutes}m idle</> : null}
    </>
  ) : (
    <>
      <ShortId id={run.luxRunId} />
      {host}
      {started}
      {owner ? <> · for {firstName(owner.name)}</> : null}
    </>
  );

  return (
    <>
      <ServersPanel
        data-testid="servers-panel"
        data-run={run.kind}
        note={isPreview
          ? <>A preview run has no agent. It is parked after {run.parksAfterMinutes ?? PREVIEW_IDLE_TIMEOUT_DEFAULT_MINUTES} minutes without a request; starting a server wakes it.</>
          : <>Servers stop when the run pauses, ends or moves host; they do not restart on their own. Output streams into the run log as <span className="ds-mono">server:&lt;name&gt;</span>.</>}
      >
        <ServersRunLine
          avatar={isPreview || !owner
            ? <AgentAvatar role="system" size="lg" live={isPreview && live} />
            : <PersonAvatar person={owner} size={32} agent="implementer" live={live} title="" />}
          title={run.label}
          status={runMark(run)}
          detail={detail}
          actions={
            <>
              {live && run.terminalUrl ? <TerminalLink href={run.terminalUrl} /> : null}
              {isPreview && taskId ? (
                <Button size="sm" variant="quiet" leadingIcon="stop" disabled={busy !== null} onClick={stopPreview} data-testid="stop-preview">Stop preview</Button>
              ) : live ? (
                <>
                  <Button size="sm" variant="secondary" leadingIcon="play" disabled={busy !== null || !canStartAny(data.servers)} onClick={() => void servers.startAll()} data-testid="start-all">Start all</Button>
                  <Button size="sm" variant="quiet" leadingIcon="stop" disabled={busy !== null || !canStopAny(data.servers)} onClick={() => void servers.stopAll()} data-testid="stop-all">Stop all</Button>
                </>
              ) : null}
              <Button size="sm" variant="quiet" leadingIcon="plus" disabled={!live} onClick={() => setAdding(true)} data-testid="add-server">Add server</Button>
            </>
          }
        />
        {problem ? <Callout tone="danger">{problem}</Callout> : null}
        {data.preview && taskId ? (
          <PreviewAlsoRunning parked={data.preview.state === "paused"} busy={busy !== null} onStop={stopPreview} />
        ) : null}
        {data.moved ? (
          <ServersMoved at={formatTimestamp(data.moved.at, "time-short")} fromHost={data.moved.fromHost} toHost={data.moved.toHost}
            busy={busy !== null} onStartAll={live ? () => void servers.startAll() : undefined} />
        ) : null}
        {isPreview && run.previewStage && run.previewStage !== "ready" ? (
          <PreviewStages stage={run.previewStage} branch={run.branch} setup={setup || undefined}
            elapsed={run.startedAt ? <Duration since={run.startedAt} live tone="muted" /> : undefined} />
        ) : null}
        <ServerList aria-label="Servers">
          {data.servers.map((s) => {
            const words = describeServer(s, now, run);
            const rowBusy = busy === s.name || busy === "*";
            const log = logs[s.name];
            return (
              <ServerRow
                key={s.name}
                name={s.name}
                port={s.port}
                state={words.state}
                stateLabel={words.label}
                detail={words.detail}
                error={s.error}
                url={s.url}
                busy={rowBusy}
                onStart={live && s.command ? () => void servers.start(s.name) : undefined}
                onStop={live ? () => void servers.stop(s.name) : undefined}
                onRestart={live && s.command ? () => void servers.restart(s.name) : undefined}
                menu={<RowMenu size="sm" label={`Actions for ${s.name}`} items={[
                  ...(s.url ? [{ id: "copy", label: "Copy URL", icon: "copy" as const, onSelect: () => void navigator.clipboard?.writeText(s.url!) }] : []),
                  ...(live && s.command && canStop(s) ? [{ id: "restart", label: "Restart", icon: "retry" as const, disabled: rowBusy, onSelect: () => void servers.restart(s.name) }] : []),
                  { kind: "separator" as const },
                  { id: "remove", label: "Remove from this run", tone: "danger" as const, disabled: rowBusy, onSelect: () => setRemoving(s.name) },
                ]} />}
                logs={{
                  open: openLogs.has(s.name),
                  onToggle: () => setOpenLogs((o) => toggled(o, s.name)),
                  lines: log === "loading" ? [] : log ?? [],
                  loading: log === "loading",
                  live: isMoving(s),
                  maxHeight: 240,
                }}
              />
            );
          })}
        </ServerList>
        {data.servers.length === 0 ? (
          <EmptyState compact icon="globe" title="No servers on this run" description={isPreview ? "The preview starts none; add one." : "Add one of the project’s, or a port and command just for this run."} />
        ) : null}
      </ServersPanel>

      {adding ? (
        <AddServerDialog
          recipes={data.recipes}
          present={new Set(data.servers.map((s) => s.name))}
          busy={busy !== null}
          problem={problem}
          onClose={() => {
            setAdding(false);
            servers.clearProblem();
          }}
          onAdd={servers.add}
        />
      ) : null}

      <Dialog
        open={removing !== null}
        onOpenChange={(open) => !open && setRemoving(null)}
        tone="danger"
        size="sm"
        title={`Remove ${removing ?? ""} from this run?`}
        description="It is stopped first. The project's definition, if it has one, stays."
        footer={
          <>
            <Button variant="quiet" onClick={() => setRemoving(null)}>Cancel</Button>
            <Button variant="danger" solid disabled={busy !== null} data-testid="remove-server-confirm" onClick={() => {
              const name = removing!;
              setRemoving(null);
              void servers.remove(name);
            }}>
              Remove
            </Button>
          </>
        }
      />

    </>
  );
});

/** What the Servers tab shows beside its name: the first bad server as a dot, else how many are ready. */
export function serversTabTrailing(data: TaskServers | null): ReactNode {
  if (!data?.run) return null;
  const { bad, ready } = summarizeServers(data.servers);
  if (bad) return <ServerStateDot state={bad.state} label={`${bad.name} ${bad.state}`} />;
  if (ready > 0) return <TabCount>{ready} ready</TabCount>;
  if (data.run.kind === "preview" && data.run.previewStage !== "ready" && runIsLive(data.run)) return <ServerStateDot state="starting" label="Preview starting" />;
  if (data.moved) return <ServerStateDot state="unreachable" label="Stopped when the run moved" />;
  return null;
}
