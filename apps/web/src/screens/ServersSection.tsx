/**
 * The servers a task or a run has, as one section: the run they live on
 * (the agent's, or a branch preview's), a notice when the run moved host,
 * a preview's stages, the list with each server's log folding open under
 * it, and Preview opening the page in a sheet. The task's Servers tab and
 * the run screen's drawer are the same section at two widths; the drawer
 * passes `compact`.
 */

import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import {
  AgentAvatar,
  Duration,
  PersonAvatar,
  PreviewFrame,
  PreviewScrim,
  PreviewStages,
  ServerList,
  ServerRecipeRow,
  ServerRow,
  ServerStateDot,
  ServersMoved,
  ServersPanel,
  ServersRunLine,
  ShortId,
  StatusMark,
  TerminalLink,
} from "@dude/design-system/components";
import { ALL_STATUSES, canStartAny, canStopAny, describeServer, firstName, formatTimestamp, serverLogLines, summarizeServers, useNow } from "@dude/design-system";
import { Button, Callout, Dialog, EmptyState, FormActions, RowMenu, Spinner, TabCount } from "@dude/design-system/primitives";
import { PREVIEW_IDLE_TIMEOUT_DEFAULT_MINUTES, type RunStatus, type Server, type ServerLogLine, type ServersRun, type TaskServers } from "@dude/domain";
import type { LogLine } from "@dude/design-system/components";
import type { ApiClient } from "../api/client.ts";
import { runIsLive, type ServersState } from "../hooks/useServers.ts";
import { usePeople } from "../people.tsx";
import { usePreviewDocument } from "../preview.tsx";
import { AddServerDialog } from "./AddServerDialog.tsx";

export interface ServersSectionProps {
  client: ApiClient;
  servers: ServersState;
  /** The task, for Preview branch (its branch is what a preview checks out). */
  taskId?: string | undefined;
  /** Narrow (the run screen's drawer): Start all and Stop all move to the drawer's head. */
  compact?: boolean | undefined;
  /** The preview, when the parent lays it out itself (docked beside a conversation). */
  onPreview?: ((name: string | null) => void) | undefined;
  /** Which server's preview is open, when the parent owns it. */
  previewing?: string | null | undefined;
  /** Open the full log elsewhere (the run's events), when there is an elsewhere. */
  onFullLog?: ((name: string) => void) | undefined;
}

/** The run status as StatusMark says it; a preview's own word until its servers are up. */
function runMark(run: ServersRun) {
  if (run.kind === "preview" && run.previewStage && run.previewStage !== "ready" && runIsLive(run)) return <StatusMark status="starting" size="sm" />;
  // dude's status, when the vocabulary has it; a word it lacks is shown as it came.
  const known = (ALL_STATUSES as readonly string[]).includes(run.state);
  return <StatusMark status={known ? (run.state as RunStatus) : "running"} size="sm" label={known ? undefined : run.state} />;
}

