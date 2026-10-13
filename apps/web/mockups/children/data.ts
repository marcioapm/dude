/*
 * The children mockup's world: WI-2416's fix Run as it really happened on
 * 2026-10-13 (times and titles from the ledger and opencode's own store),
 * then the same shape on Claude Code and Codex, and what the stall banner
 * says once it can see the child.
 */

export type Harness = "opencode" | "claude" | "codex";
export type Scene = "today" | "live" | "waiting" | "done" | "parallel";

const MIN = 60_000;
export const NOW = Date.now();
export const ago = (min: number) => NOW - min * MIN;
export const at = (iso: string) => new Date(iso).getTime();

export const RUN = {
  id: "run_0mv60mjic642ee87f9d6eb3a7",
  taskKey: "WI-2416",
  title: "Correcting revenue chart runbook and OOM limits",
  repo: "acme/shop",
  branch: "dude/wi_2416/attempt-3",
  model: "claude-sonnet-5",
  startedAt: at("2026-10-13T01:41:53Z"),
};

/** The parent's own turns before it delegated, as the ledger has them. */
export const BEFORE = {
  steer: {
    at: at("2026-10-13T01:43:28Z"),
    by: "Márcio Martins",
    text: "Stop the unrelated Gradle checks. Keep only the docs edit you were asked for, finish the chart screenshot fallback, and do not invent test fixtures.",
  },
  plan: [
    { content: "Revert the full-monorepo spotless run", status: "completed" as const },
    { content: "Keep docs/runbooks/revenue-chart.md edit only", status: "completed" as const },
    { content: "Capture the revenue chart with the Playwright fallback", status: "in_progress" as const },
    { content: "Report evidence", status: "pending" as const },
  ],
  message:
    "Reverting the spotless run. Before I touch the screenshot fallback I want to know exactly how the UI tests create their Playwright contexts, so I'll have a subagent map that while I keep the docs edit.",
};

/** The delegation: what the parent asked, in each harness's words. */
export const TASK_CALL = {
  opencode: { tool: "task", title: "Inspect test capture setup", kind: "explore", input: { description: "Inspect test capture setup", subagent_type: "explore", prompt: "Research only, do not modify files. Determine exactly how the Python UI tests create Playwright contexts, where record_video / screenshot options are set, and which conftest hooks run for ui_previewer…" } },
  claude: { tool: "Agent", title: "Inspect test capture setup", kind: "Explore", input: { description: "Inspect test capture setup", subagent_type: "Explore", prompt: "Research only, do not modify files. Determine exactly how the Python UI tests create Playwright contexts…" } },
  codex: { tool: "spawn_agent", title: "Inspect test capture setup", kind: "explorer", input: { agent_path: "/root/inspect_capture", prompt: "Research only, do not modify files. Determine exactly how the Python UI tests create Playwright contexts…", model: "gpt-5.6-sol", reasoning_effort: "low" } },
} as const;

export const CHILD_ID = { opencode: "ses_ecd119ab1ffe4Jl4a1gyJ1Ip8B", claude: "a2bf1288fc17cd1d3", codex: "01a133b0-0aa5-7b93-b598-a603ff0d58a5" } as const;

export interface ChildTool {
  readonly id: string;
  readonly tool: string;
  readonly args: unknown;
  readonly at: number;
  readonly ms: number;
  readonly status: "completed" | "failed" | "running";
  readonly result?: string;
  readonly error?: string;
}

/** What the child actually did, from opencode.db of the real Run. */
export const CHILD_TOOLS: readonly ChildTool[] = [
  { id: "c1", tool: "glob", args: { pattern: "**/conftest.py" }, at: at("2026-10-13T01:44:13Z"), ms: 310, status: "completed", result: "tests/conftest.py\ntests/suites/ui_previewer/conftest.py\ntests/suites/ui_visual_diffs/conftest.py" },
  { id: "c2", tool: "grep", args: { pattern: "(pytest-playwright|playwright)", include: "tests/requirements*.txt" }, at: at("2026-10-13T01:44:22Z"), ms: 420, status: "completed", result: "tests/requirements.txt:14: pytest-playwright==0.7.1" },
  { id: "c3", tool: "read", args: { filePath: "tests/run_tests.py" }, at: at("2026-10-13T01:44:31Z"), ms: 90, status: "completed", result: "312 lines" },
  { id: "c4", tool: "read", args: { filePath: "tests/conftest.py" }, at: at("2026-10-13T01:44:51Z"), ms: 80, status: "completed", result: "188 lines" },
  { id: "c5", tool: "read", args: { filePath: "tests/suites/ui_previewer/base.py" }, at: at("2026-10-13T01:44:51Z"), ms: 70, status: "completed", result: "96 lines" },
  { id: "c6", tool: "grep", args: { pattern: "def pytest_(addoption|browser_context_args|configure)", path: "tests" }, at: at("2026-10-13T01:44:51Z"), ms: 380, status: "completed", result: "4 matches in 2 files" },
  { id: "c7", tool: "bash", args: { command: "python3 -c \"import pytest_playwright, inspect; print(pytest_playwright.__file__)\"" }, at: at("2026-10-13T01:45:02Z"), ms: 1_240, status: "completed", result: "/opt/ui-tests/lib/python3.14/site-packages/pytest_playwright/__init__.py" },
];

