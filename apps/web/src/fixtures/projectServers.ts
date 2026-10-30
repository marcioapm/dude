/**
 * The project Servers page's world, as the v4 mockup draws it: a branch
 * preview connected to this tab with two servers, two task Runs, and a
 * branch preview starting; each server's feedback, everyone's, with a
 * draft of your own on a task's server and none of anyone else's.
 * `?servers=none|failed|loading|live` turns the read empty, failing,
 * never answering, or Kai submitting a comment a moment after the page opens.
 */

import type { FeedbackItem, PreviewFeedbackItem, RunningRun } from "@dude/domain";
import { PROJECT, TASK_ID, YOU } from "./data.ts";

const ago = (min: number) => new Date(Date.now() - min * 60_000).toISOString();
const person = (id: string, name: string) => ({ id, name });
const ANA = person("u_ana", "Ana Ribeiro");
const TOM = person("u_tom", "Tom Okafor");
const LIN = person("u_lin", "Lin Zhao");
const KAI = person("u_kai", "Kai Nakamura");
const ME = person(YOU, "Márcio Martins");

export const CHECKOUT_WEB = "https://web-pv-7h2k.preview.acme.dev";
export const CHECKOUT_ADMIN = "https://admin-pv-7h2k.preview.acme.dev";
export const TASK_WEB = "https://web-r9x1.preview.acme.dev";

const server = (name: string, port: number, url: string, state: "ready" | "starting" | "unreachable", sinceMin: number,
    open: number, resolved: number, authors: number) => ({
  name, port, url, state, since: ago(sinceMin), readySince: state === "ready" ? ago(sinceMin) : null,
  commenting: { available: true as const }, feedback: { open, resolved, authors },
});

export function runningRuns(): RunningRun[] {
  return [
    { id: "run_pv_checkout", kind: "preview", task: null, branch: "feat/checkout-polish", state: "running", startedAt: ago(52), startedBy: ME,
      servers: [server("web", 5173, CHECKOUT_WEB, "ready", 48, 2, 1, 2), server("admin", 5174, CHECKOUT_ADMIN, "ready", 48, 0, 1, 1)] },
    { id: "run_web142", kind: "task", task: { id: TASK_ID, key: "WEB-142", title: "Streaming Markdown: treat open fences as open" },
      branch: "dude/WEB-142-streaming-markdown", state: "running", startedAt: ago(130), startedBy: LIN,
      servers: [server("web", 5173, TASK_WEB, "ready", 120, 2, 2, 3)] },
    { id: "run_pv_returns", kind: "preview", task: null, branch: "docs/returns-page", state: "running", startedAt: ago(18), startedBy: ANA,
      servers: [server("web", 5173, "https://web-pv-3m8q.preview.acme.dev", "starting", 0.7, 0, 0, 0)] },
    { id: "run_web151", kind: "task", task: { id: "task_wc151", key: "WEB-151", title: "Tool call card: diff hands off to DiffView" },
      branch: "dude/WEB-151-tool-call-diff", state: "running", startedAt: ago(300), startedBy: LIN,
      servers: [server("web", 5173, "https://web-k2p7.preview.acme.dev", "ready", 300, 0, 1, 1),
        server("storybook", 6006, "https://storybook-k2p7.preview.acme.dev", "unreachable", 12, 0, 0, 0)] },
  ];
}

const pf = (id: string, author: { id: string; name: string }, min: number, url: string, selector: string, body: string,
    resolved?: { by: { id: string; name: string }; note: string }): PreviewFeedbackItem => ({
  id, projectId: PROJECT.id, previewRunId: "run_pv_checkout", serverName: new URL(url).hostname.startsWith("admin") ? "admin" : "web",
  author, body, anchor: { kind: "preview", url, selector, viewport: { width: 1440, height: 900 } }, captureSha: null,
  status: resolved ? "resolved" : "open", createdAt: ago(min), resolvedAt: resolved ? ago(min - 6) : null, resolvedBy: resolved?.by.id ?? null,
  resolutionNote: resolved?.note ?? "", history: resolved ? [{ status: "resolved", personId: resolved.by.id, note: resolved.note, at: ago(min - 6) }] : [],
});

export function previewFeedbackFor(runId: string, server: string): PreviewFeedbackItem[] {
  if (runId !== "run_pv_checkout") return [];
  const all = [
    pf("fbi_pf_1", ANA, 4, `${CHECKOUT_WEB}/checkout`, "#pay-button", "The payment button is too close to the order total on a small screen."),
    pf("fbi_pf_2", TOM, 31, `${CHECKOUT_WEB}/checkout`, "#delivery-note", "Make the delivery estimate easier to read.",
      { by: ME, note: "Increased the contrast and spacing around the delivery estimate." }),
    pf("fbi_pf_3", ANA, 27, `${CHECKOUT_WEB}/cart`, "#promo-code", "Keep the promo code field visible when an invalid code is entered."),
    pf("fbi_pf_4", KAI, 40, `${CHECKOUT_ADMIN}/orders`, "#status-filter", "Status filter resets after opening an order.",
      { by: ME, note: "The filter is kept in the URL now." }),
  ];
  // Newest first, as the page lists them.
  return all.filter((i) => i.serverName === server).sort((a, b) => b.createdAt.localeCompare(a.createdAt));
}

/** What Kai submits from his window while the page is open (`?servers=live`). */
export const KAI_SUBMIT = (): PreviewFeedbackItem =>
  pf("fbi_pf_new", KAI, 0, `${CHECKOUT_WEB}/checkout`, "#order-total", "Total jumps when the delivery estimate loads; reserve its height.");

const tf = (id: string, author: { id: string; name: string }, min: number, status: FeedbackItem["status"], selector: string, body: string,
    reply?: string, path = "/chat"): FeedbackItem => ({
  id, taskId: TASK_ID, attempt: 1, batchId: status === "draft" ? null : `fbb_${id}`, author, body,
  anchor: { kind: "preview", url: `${TASK_WEB}${path}`, selector, viewport: { width: 1440, height: 900 } },
  status, deliveredRunId: null, resolvedCommit: status === "resolved" ? "9c41e2ab" : null, resolutionPending: false, outdated: false,
  routedTo: status === "delivered" ? "steer" : status === "draft" ? null : "workflow",
  submittedAt: status === "draft" ? null : ago(min), createdAt: ago(min), updatedAt: ago(min),
  history: reply ? [{ id: `${id}_r`, kind: status === "replied" ? "reply" : "resolve", body: reply, commit: status === "resolved" ? "9c41e2ab" : null,
    personId: status === "replied" ? YOU : null, runId: status === "replied" ? null : "run_fix", createdAt: ago(min - 6) }] : [],
});

/** WEB-142's feedback on its web server: everyone's submitted items and your own draft (nobody else's). */
export function taskServerFeedback(): FeedbackItem[] {
  return [
    tf("fbi_tf_1", ME, 2, "draft", ".composer textarea", "Placeholder disappears too early while typing a fence."),
    tf("fbi_tf_2", LIN, 25, "delivered", ".message pre", "An open fence at the end of a message still renders as text."),
    tf("fbi_tf_3", ANA, 9, "open", ".message .copy", "Copy button sits over the last line of long code."),
    tf("fbi_tf_4", LIN, 70, "resolved", ".message pre", "Code inside a list item loses its indent.", "Indent kept for fences inside list items."),
    tf("fbi_tf_5", TOM, 95, "replied", ".message", "Can we syntax-highlight while streaming?",
      "Not in this task: highlighting waits for the fence to close, by design."),
  ];
}