export function ServersSection({ client, servers, taskId, compact, onPreview, previewing, onFullLog }: ServersSectionProps) {
  const { data, problem, busy } = servers;
  const people = usePeople();
  const now = useNow(Boolean(data?.servers.some((s) => s.state === "starting" || s.state === "ready")), 30_000);
  const [openLogs, setOpenLogs] = useState<Set<string>>(() => new Set());
  const [logs, setLogs] = useState<Record<string, ServerLogLine[] | "loading">>({});
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<string | null>(null);
  const [ownPreview, setOwnPreview] = useState<string | null>(null);
  const preview = onPreview ? previewing ?? null : ownPreview;
  const setPreview = onPreview ?? setOwnPreview;

  const run = data?.run ?? null;
  const runId = run?.id ?? null;

  // A server's log is read when its row opens, and again on each change
  // while it is open: the log is not on the stream.
  const readLog = useCallback(async (name: string) => {
    if (!runId) return;
    setLogs((l) => (l[name] ? l : { ...l, [name]: "loading" }));
    try {
      const { lines } = await client.serverLog(runId, name);
      setLogs((l) => ({ ...l, [name]: lines }));
    } catch {
      setLogs((l) => ({ ...l, [name]: [] }));
    }
  }, [client, runId]);
  useEffect(() => {
    for (const name of openLogs) void readLog(name);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- re-read on each change of the servers
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

  const toggleLog = (name: string) => setOpenLogs((s) => {
    const next = new Set(s);
    if (next.has(name)) next.delete(name);
    else next.add(name);
    return next;
  });

  const lines = useMemo(() => {
    const out: Record<string, LogLine[]> = {};
    for (const [name, log] of Object.entries(logs)) if (log !== "loading") out[name] = serverLogLines(log);
    return out;
  }, [logs]);

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
        {data.recipes.length > 0 ? (
          <section className="serversRecipes" aria-label="Project servers">
            <h2 className="ds-label">What a preview starts</h2>
            <ServerList>
              {data.recipes.map((r) => (
                <ServerRecipeRow key={r.name} name={r.name} port={r.port} command={r.command} autostart={r.autostartInPreviews} />
              ))}
            </ServerList>
            <p className="formNote">Defined in project settings → Servers.</p>
          </section>
        ) : null}
      </ServersPanel>
    );
  }

  const isPreview = run.kind === "preview";
  const owner = run.startedBy ? (people.byId.get(run.startedBy.id) ?? run.startedBy) : null;
  const live = runIsLive(run);
  const previewed = preview ? data.servers.find((s) => s.name === preview) ?? null : null;
  const previewedWords = previewed ? describeServer(previewed, now, { runKind: run.kind, previewStage: run.previewStage }) : null;
  const setup = data.recipes.filter((r) => r.autostartInPreviews && r.setup).map((r) => r.setup).join(", ");

  const detail = isPreview ? (
    <>
      <code>{run.branch}{run.commit ? ` @ ${run.commit.slice(0, 7)}` : ""}</code>
      {run.host ? <> · {run.host}</> : null}
      {run.startedAt ? <> · started <Duration ms={Math.max(0, now - Date.parse(run.startedAt))} format="age" tone="muted" /> ago</> : null}
      {run.parksAfterMinutes ? <> · parks after {run.parksAfterMinutes}m idle</> : null}
    </>
  ) : (
    <>
      <ShortId id={run.luxRunId} />
      {run.host ? <> · {run.host}</> : null}
      {run.startedAt ? <> · started <Duration ms={Math.max(0, now - Date.parse(run.startedAt))} format="age" tone="muted" /> ago</> : null}
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
              {live ? <TerminalLink href={run.terminalUrl} /> : null}
              {isPreview && taskId ? (
                <Button size="sm" variant="quiet" leadingIcon="stop" disabled={busy !== null} onClick={stopPreview} data-testid="stop-preview">Stop preview</Button>
              ) : (
                <>
                  {compact ? null : <Button size="sm" variant="secondary" leadingIcon="play" disabled={busy !== null || !live || !canStartAny(data.servers)} onClick={() => void servers.startAll()} data-testid="start-all">Start all</Button>}
                  {compact ? null : <Button size="sm" variant="quiet" leadingIcon="stop" disabled={busy !== null || !canStopAny(data.servers)} onClick={() => void servers.stopAll()} data-testid="stop-all">Stop all</Button>}
                </>
              )}
              <Button size="sm" variant="quiet" leadingIcon="plus" disabled={!live} onClick={() => setAdding(true)} data-testid="add-server">Add server</Button>
            </>
          }
        />
        {problem ? <Callout tone="danger">{problem}</Callout> : null}
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
            const words = describeServer(s, now, { runKind: run.kind, previewStage: run.previewStage });
            const rowBusy = busy === s.name || busy === "*";
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
                onPreview={s.url ? () => setPreview(s.name) : undefined}
                onStart={live && s.command ? () => void servers.start(s.name) : undefined}
                onStop={live ? () => void servers.stop(s.name) : undefined}
                onRestart={live && s.command ? () => void servers.restart(s.name) : undefined}
                menu={<RowMenu size="sm" label={`Actions for ${s.name}`} items={[
                  ...(s.url ? [{ id: "copy", label: "Copy URL", icon: "copy" as const, onSelect: () => void navigator.clipboard?.writeText(s.url!) }] : []),
                  ...(live && s.command && s.state !== "stopped" && s.state !== "exited" ? [{ id: "restart", label: "Restart", icon: "retry" as const, disabled: rowBusy, onSelect: () => void servers.restart(s.name) }] : []),
                  { kind: "separator" as const },
                  { id: "remove", label: "Remove from this run", tone: "danger" as const, disabled: rowBusy, onSelect: () => setRemoving(s.name) },
                ]} />}
                logs={{
                  open: openLogs.has(s.name),
                  onToggle: () => toggleLog(s.name),
                  lines: lines[s.name] ?? [],
                  loading: logs[s.name] === "loading",
                  live: s.state === "ready" || s.state === "starting",
                  maxHeight: compact ? 200 : 240,
                  onFull: onFullLog ? () => onFullLog(s.name) : undefined,
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

      {!onPreview && previewed && previewedWords && previewed.url ? (
        <>
          <PreviewScrim onClose={() => setPreview(null)} />
          <ServerPreview server={previewed} words={previewedWords} you={people.me?.email ?? null} onClose={() => setPreview(null)}
            onLogs={() => {
              setPreview(null);
              setOpenLogs((s) => new Set([...s, previewed.name]));
            }}
            onRestart={live && previewed.command ? () => void servers.restart(previewed.name) : undefined} />
        </>
      ) : null}
    </>
  );
}

/** The preview sheet or docked frame for one server, with the words the row uses. */
export function ServerPreview({ server, words, you, docked, onClose, onLogs, onRestart }: {
  server: Server;
  words: ReturnType<typeof describeServer>;
  you: string | null;
  docked?: boolean | undefined;
  onClose: () => void;
  onLogs?: (() => void) | undefined;
  onRestart?: (() => void) | undefined;
}) {
  const srcDoc = usePreviewDocument();
  return (
    <PreviewFrame
      name={server.name}
      state={words.state}
      stateLabel={words.label}
      url={server.url!}
      srcDoc={srcDoc}
      docked={docked}
      access={you ? `Signed in as ${you}` : undefined}
      onClose={onClose}
      onLogs={onLogs}
      onRestart={onRestart}
      foot={<span>{server.name} · {words.detail}</span>}
      footNote="Opens with your own sign-in; the agent cannot open this."
    />
  );
}

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
