/**
 * Navigation fixtures: three projects, ~50 tasks, enough variety to
 * show whether the sidebar stays calm. Deterministic.
 */

import type { Person } from "../components/PersonAvatar.tsx";
import type { NavProject, NavSession, NavTask } from "../util/navModel.ts";
import type { AgentRole, SessionStatus, TaskStatus } from "@dude/domain";

export const people: Record<string, Person> = {
  marcio: { id: "u_marcio", name: "Márcio Martins" },
  ana: { id: "u_ana", name: "Ana Ribeiro" },
  tom: { id: "u_tom", name: "Tom Okafor" },
  lin: { id: "u_lin", name: "Lin Zhao" },
  sam: { id: "u_sam", name: "sam.delgado" },
  priya: { id: "u_priya", name: "priya@example.com" },
  jules: { id: "u_jules", name: "Jules" },
  kai: { id: "u_kai", name: "Kai Nakamura" },
};

const P = people;

function ses(id: string, role: AgentRole, status: SessionStatus, activity?: string, children?: NavSession[]): NavSession {
  return { id, role, status, activity, children };
}

/** A running orchestrator with a typical crew. */
function crew(id: string, extra?: NavSession[]): NavSession[] {
  return [
    ses(`${id}-orc`, "orchestrator", "running", "Waiting for implementer", [
      ses(`${id}-inv`, "investigator", "completed"),
      ses(`${id}-imp`, "implementer", "running", "Running bun test", extra),
    ]),
  ];
}

const NOW = Date.now();
const HOUR = 3_600_000;

/** Plausible time-in-status by stage, in hours; the spread comes from the id. */
const AGE_HOURS: Record<TaskStatus, number> = {
  received: 30,
  intake: 3,
  awaiting_confirmation: 5,
  queued: 20,
  running: 4,
  awaiting_input: 2,
  review: 26,
  ready_to_merge: 9,
  done: 160,
  failed: 40,
  aborted: 90,
};
const SPENDS: ReadonlySet<TaskStatus> = new Set(["intake", "awaiting_confirmation", "running", "awaiting_input", "review", "ready_to_merge", "done", "failed", "aborted"]);

let n = 2400;
function wi(title: string, status: TaskStatus, opts: { people?: Person[]; sessions?: NavSession[]; runs?: number } = {}): NavTask {
  n += 1;
  const id = `wi_${n}`;
  const spread = ((n * 37) % 17) / 17 + 0.15;
  const statusSince = NOW - AGE_HOURS[status] * spread * HOUR;
  const costUsd = SPENDS.has(status) ? (((n * 7919) % 2400) / 100 + 0.4) * (opts.runs ?? 1) : undefined;
  const runs =
    opts.sessions !== undefined
      ? [
          ...Array.from({ length: (opts.runs ?? 1) - 1 }, (_, i) => ({
            id: `${id}-r${i + 1}`,
            attempt: i + 1,
            status: "failed" as const,
            sessions: [ses(`${id}-r${i + 1}-orc`, "orchestrator" as const, "failed" as const, undefined, [ses(`${id}-r${i + 1}-imp`, "implementer", "failed")])],
          })),
          { id: `${id}-r${opts.runs ?? 1}`, attempt: opts.runs ?? 1, status: status === "failed" ? ("failed" as const) : ("running" as const), sessions: opts.sessions },
        ]
      : undefined;
  return { id, key: `WI-${n}`, title, status, people: opts.people, runs, statusSince, costUsd };
}

