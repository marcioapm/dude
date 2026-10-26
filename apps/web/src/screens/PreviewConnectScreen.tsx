/**
 * Where a preview's Connect to dude lands (`#/preview-connect`), in a tab
 * the preview opened with an opener. It waits for the preview's hello (the
 * overlay says one every two seconds), asks which of the person's tasks
 * has a live preview at the origin the browser gives that message, and goes
 * to that task in this same tab, so window.opener is still the preview and
 * the task page adopts it. Nothing here answers the preview: the task page does.
 */

import { useEffect, useRef, useState } from "react";
import { FrontDoor, FrontDoorMessage } from "@dude/design-system/components";
import { Card, CardBody, Spinner } from "@dude/design-system/primitives";
import { OVERLAY_PROTOCOL } from "@dude/domain";
import { ApiError, type ApiClient, type PreviewServerAt } from "../api/client.ts";
import { DudeMark } from "../DudeMark.tsx";
import { errorText } from "../hooks/useSave.tsx";
import { httpsOriginOf, markPreviewArrival } from "../previewComment.ts";

/** How long the preview has to say hello before this page says it didn't. */
export const PREVIEW_HELLO_WAIT_MS = 10_000;

type Step =
  | { kind: "no-opener" }
  | { kind: "waiting" }
  | { kind: "looking"; origin: string }
  | { kind: "silent" }
  | { kind: "none"; origin: string }
  | { kind: "failed"; why: string };

/** This window's opener, or null when there is none or it can't be read. */
function openerOf(win: Window): Window | null {
  try {
    return (win.opener as Window | null) ?? null;
  } catch {
    return null;
  }
}

export function PreviewConnectScreen({ client, onFound, win = window, waitMs = PREVIEW_HELLO_WAIT_MS }: {
  client: ApiClient;
  /** Goes to the task, in this tab. */
  onFound: (found: PreviewServerAt) => void;
  win?: Window;
  waitMs?: number;
}) {
  const [step, setStep] = useState<Step>(() => (openerOf(win) ? { kind: "waiting" } : { kind: "no-opener" }));
  const onFoundRef = useRef(onFound);
  onFoundRef.current = onFound;
  useEffect(() => {
    const opener = openerOf(win);
    if (!opener) return;
    let done = false;
    // False once this page is left: a lookup landing later moves nothing and marks nothing.
    let alive = true;
    const timer = win.setTimeout(() => {
      if (!done) setStep({ kind: "silent" });
    }, waitMs);
    const onMessage = (e: MessageEvent) => {
      if (done || e.source !== opener) return;
      const m = e.data as { type?: unknown; v?: unknown; url?: unknown } | null;
      if (!m || typeof m !== "object" || m.type !== "hello" || m.v !== OVERLAY_PROTOCOL || typeof m.url !== "string") return;
      // The origin is the browser's word for who sent it; the page's own URL must agree.
      const origin = httpsOriginOf(e.origin);
      if (origin === null || origin !== e.origin || httpsOriginOf(m.url) !== origin) return;
      done = true;
      win.clearTimeout(timer);
      setStep({ kind: "looking", origin });
      client.previewServerAt(origin).then((found) => {
        if (!alive) return;
        markPreviewArrival(win, found.taskId);
        onFoundRef.current(found);
      }, (err: unknown) => {
        if (!alive) return;
        setStep(err instanceof ApiError && err.status === 404 ? { kind: "none", origin } : { kind: "failed", why: errorText(err) });
      });
    };
    win.addEventListener("message", onMessage);
    return () => {
      done = true;
      alive = false;
      win.clearTimeout(timer);
      win.removeEventListener("message", onMessage);
    };
  }, [client, win, waitMs]);

  const words: Record<Step["kind"], { title: string; line: string }> = {
    "no-opener": { title: "Open this from a preview's Connect to dude button", line: "This page connects a preview's comment tool to dude: it works only in the tab a preview's Connect to dude opens." },
    waiting: { title: "Connecting to the preview…", line: "Waiting for the preview's page to say which preview it is." },
    looking: { title: "Finding the preview's task…", line: step.kind === "looking" ? step.origin : "" },
    silent: { title: "The preview didn't answer", line: "Its page may have been closed or reloaded without the comment tool. Press Connect to dude on the preview again." },
    none: { title: `No task of yours has a preview at ${step.kind === "none" ? step.origin : ""}`, line: "The preview may have ended, or belong to an organisation you are not in." },
    failed: { title: "dude couldn't look the preview up", line: step.kind === "failed" ? step.why : "" },
  };
  const w = words[step.kind];
  const busy = step.kind === "waiting" || step.kind === "looking";
  return (
    <FrontDoor variant="inside" mark={<DudeMark size={56} />} data-testid="preview-connect" data-step={step.kind}>
      <Card>
        <CardBody>
          <FrontDoorMessage lead={busy ? <Spinner size={20} /> : undefined} icon={busy ? undefined : "globe"} title={w.title} line={w.line} />
        </CardBody>
      </Card>
    </FrontDoor>
  );
}
