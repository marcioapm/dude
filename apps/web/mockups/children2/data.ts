/*
 * The second children mockup's world: a Codex implementer spawns two
 * subagents and KEEPS WORKING — twenty turns of its own after the spawn.
 * This is the case the first mockup never showed, and where "threads
 * inline, open by default" buries the children.
 */

export const NOW = Date.now();
export const ago = (min: number) => NOW - min * 60_000;

export const RUN = {
  id: "run_0mv7k2q9a3e8c1b4d6f0a2c5e",
  taskKey: "WI-2416",
  title: "Correct the revenue chart runbook and OOM limits",
  repo: "acme/shop",
  branch: "dude/wi_2416/attempt-3",
  model: "gpt-5.6-sol",
  startedAt: ago(26),
};

export interface Kid {
  readonly id: string;
  readonly callId: string;
  readonly title: string;
  readonly kind: string;
  readonly startedAt: number;
  readonly endedAt: number | null;
  readonly costUsd: number;
  readonly turnCount: number;
  readonly status: "running" | "completed";
  readonly summary: string;
  readonly last: string;
  readonly waiting?: { on: string; since: number; detail: string } | undefined;
  readonly turns: ReadonlyArray<{ kind: "tool"; tool: string; args: unknown; at: number; ms?: number; done: boolean; result?: string } | { kind: "msg"; text: string; at: number; cost: number }>;
}

export const KIDS: readonly Kid[] = [
  {
    id: "thr_a9f2", callId: "spawn_a", title: "Inspect test capture setup", kind: "explorer", startedAt: ago(19), endedAt: null, costUsd: 0.41, turnCount: 9,
    status: "running", summary: "", last: "grep viewport tests/",
    waiting: { on: "a permission", since: ago(11), detail: "external_directory for /opt/ui-tests/lib/python3.14/site-packages/*" },
    turns: [
      { kind: "tool", tool: "shell", args: { command: "find tests -name conftest.py" }, at: ago(19), ms: 320, done: true, result: "tests/conftest.py\ntests/suites/ui_previewer/conftest.py" },
      { kind: "tool", tool: "read", args: { file_path: "tests/suites/ui_previewer/conftest.py" }, at: ago(18.5), ms: 80, done: true, result: "96 lines" },
      { kind: "msg", text: "Two conftests; the previewer one sets record_video_dir from PYTEST_ADDOPTS. Looking for where the viewport comes from.", at: ago(17), cost: 0.08 },
      { kind: "tool", tool: "shell", args: { command: "python3 -c 'import pytest_playwright; print(pytest_playwright.__file__)'" }, at: ago(14), ms: 1_240, done: true, result: "/opt/ui-tests/lib/python3.14/site-packages/pytest_playwright/__init__.py" },
      { kind: "tool", tool: "grep", args: { pattern: "(record_video|viewport)", path: "/opt/ui-tests/lib/python3.14/site-packages/pytest_playwright" }, at: ago(11), done: false },
    ],
  },
  {
    id: "thr_c41e", callId: "spawn_b", title: "Find the chart fixture dataset", kind: "explorer", startedAt: ago(19), endedAt: ago(13), costUsd: 0.09, turnCount: 4,
    status: "completed", summary: "tests/data/pull.sh fetches it from the sandbox test-data bucket; not git LFS.", last: "ended",
    turns: [
      { kind: "tool", tool: "grep", args: { pattern: "exposures.psv", path: "tests" }, at: ago(18.8), ms: 410, done: true, result: "tests/data/pull.sh:12" },
      { kind: "tool", tool: "read", args: { file_path: "tests/data/pull.sh" }, at: ago(17.5), ms: 60, done: true, result: "41 lines" },
      { kind: "msg", text: "tests/data/pull.sh fetches it from the sandbox test-data bucket; not git LFS.", at: ago(13.2), cost: 0.09 },
    ],
  },
];

