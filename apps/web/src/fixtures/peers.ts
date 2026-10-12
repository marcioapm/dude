/**
 * Agents coordinating on the fixture epic (`dude.fixtures.run` = peers):
 * WC-214's implementer asked WC-212's for the totals hook's name and had
 * its reply, pinned the contract as a note, then asked WC-212 again and
 * waits on it; WC-215's implementer asked WC-214 something it has read.
 * A conductor on WC-214 was briefed with a digest of what happened. The
 * epic's notes, threads and peers say the same as the Runs' ledgers.
 */

import { MIN, iso } from "@dude/design-system/fixtures/servers";
import { EventTypes, type EpicNote, type EpicNotes, type EpicPeers, type EpicThreads, type PeerMessageView, type PersistedEvent, type Run, type TaskRef } from "@dude/domain";
import { EPIC, RUN_ID, TASK_ID, YOU, event, run } from "./data.ts";

export const CONDUCTOR_ID = "run_01j9x6c0nd8e4t1";

const ref = (key: string, title: string, taskId: string): TaskRef => ({ taskId, key, title });
const WC211 = ref("WC-211", "Cart summary: show tax breakdown", "t_211");
const WC212 = ref("WC-212", "Address form: autocomplete with Places", "t_212");
const WC214 = ref("WC-214", "Checkout v2: split payment step", TASK_ID);
const WC215 = ref("WC-215", "Payment step: Apple Pay fallback", "t_215");
const WC216 = ref("WC-216", "Order confirmation e-mail template", "t_216");
const WC217 = ref("WC-217", "Analytics: checkout funnel events", "t_217");

const message = (id: string, from: TaskRef, to: TaskRef, text: string, sentMin: number, readMin: number | null, more: Partial<PeerMessageView> = {}): PeerMessageView => ({
  id, from, to, fromRunId: from === WC214 ? RUN_ID : `run_${from.key}`, fromRole: "implementer", fromPhase: "implement", replyTo: null, text,
  sentAt: iso(sentMin * MIN), readAt: readMin === null ? null : iso(readMin * MIN), deliveredRunId: readMin === null ? null : `run_${to.key}`,
  briefedRunId: null, lands: "next_step", ...more,
});

const M_ASK = message("pm_01", WC214, WC212, "Which hook gives the totals once the billing country is known: `useTotals` or `useCartTotals`?", 33, 32.6);
const M_REPLY = message("pm_02", WC212, WC214, "`useTotals(country)`. `useCartTotals` is the old one and goes when checkout_v2 ships.", 31, 30.8, { replyTo: "pm_01" });
const M_APPLE = message("pm_03", WC215, WC214, "Does the payment step keep its own submit button, or does the footer own it? Apple Pay needs to replace it.", 20, 19.7);
const M_ANSWER = message("pm_04", WC214, WC215, "The footer owns it: `CheckoutFooter` takes a `primary` slot. Put the Apple Pay button there.", 19.5, 19.2, { replyTo: "pm_03" });
const M_WAIT = message("pm_05", WC214, WC212, "Does the address form still write `billing.country` on blur, or only on submit? VAT must update as it changes.", 0.2, null);

const thread = (id: string, pair: [TaskRef, TaskRef], messages: PeerMessageView[], state: EpicThreads["threads"][number]["state"], wait: EpicThreads["threads"][number]["wait"] = null) =>
  ({ id, pair, messages, state, wait, lastAt: messages.at(-1)!.sentAt });

export const PEER_THREADS: EpicThreads = {
  epicId: EPIC.id,
  waiting: 1,
  threads: [
    thread("pm_05", [WC214, WC212], [M_WAIT], "waiting",
      { runId: RUN_ID, messageId: "pm_05", on: WC212, how: "wait", since: iso(0.15 * MIN), parksAt: iso(-9.85 * MIN), closedAt: null, closedBy: null }),
    thread("pm_03", [WC215, WC214], [M_APPLE, M_ANSWER], "answered"),
    thread("pm_01", [WC214, WC212], [M_ASK, M_REPLY], "answered",
      { runId: RUN_ID, messageId: "pm_01", on: WC212, how: "wait", since: iso(32.9 * MIN), parksAt: null, closedAt: iso(30.8 * MIN), closedBy: "reply" }),
  ],
};

const agent = (task: TaskRef, runId: string) => ({ kind: "agent" as const, label: `${task.key}'s implementer`, runId, task, personId: null, fromSessionId: null });
const person = { kind: "person" as const, label: "Márcio Martins", runId: null, task: null, personId: YOU, fromSessionId: null };
const note = (n: number, text: string, by: EpicNote["by"], atMin: number, more: Partial<EpicNote> = {}): EpicNote =>
  ({ id: `en_0${n}`, n, text, by, createdAt: iso(atMin * MIN), replaces: null, replacedBy: null, retired: null, readBy: 3, ...more });

