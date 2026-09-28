/**
 * A session's Changes: its agent's checkout against the commit it started
 * from, uncommitted work included, as it changes.
 *
 * The stream carries a summary of each new diff (run.diff.updated: paths,
 * counts and a checksum, never the lines); a summary whose checksum is not
 * the one shown is the cue to fetch the diff. Opened on a finished session,
 * it shows the last diff — the final one its container left when it
 * stopped, or the last read while it worked.
 */

import { useEffect, useMemo, useRef, useState } from "react";
import { LiveDiff } from "@dude/design-system/components";
import { Callout } from "@dude/design-system/primitives";
import { EventTypes } from "@dude/domain";
import type { PersistedEvent } from "@dude/domain";
import type { ApiClient, RunDiff } from "../api/client.ts";

export function ChangesPanel({ client, runId, events, live }: {
  client: ApiClient;
  runId: string;
  /** The Run's event stream, already open for its conversation. */
  events: readonly PersistedEvent[];
  /** Its agent is at work: the diff may still change. */
  live: boolean;
}) {
  const [diff, setDiff] = useState<RunDiff | null>(null);
  const [problem, setProblem] = useState<string | null>(null);

  // The newest summary's checksum: a different one from what is shown
  // means there is a newer diff to fetch.
  const latest = useMemo(() => {
    for (let i = events.length - 1; i >= 0; i--) {
      const e = events[i]!;
      if (e.eventType === EventTypes.RunDiffUpdated) return String(e.payload.checksum ?? e.eventId);
    }
    return "";
  }, [events]);

  const shown = useRef<string | null>(null);
  useEffect(() => {
    if (shown.current !== null && shown.current === latest) return;
    let current = true;
    void client.runDiff(runId).then(
      (d) => {
        if (!current) return;
        shown.current = d.checksum || latest;
        setDiff(d);
        setProblem(null);
      },
      (err: unknown) => current && setProblem(err instanceof Error ? err.message : String(err)),
    );
    return () => {
      current = false;
    };
  }, [client, runId, latest]);

  // What the agent last wrote: the newest file-changing tool it called.
  const lastChange = useMemo(() => {
    for (let i = events.length - 1; i >= 0; i--) {
      const e = events[i]!;
      if (e.eventType !== EventTypes.ToolCalled) continue;
      const tool = String(e.payload.tool ?? "");
      if (!/^(edit|write|patch|multiedit|apply_patch)$/i.test(tool)) continue;
      const input = e.payload.input as Record<string, unknown> | undefined;
      const path = String(input?.filePath ?? input?.path ?? input?.file_path ?? "");
      return `${tool.charAt(0).toUpperCase()}${tool.slice(1)}${path ? ` ${path.split("/").pop()}` : ""}`;
    }
    return null;
  }, [events]);

  if (problem) return <Callout tone="danger">{problem}</Callout>;
  if (!diff) return null;
  return (
    <LiveDiff
      data-testid="changes"
      files={diff.files}
      base={diff.base}
      live={live}
      lastChange={live && lastChange ? lastChange : undefined}
      emptyMessage={live ? "The agent has not changed anything yet." : "This session changed nothing."}
    />
  );
}
