/**
 * The task overview's servers, beside the pull request: one row per
 * server with its state and URL, so a ready one is a click away; with no
 * run, the way to a branch preview. Preview here opens the same sheet the
 * Servers tab opens.
 */

import { useState } from "react";
import { ServersSummary, ServersSummaryRow, TerminalLink } from "@dude/design-system/components";
import { anyMoving, describeServer, formatTimestamp, useNow } from "@dude/design-system";
import { Button } from "@dude/design-system/primitives";
import type { ApiClient } from "../api/client.ts";
import { runIsLive, type ServersState } from "../hooks/useServers.ts";
import { ServerPreview } from "./ServersSection.tsx";

export function ServersAside({ client, taskId, servers, onAll }: { client: ApiClient; taskId: string; servers: ServersState; onAll: () => void }) {
  const data = servers.data!;
  // The clock the words are told by: ticking while something is moving.
  const now = useNow(anyMoving(data.servers), 30_000);
  const [preview, setPreview] = useState<string | null>(null);
  const run = data.run;

  if (!run) {
    return (
      <ServersSummary
        data-testid="servers-summary"
        empty={servers.problem ?? "No run is serving this branch."}
        actions={
          data.recipes.length > 0 ? (
            <Button size="sm" variant="secondary" leadingIcon="play" disabled={servers.busy !== null} data-testid="preview-branch-aside"
              onClick={() => void servers.act("*", () => client.startPreview(taskId))}>
              Preview branch
            </Button>
          ) : undefined
        }
      />
    );
  }

  const live = runIsLive(run);
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
            {live && run.terminalUrl ? <TerminalLink href={run.terminalUrl}>Terminal</TerminalLink> : null}
          </>
        }
      >
        {data.servers.map((s) => {
          const words = describeServer(s, now, run);
          return (
            <ServersSummaryRow key={s.name} name={s.name} state={words.state} stateLabel={words.label} url={s.url} detail={words.detail}
              onPreview={s.url ? () => setPreview(s.name) : undefined}
              onStart={live && s.command ? () => void servers.start(s.name) : undefined}
              onRestart={live && s.command ? () => void servers.restart(s.name) : undefined} />
          );
        })}
      </ServersSummary>
      {previewed ? <ServerPreview server={previewed} run={run} now={now} servers={servers} onClose={() => setPreview(null)} /> : null}
    </>
  );
}