export const navProjects: NavProject[] = [
  {
    id: "p_control",
    name: "control-plane",
    epics: [
      {
        id: "e_webhooks",
        title: "Webhook reliability",
        tasks: [
          wi("Add retry with backoff to the GitHub webhook handler", "awaiting_input", {
            people: [P["marcio"]!, P["ana"]!],
            sessions: [
              ses("s_2401-orc", "orchestrator", "awaiting_input", "Should 4xx responses be retried?", [
                ses("s_2401-inv", "investigator", "completed"),
                ses("s_2401-imp", "implementer", "completed", undefined, [ses("s_2401-qa", "qa_browser", "failed")]),
                ses("s_2401-rev", "reviewer", "pending"),
              ]),
            ],
          }),
          wi("Dedupe deliveries by X-GitHub-Delivery across restarts", "running", { people: [P["marcio"]!], sessions: crew("s_2402") }),
          wi("Verify webhook signature before enqueueing", "ready_to_merge", { people: [P["ana"]!, P["marcio"]!, P["tom"]!, P["lin"]!] }),
          wi("Replay endpoint for missed deliveries", "review", { people: [P["tom"]!] }),
          wi("Surface delivery failures in the events ledger", "queued", { people: [P["ana"]!] }),
          wi("Alert on sustained 5xx from api.github.com", "done", { people: [P["marcio"]!] }),
        ],
      },
      {
        id: "e_intervention",
        title: "Human intervention",
        tasks: [
          wi("Steer: deliver directives to the running harness", "running", {
            people: [P["marcio"]!],
            sessions: [
              ses("s_2407-orc", "orchestrator", "running", "Delegating", [
                ses("s_2407-imp", "implementer", "running", "Editing control.go", [ses("s_2407-inv", "investigator", "running", "Reading runner/cmd")]),
                ses("s_2407-rev", "reviewer", "running", "Reviewing intervention.ts"),
              ]),
            ],
          }),
          wi("Pause graceful vs hard: finish the current tool call first", "failed", { people: [P["tom"]!], sessions: [ses("s_2408-orc", "orchestrator", "failed")], runs: 2 }),
          wi("Abort must preserve the workspace for inspection", "done", { people: [P["marcio"]!] }),
          wi("Resume after pause re-attaches to the same harness session", "queued", { people: [P["lin"]!] }),
          wi("Directive supersession: keep the earlier row", "done", { people: [P["ana"]!] }),
        ],
      },
      {
        id: "e_sweepers",
        title: "Background sweepers",
        tasks: [
          wi("Reconcile drifted runs against worker heartbeats", "done", { people: [P["marcio"]!] }),
          wi("Expire stale workspaces after 72h idle", "done", { people: [P["sam"]!] }),
          wi("Budget sweeper: pause runs that cross the cost ceiling", "awaiting_confirmation", { people: [P["sam"]!, P["marcio"]!] }),
          wi("Ledger compaction for events older than 30 days", "received", { people: [P["kai"]!] }),
        ],
      },
    ],
    tasks: [
      wi("Fix flaky test: reconcile: 3 runs checked", "running", { people: [P["jules"]!], sessions: [ses("s_2416-orc", "orchestrator", "running", "Thinking", [ses("s_2416-inv", "investigator", "running", "grep reconcile")])] }),
      wi("Bump zod to 3.25", "ready_to_merge", { people: [P["kai"]!] }),
      wi("Type the event payloads end to end", "intake", { people: [P["marcio"]!] }),
    ],
  },
  {
    id: "p_web",
    name: "web",
    epics: [
      {
        id: "e_chat",
        title: "Chat interface",
        tasks: [
          wi("Transcript follows the tail until the operator scrolls", "done", { people: [P["marcio"]!] }),
          wi("Composer: answer vs steer on four channels", "done", { people: [P["marcio"]!] }),
          wi("Nested subagent threads with role-coloured rails", "done", { people: [P["marcio"]!] }),
          wi("Streaming Markdown: treat open fences as open", "ready_to_merge", { people: [P["lin"]!, P["marcio"]!] }),
          wi("Tool call card: diff hands off to DiffView", "review", { people: [P["lin"]!] }),
          wi("Retry countdown ring keeps ticking under reduced motion", "done", { people: [P["priya"]!] }),
        ],
      },
      {
        id: "e_nav",
        title: "Navigation",
        tasks: [
          wi("Sidebar: projects, epics, tasks, sessions", "running", {
            people: [P["marcio"]!],
            sessions: [
              ses("s_2425-orc", "orchestrator", "running", "Waiting for reviewer", [
                ses("s_2425-imp", "implementer", "completed"),
                ses("s_2425-rev", "reviewer", "awaiting_input", "Is 12px indent enough at depth 4?"),
              ]),
            ],
          }),
          wi("Human avatars: initials, deterministic colour, stack", "queued", { people: [P["marcio"]!] }),
          wi("Keyboard: arrows, Home/End, slash to search", "queued", { people: [P["priya"]!] }),
          wi("Persist expand state per project", "received", { people: [P["jules"]!] }),
        ],
      },
      {
        id: "e_settings",
        title: "Settings",
        tasks: [
          wi("Per-role model picker with org fallback", "done", { people: [P["ana"]!] }),
          wi("Repository trust class toggle", "done", { people: [P["ana"]!] }),
          wi("Cost ceiling per role", "done", { people: [P["tom"]!] }),
          wi("Runtime image override", "aborted", { people: [P["tom"]!] }),
          wi("Theme preference persists across devices", "done", { people: [P["priya"]!] }),
        ],
      },
    ],
    tasks: [
      wi("Lighthouse: LCP under 1.5s on the transcript page", "queued", { people: [P["kai"]!] }),
      wi("Replace CDN font load with bundled files", "done", { people: [P["marcio"]!] }),
    ],
  },
  {
    id: "p_runner",
    name: "runner",
    epics: [
      {
        id: "e_harness",
        title: "Harness adapters",
        tasks: [
          wi("Claude Code adapter: map tool events to the ledger", "done", { people: [P["marcio"]!] }),
          wi("OpenCode adapter: session resume", "failed", {
            people: [P["sam"]!, P["marcio"]!],
            sessions: [ses("s_2437-orc", "orchestrator", "failed", undefined, [ses("s_2437-imp", "implementer", "failed")])],
            runs: 3,
          }),
          wi("Capability negotiation: refuse a role the harness cannot fill", "review", { people: [P["lin"]!] }),
          wi("No-model runner mode for platform tests", "done", { people: [P["marcio"]!] }),
        ],
      },
      {
        id: "e_sandbox",
        title: "Sandboxing",
        tasks: [
          wi("Network egress allowlist per trust class", "done", { people: [P["tom"]!] }),
          wi("Credential injection at exec time, never on disk", "done", { people: [P["tom"]!] }),
          wi("Untrusted repos run without org secrets", "queued", { people: [P["ana"]!] }),
          wi("Seccomp profile for the runtime image", "received", { people: [P["kai"]!] }),
          wi("Workspace snapshots before destructive tool calls", "queued", { people: [P["sam"]!] }),
        ],
      },
    ],
    tasks: [
      wi("Heartbeat every 5s; worker marked lost after 3 misses", "done", { people: [P["marcio"]!] }),
      wi("Graceful drain on SIGTERM", "done", { people: [P["marcio"]!] }),
      wi("Structured logs with session id on every line", "running", { people: [P["jules"]!], sessions: crew("s_2448") }),
      wi("Exponential backoff on control-plane reconnect", "queued", { people: [P["kai"]!] }),
    ],
  },
];

/** A single quiet project: nothing needs anyone. */
export const navProjectsQuiet: NavProject[] = [
  {
    id: "p_docs",
    name: "docs",
    epics: [
      {
        id: "e_guides",
        title: "Guides",
        tasks: [wi("Getting started", "done", { people: [P["ana"]!] }), wi("Configuring a project", "done", { people: [P["ana"]!] }), wi("Steering an agent", "queued", { people: [P["marcio"]!] })],
      },
    ],
    tasks: [wi("Fix broken anchors in the plan", "review", { people: [P["jules"]!] })],
  },
];

/** All three projects folded into one: a board whose Closed column overflows its cap. */
export const navProjectEverything: NavProject = {
  id: "p_everything",
  name: "everything",
  epics: navProjects.flatMap((p) => p.epics ?? []),
  tasks: navProjects.flatMap((p) => p.tasks ?? []),
};

/** A project with nothing in it yet. */
export const navProjectEmpty: NavProject = { id: "p_new", name: "new-service", epics: [], tasks: [] };