/** The parent's own work after the spawn: what buries inline threads. */
export const PARENT_AFTER: ReadonlyArray<{ kind: "tool"; tool: string; args: unknown; at: number; ms: number; result?: string } | { kind: "msg"; text: string; at: number; cost: number }> = [
  { kind: "msg", text: "While they look, I'll fix the runbook's OOM limits section: the numbers are from the old 2 GiB containers.", at: ago(18.9), cost: 0.05 },
  { kind: "tool", tool: "read", args: { file_path: "docs/runbooks/revenue-chart.md" }, at: ago(18.7), ms: 70, result: "212 lines" },
  { kind: "tool", tool: "edit", args: { file_path: "docs/runbooks/revenue-chart.md", old: "memory: 2Gi", new: "memory: 8Gi" }, at: ago(18.2), ms: 40, result: "1 replacement" },
  { kind: "tool", tool: "edit", args: { file_path: "docs/runbooks/revenue-chart.md", old: "OOMKilled after ~40 min", new: "OOMKilled after ~3 h at 8Gi; raise to 16Gi for the yearly chart" }, at: ago(17.9), ms: 40, result: "1 replacement" },
  { kind: "tool", tool: "shell", args: { command: "git diff --stat" }, at: ago(17.4), ms: 90, result: " docs/runbooks/revenue-chart.md | 6 +++---" },
  { kind: "msg", text: "Runbook limits updated. Next the chart capture fallback itself.", at: ago(16.8), cost: 0.04 },
  { kind: "tool", tool: "read", args: { file_path: "tests/suites/ui_previewer/base.py" }, at: ago(16.5), ms: 60, result: "96 lines" },
  { kind: "tool", tool: "read", args: { file_path: "tests/suites/ui_previewer/charts.py" }, at: ago(16.1), ms: 80, result: "154 lines" },
  { kind: "tool", tool: "grep", args: { pattern: "screenshot\\(", path: "tests/suites" }, at: ago(15.6), ms: 300, result: "7 matches in 3 files" },
  { kind: "msg", text: "The fallback captures the page at whatever viewport the context has. I'll wait for the capture-setup finding before changing it; meanwhile the dataset answer is in.", at: ago(12.9), cost: 0.06 },
  { kind: "tool", tool: "read", args: { file_path: "tests/data/pull.sh" }, at: ago(12.6), ms: 50, result: "41 lines" },
  { kind: "tool", tool: "edit", args: { file_path: "docs/runbooks/revenue-chart.md", old: "Fetch the fixture from LFS", new: "Fetch the fixture with tests/data/pull.sh (sandbox test-data bucket)" }, at: ago(12.1), ms: 40, result: "1 replacement" },
  { kind: "tool", tool: "shell", args: { command: "uv run pytest tests/suites/ui_previewer -k runbook -q" }, at: ago(11.4), ms: 48_000, result: "3 passed in 46.2s" },
  { kind: "msg", text: "Runbook tests green. Still waiting on the capture-setup agent for the viewport.", at: ago(10.2), cost: 0.03 },
  { kind: "tool", tool: "shell", args: { command: "git add -A && git commit -m 'Runbook: OOM limits and fixture source'" }, at: ago(9.8), ms: 120, result: "[dude/wi_2416/attempt-3 4c1e9a2] 1 file changed" },
  { kind: "tool", tool: "read", args: { file_path: "tests/suites/ui_previewer/conftest.py" }, at: ago(8.9), ms: 60, result: "96 lines" },
  { kind: "tool", tool: "grep", args: { pattern: "browser_context_args", path: "tests" }, at: ago(8.4), ms: 280, result: "2 matches" },
  { kind: "msg", text: "I can see record_video_dir is set here but no viewport. I'll draft the fixture change now and confirm against the agent's finding.", at: ago(7.1), cost: 0.05 },
  { kind: "tool", tool: "edit", args: { file_path: "tests/suites/ui_previewer/conftest.py", old: "return {**args, \"record_video_dir\": d}", new: "return {**args, \"record_video_dir\": d, \"viewport\": {\"width\": 1920, \"height\": 1080}, \"record_video_size\": {\"width\": 1920, \"height\": 1080}}" }, at: ago(6.4), ms: 40, result: "1 replacement" },
  { kind: "tool", tool: "shell", args: { command: "uv run pytest tests/suites/ui_previewer -k capture -q" }, at: ago(5.9), ms: 61_000, result: "2 passed in 59.8s" },
  { kind: "tool", tool: "wait", args: { agents: ["thr_a9f2"] }, at: ago(4.8), ms: 0, result: undefined },
];