export const PEER_NOTES: EpicNotes = {
  epicId: EPIC.id,
  limit: 10,
  live: 3,
  current: [
    note(2, "**Feature flag.** Everything new in checkout goes behind `checkout_v2`; the old flow stays reachable for a week after launch.", person, 26 * 60),
    note(4, "**Totals.** Read them with `useTotals(country)`; VAT follows the billing country. `useCartTotals` is going away.", agent(WC214, RUN_ID), 30, { replaces: 3 }),
  ],
  history: [
    note(3, "**Totals.** Read them with `useCartTotals()`.", agent(WC212, "run_WC-212"), 90, { replacedBy: { n: 4, at: iso(30 * MIN) }, readBy: 4 }),
    note(1, "**Design.** The checkout's Figma is the source of truth for spacing.", person, 3 * 24 * 60,
      { retired: { by: { personId: YOU, name: "Márcio Martins" }, at: iso(20 * 60 * MIN) }, readBy: 2 }),
  ],
};

export const PEER_STATES: EpicPeers = {
  epicId: EPIC.id,
  tasks: [
    { ...WC211, status: "done", phase: null, role: null, run: "none", runId: null, waitingOn: null, inbox: 0 },
    { ...WC212, status: "review", phase: "fix", role: "implementer", run: "live", runId: "run_WC-212", waitingOn: null, inbox: 1 },
    { ...WC214, status: "running", phase: "implement", role: "implementer", run: "live", runId: RUN_ID,
      waitingOn: { task: WC212, messageId: "pm_05", how: "wait", since: iso(0.15 * MIN), parksAt: iso(-9.85 * MIN) }, inbox: 0 },
    { ...WC215, status: "awaiting_input", phase: "implement", role: "implementer", run: "parked", runId: "run_WC-215", waitingOn: null, inbox: 0 },
    { ...WC216, status: "queued", phase: null, role: null, run: "none", runId: null, waitingOn: null, inbox: 0 },
    { ...WC217, status: "received", phase: null, role: null, run: "none", runId: null, waitingOn: null, inbox: 0 },
  ],
};

const dude = { type: "system" as const, id: "dude" };
const json = (v: unknown) => ({ head: JSON.stringify(v) });
const peerList = {
  tasks: PEER_STATES.tasks.filter((t) => t.status !== "done" && t.status !== "received").map((t) => ({
    key: t.key, title: t.title, status: t.status, phase: t.phase, role: t.role, run: t.run, self: t.key === WC214.key,
    heads: t.key === WC212.key ? [{ repo: "web-console", branch: "dude/t_212/address-autocomplete", head: "5c1e9a0b7d" }] : [],
    files: t.key === WC212.key ? [{ path: "apps/web/src/checkout/AddressForm.tsx", additions: 142, deletions: 38 }] : [],
    ...(t.key === WC212.key ? { lastSaid: "Moving the country field's write to onBlur." } : {}),
    messagesForYou: 0,
  })),
};

/**
 * WC-214's implementer's peer events, after its ledger's last (the checkout
 * tests it was running): what it read and told, then its open wait.
 */
