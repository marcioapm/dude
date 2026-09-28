/**
 * A session's Changes: its agent's checkout against the commit it started
 * from, uncommitted work included, as it changes.
 *
 * The stream carries a summary of each new diff (run.diff.updated: paths,
 * counts and a checksum, never the lines); a summary whose checksum is not
 * the one shown is the cue to fetch the diff. Opened on a finished session,
 * it shows the last diff — the final one its container left when it
 * stopped, or the last read while it worked.
 *
 * A file opens in the viewer: its diff alone, side by side, over the page.
 * dude keeps a Run's diff, not its files, so that is what there is to show.
 */

import { useEffect, useMemo, useRef, useState } from "react";
import { AgentAvatar, LiveDiff, summarizeToolArgs } from "@dude/design-system/components";
import { Callout, Dialog } from "@dude/design-system/primitives";
import type { AgentRole } from "@dude/domain";
import { EventTypes } from "@dude/domain";
import type { PersistedEvent } from "@dude/domain";
import type { ApiClient, RunDiff } from "../api/client.ts";
import { errorText } from "../hooks/useSave.tsx";

const EDIT_TOOLS = /^(edit|write|patch|multiedit|apply_patch)$/i;

export function ChangesPanel({ client, runId, role, events, checksum: latest, live, focus }: {
  client: ApiClient;
  runId: string;
  /** The agent's, for its face beside what it last wrote. */
  role: AgentRole;
  /** The Run's event stream, already open for its conversation. */
  events: readonly PersistedEvent[];
  /** The newest run.diff.updated summary's: another than shown means a newer diff to fetch. */
  checksum: string;
  /** Its agent is at work: the diff may still change. */
  live: boolean;
  /** A file picked elsewhere (the session's rail), to show alone. */
  focus?: { path: string } | null;
}) {
  const [diff, setDiff] = useState<RunDiff | null>(null);
  const [viewing, setViewing] = useState<string | null>(null);
  // The file in the viewer, as one list: the same list while the file is
  // unchanged, so the viewer's diff does not re-read it on every event.
  const openedFiles = useMemo(() => {
    const f = viewing ? diff?.files.find((x) => x.path === viewing) : undefined;
    return f ? [f] : null;
  }, [diff, viewing]);
  // A file that left the diff closes its viewer for good: it does not come back by itself.
  useEffect(() => {
    if (viewing && diff && !diff.files.some((f) => f.path === viewing)) setViewing(null);
  }, [diff, viewing]);
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
    return (
      <>
        <AgentAvatar role={role} size="xs" live />
        {tool.charAt(0).toUpperCase()}{tool.slice(1)}
        {path ? <code>{path.split("/").pop()}</code> : null}
      </>
    );
  }, [events, role]);

  if (problem) return <Callout tone="danger">{problem}</Callout>;
  if (!diff) return null;
  return (
    <>
      <LiveDiff
        data-testid="changes"
        className="runChanges"
        files={diff.files}
        base={diff.base}
        live={live}
        focus={focus}
        onOpenFile={setViewing}
        lastChange={live && lastChange ? lastChange : undefined}
        emptyMessage={live ? "The agent has not changed anything yet." : "This session changed nothing."}
      />
      {openedFiles ? (
        <Dialog open size="xl" onOpenChange={(o) => !o && setViewing(null)} title={<code>{openedFiles[0]!.path}</code>}>
          <div className="diffViewer" data-testid="diff-viewer">
            <LiveDiff files={openedFiles} base={diff.base} fileList={false} defaultView="split" />
          </div>
        </Dialog>
      ) : null}
    </>
  );
}
