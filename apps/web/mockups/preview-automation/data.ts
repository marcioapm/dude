export const SCREENS = {
  "project-before": "1 · Project settings / Before",
  "project-off": "2 · Project settings / Proposed · Off",
  "project-on": "3 · Project settings / Proposed · On",
  "project-missing": "4 · Settings / On · none enabled",
  "project-empty": "5 · Settings / On · no definitions",
  "task-inherit": "6 · Task overview / Inherited policy",
  "task-off": "7 · Non-UI task / Explicit Off",
  "conductor-ready": "8 · Conductor / Pre-PR ready",
  "conductor-starting": "9 · Conductor / Starting",
  "conductor-failed": "10 · Conductor / Failed",
  "brainstorm-ready": "11 · Brainstorm / Branch preview",
  "servers-dormant": "12 · Task servers / Dormant on PR",
  "servers-reused": "13 · Task servers / Conductor preview reused",
  notes: "14 · Design notes / Recommendation",
} as const;
export type Screen = keyof typeof SCREENS;
export type Override = "inherit" | "on" | "off";
export type PreviewState = "dormant" | "starting" | "ready" | "failed";
export const TASK_PREVIEW = { id: "pv_7f3a", url: "https://web-pv7f3a.preview.example.com", branch: "feature/queue-retry", head: "a47b9e2", task: "ACME-142" };
export const BRANCH_PREVIEW = { id: "pv_b92c", url: "https://web-pvb92c.preview.example.com", branch: "explore/retry-observability", head: "de309a1", project: "Acme" };
export const RECIPES = [
  { name: "web", port: 3000, command: "bun run dev --host 0.0.0.0", workdir: "apps/web", env: [], autostartInPreviews: true, setup: "bun install --frozen-lockfile", createdAt: "2026-11-23T12:00:00Z", updatedAt: "2026-11-23T12:00:00Z", updatedBy: null },
  { name: "worker", port: 8080, command: "bun run worker", workdir: ".", env: [], autostartInPreviews: false, setup: null, createdAt: "2026-11-23T12:00:00Z", updatedAt: "2026-11-23T12:00:00Z", updatedBy: null },
];
export const PERSON = { id: "marcio", name: "Márcio", email: "marcio@example.com" };