export function implementerPeerEvents(): PersistedEvent[] {
  return [
    event(33 * MIN, EventTypes.AgentMessageSent, { message: "pm_01", to: "WC-212", reply_to: null, lands: "next_step", text: M_ASK.text }),
    event(32.9 * MIN, EventTypes.AgentPeerWaiting, { message: "pm_01", on: "WC-212", how: "wait", parks_at: iso(22.9 * MIN) }),
    event(30.8 * MIN, EventTypes.AgentMessageReceived, { message: "pm_02", from: "WC-212", directive: "dir_p2", after: "bash" }),
    event(30.8 * MIN - 500, EventTypes.AgentPeerWaitEnded, { message: "pm_01", on: "WC-212", by: "reply" }),
    event(19.7 * MIN, EventTypes.AgentMessageReceived, { message: "pm_03", from: "WC-215", directive: "dir_p3", after: "edit" }),
    event(19.5 * MIN, EventTypes.AgentMessageSent, { message: "pm_04", to: "WC-215", reply_to: "pm_03", lands: "next_step", text: M_ANSWER.text }),
    event(0.28 * MIN, EventTypes.ToolCompleted, { tool: "bash", callId: "c5", status: "completed", exitCode: 0, output: { head: " Test Files  3 passed (3)\n      Tests  14 passed (14)\n" } }),
    event(0.27 * MIN, EventTypes.AgentNoteTold, { note: "en_04", n: 4, change: "replaced", was: 3, after: "bash", directive: "dir_p4" }),
    event(0.25 * MIN, EventTypes.ToolCalled, { tool: "list_tasks", callId: "p1", input: { peers: true }, title: "list_tasks" }),
    event(0.25 * MIN - 800, EventTypes.ToolCompleted, { tool: "list_tasks", callId: "p1", status: "completed", output: json(peerList) }),
    event(0.2 * MIN, EventTypes.ToolCalled, { tool: "message", callId: "p2", input: { to: "WC-212", text: M_WAIT.text }, title: "message" }),
    event(0.2 * MIN - 300, EventTypes.AgentMessageSent, { message: "pm_05", to: "WC-212", reply_to: null, lands: "next_step", text: M_WAIT.text }),
    event(0.2 * MIN - 600, EventTypes.ToolCompleted, { tool: "message", callId: "p2", status: "completed", output: json({ id: "pm_05", to: "WC-212", lands: "next_step" }) }),
    event(0.16 * MIN, EventTypes.ToolCalled, { tool: "wait_for_reply", callId: "p3", input: { message: "pm_05" }, title: "wait_for_reply" }),
    event(0.15 * MIN + 200, EventTypes.ToolCompleted, { tool: "wait_for_reply", callId: "p3", status: "completed",
      output: json({ waiting: { on: "WC-212", message: "pm_05" }, next: "End your turn: the reply resumes you." }) }),
    event(0.15 * MIN, EventTypes.AgentPeerWaiting, { message: "pm_05", on: "WC-212", how: "wait", parks_at: iso(-9.85 * MIN) }),
  ].map((e) => ({ ...e, actor: e.eventType.startsWith("agent.message") || e.eventType.startsWith("agent.peer") || e.eventType === EventTypes.AgentNoteTold ? dude : e.actor }));
}

/** A conductor on WC-214, ended: Márcio asked what changed, and its briefing carried the epic's digest. */
export const CONDUCTOR_RUN: Run = run({ id: CONDUCTOR_ID, phase: null, role: "conductor", status: "completed", kind: "agent",
  createdAt: iso(12 * MIN), startedAt: iso(12 * MIN), endedAt: iso(9 * MIN), modelTier: "Thinker", model: "claude-fable-5-1" });

export function conductorEvents(): PersistedEvent[] {
  const me = { type: "human" as const, id: YOU, name: "Márcio Martins" };
  const conductor = { type: "agent" as const, id: "conductor" };
  const at = (m: number, type: string, payload: Record<string, unknown>, actor: PersistedEvent["actor"] = dude) => event(m * MIN, type, payload, actor, CONDUCTOR_ID);
  return [
    at(12, EventTypes.RunCreated, { role: "conductor" }),
    at(12 - 0.01, EventTypes.ChatMessage, { text: "What did the other tasks on Checkout v2 settle while I was away?" }, me),
    at(12 - 0.02, EventTypes.ConductorDigest, {
      since: iso(40 * MIN), through: iso(12 * MIN), where: "briefing", task: "WC-214", more: 0,
      messages: [
        { message: "pm_01", at: M_ASK.sentAt, from: "WC-214", to: "WC-212", text: M_ASK.text, replyTo: null, answeredAt: M_REPLY.sentAt },
        { message: "pm_02", at: M_REPLY.sentAt, from: "WC-212", to: "WC-214", text: M_REPLY.text, replyTo: "pm_01", answeredAt: null },
        { message: "pm_03", at: M_APPLE.sentAt, from: "WC-215", to: "WC-214", text: M_APPLE.text, replyTo: null, answeredAt: M_ANSWER.sentAt },
        { message: "pm_04", at: M_ANSWER.sentAt, from: "WC-214", to: "WC-215", text: M_ANSWER.text, replyTo: "pm_03", answeredAt: null },
      ],
      notes: [{ at: iso(30 * MIN), n: 4, change: "replaced", was: 3, line: "note 4, WC-214's implementer, replaces note 3: Totals. Read them with useTotals(country)" }],
      peers: [{ task: "WC-215", status: "awaiting_input", at: iso(20 * MIN) }],
    }),
    at(12 - 0.03, EventTypes.ConductorBriefed, { text: "Conductor for WC-214. Since you last looked, its implementer and WC-212's agreed the totals hook; note 4 pins it.\n\nMárcio wrote: What did the other tasks on Checkout v2 settle while I was away?" }),
    at(11.5, EventTypes.AgentMessage, { text: "Two things settled. WC-212 confirmed **`useTotals(country)`** is the hook to read totals with, and WC-214's implementer pinned that as note 4, replacing note 3. WC-215 asked who owns the submit button; the answer is the footer's `primary` slot, so Apple Pay goes there." }, conductor),
    at(9, EventTypes.RunCompleted, {}),
  ];
}