/** The three calls that hit `external_directory: ask` and were never answered. */
export const CHILD_STUCK: readonly ChildTool[] = [
  { id: "c8", tool: "grep", args: { pattern: "(record_video|viewport|def browser_context_args)", path: "/opt/ui-tests/lib/python3.14/site-packages/pytest_playwright" }, at: at("2026-10-13T01:45:09Z"), ms: 0, status: "running" },
  { id: "c9", tool: "read", args: { filePath: "/opt/ui-tests/lib/python3.14/site-packages/pytest_playwright/pytest_playwright.py" }, at: at("2026-10-13T01:45:09Z"), ms: 0, status: "running" },
];

export const CHILD_MESSAGES = [
  { id: "m1", at: at("2026-10-13T01:44:41Z"), text: "Three conftests: the root one registers `--ui-output`, and `ui_previewer/conftest.py` overrides `browser_context_args` to set `record_video_dir` from `PYTEST_ADDOPTS`. Checking where the viewport comes from." },
  { id: "m2", at: at("2026-10-13T01:45:02Z"), text: "No viewport is set in the repo. It must come from pytest-playwright's defaults; reading the installed plugin to confirm." },
];

export const WAITING = {
  since: at("2026-10-13T01:45:09Z"),
  permission: "external_directory",
  pattern: "/opt/ui-tests/lib/python3.14/site-packages/pytest_playwright/*",
};

export const DONE = {
  endedAt: at("2026-10-13T01:46:40Z"),
  costUsd: 0.37,
  tokens: 83_400,
  summary:
    "Contexts come from pytest-playwright's `browser_context_args` fixture; `ui_previewer/conftest.py` adds `record_video_dir`. No viewport is set anywhere, so Playwright's 1280×720 default applies — that is why the captures are small. Set `viewport` and `record_video_size` to 1920×1080 in that fixture.",
};

/** Codex: two children at once, from one spawn each. */
export const PARALLEL = [
  { id: "01a133b0-0aa5-7b93-b598-a603ff0d58a5", title: "Inspect test capture setup", path: "/root/inspect_capture", startedAt: ago(4), costUsd: 0.21, status: "running" as const, tool: { tool: "shell", args: { command: "rg -n 'record_video|viewport' tests/" }, since: ago(0.5) } },
  { id: "01a133b0-1c22-7e00-9b1a-2c7f1a5d9e01", title: "Find the chart fixture dataset", path: "/root/find_fixture", startedAt: ago(3), costUsd: 0.09, status: "completed" as const, summary: "tests/data/pull.sh fetches the sandbox test-data bucket; not git LFS." },
];

export const STALL = {
  today:
    "Your fix Run run_0mv60mjic642ee87f9d6eb3a7 (round 3, running 2h 23m) has made no progress for 1h 57m. It has 1 tool call open: `think` (\"Investigate UI artifact capture\"), for 117m; `think` runs inside the agent: no separate process is expected. Processes: opencode acp (0.8% CPU), dockerd (idle). 83 CPU-s over 2h.",
  after:
    "Your fix Run run_0mv60mjic642ee87f9d6eb3a7 (round 3, running 2h 23m) has made no progress for 1h 57m. Its `task` call \"Inspect test capture setup\" has a child (explore · ses_ecd119ab1f) that has been waiting 1h 57m on a permission nothing can answer: `external_directory` for /opt/ui-tests/lib/python3.14/site-packages/pytest_playwright/*. The child's last work was at 01:45:09 (grep, read). No model request since.",
};
