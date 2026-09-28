/**
 * The task overview's servers, beside the pull request: one row per
 * server with its state and URL, so a ready one is a click away; with no
 * run, the way to a branch preview. Preview here opens the same sheet the
 * Servers tab opens.
 */

import { useState } from "react";
import { PreviewScrim, ServersSummary, ServersSummaryRow, TerminalLink } from "@dude/design-system/components";
import { describeServer, formatTimestamp, useNow } from "@dude/design-system";
import { Button } from "@dude/design-system/primitives";
import { TERMINAL_RUN_STATUSES, type RunStatus } from "@dude/domain";
import type { ApiClient } from "../api/client.ts";
import type { ServersState } from "../hooks/useServers.ts";
import { errorText } from "../hooks/useSave.tsx";
import { usePeople } from "../people.tsx";
import { ServerPreview } from "./ServersSection.tsx";

export function ServersAside({ client, taskId, servers, onAll }: { client: ApiClient; taskId: string; servers: ServersState; onAll: () => void }) {
  const data = servers.data!;
  const people = usePeople();
  const now = useNow(false);
  const [preview, setPreview] = useState<string | null>(null);
  const [starting, setStarting] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const run = data.run;

  if (!run) {
    return (
      <ServersSummary
        data-testid="servers-summary"
        empty={problem ?? "No run is serving this branch."}
        actions={
          data.recipes.length > 0 ? (
            <Button size="sm" variant="secondary" leadingIcon="play" disabled={starting} data-testid="preview-branch-aside" onClick={() => {
              setStarting(true);
              setProblem(null);
              client.startPreview(taskId).then(() => servers.reload(), (err: unknown) => setProblem(errorText(err))).finally(() => setStarting(false));
            }}>
              Preview branch
            </Button>
          ) : undefined
        }
      />
    );
  }

  const live = !TERMINAL_RUN_STATUSES.includes((run.state || "running") as RunStatus);
  const previewed = preview ? data.servers.find((s) => s.name === preview) ?? null : null;
  return (
    <>
      <ServersSummary
        data-testid="servers-summary"
        where={run.kind === "preview" ? "on the branch preview" : `on the ${run.label.toLowerCase()}`}
        notice={data.moved ? `Stopped when the run moved host at ${formatTimestamp(data.moved.at, "time-short")}.` : servers.problem ?? undefined}
        actions={
          <>
            <Button size="sm" variant="quiet" trailingIcon="arrow-right" onClick={onAll} data-testid="servers-all">All servers</Button>
            {live ? <TerminalLink href={run.terminalUrl}>Terminal</TerminalLink> : null}
          </>
        }
      >
        {data.servers.map((s) => {
          const words = describeServer(s, now, { runKind: run.kind, previewStage: run.previewStage });
          return (
            <ServersSummaryRow key={s.name} name={s.name} state={words.state} stateLabel={words.label} url={s.url} detail={words.detail}
              onPreview={s.url ? () => setPreview(s.name) : undefined}
              onStart={live && s.command ? () => void servers.start(s.name) : undefined} />
          );
        })}
      </ServersSummary>
      {previewed?.url ? (
        <>
          <PreviewScrim onClose={() => setPreview(null)} />
          <ServerPreview client={client} server={previewed} words={describeServer(previewed, now, { runKind: run.kind, previewStage: run.previewStage })}
            you={people.me?.email ?? null} onClose={() => setPreview(null)}
            onRestart={live && previewed.command ? () => void servers.restart(previewed.name) : undefined} />
        </>
      ) : null}
    </>
  );
}
