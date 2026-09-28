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
import { LiveDiff, summarizeToolArgs } from "@dude/design-system/components";
import { Callout } from "@dude/design-system/primitives";
import { EventTypes } from "@dude/domain";
import type { PersistedEvent } from "@dude/domain";
import type { ApiClient, RunDiff } from "../api/client.ts";
import { errorText } from "../hooks/useSave.tsx";

const EDIT_TOOLS = /^(edit|write|patch|multiedit|apply_patch)$/i;

export function ChangesPanel({ client, runId, events, checksum: latest, live }: {
  client: ApiClient;
  runId: string;
  /** The Run's event stream, already open for its conversation. */
  events: readonly PersistedEvent[];
  /** The newest run.diff.updated summary's: another than shown means a newer diff to fetch. */
  checksum: string;
  /** Its agent is at work: the diff may still change. */
  live: boolean;
}) {
  const [diff, setDiff] = useState<RunDiff | null>(null);
  const [problem, setProblem] = useState<string | null>(null);

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
      (err: unknown) => current && setProblem(errorText(err)),
    );
    return () => {
      current = false;
    };
  }, [client, runId, latest]);

  // What the agent last wrote: the newest file-changing tool it called
  // (the tools the orchestrator reads the diff after: editTools, livediff.go).
  const lastChange = useMemo(() => {
    const e = events.findLast((x) => x.eventType === EventTypes.ToolCalled && EDIT_TOOLS.test(String(x.payload.tool ?? "")));
    if (!e) return null;
    const tool = String(e.payload.tool);
    const path = summarizeToolArgs(e.payload.input);
    return `${tool.charAt(0).toUpperCase()}${tool.slice(1)}${path ? ` ${path.split("/").pop()}` : ""}`;
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
