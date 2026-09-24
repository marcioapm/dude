/**
 * A finished, realistic transcript for the gallery: what one implementer
 * session looks like after the fact, with everything the real runs showed
 * we were missing — the factory's phase prompt at the top, the model's
 * reasoning between its moves, tool output capped by the backend with the
 * middle dropped, a non-zero exit, and context/output tokens on every
 * turn. Static on purpose: the live scenario shows motion; this shows
 * density.
 */

import type { ToolOutput } from "../components/ToolCallCard.tsx";

export const PHASE_PROMPT = `You are the **implementer** for work item **WI-2481** in \`dude/dude\`.

## Goal

Add retry with exponential backoff to \`GithubClient.post\` so transient GitHub failures (5xx, 429) no longer surface as lost webhook deliveries.

## Context from the investigator

- \`apps/control-plane/src/integrations/github/client.ts:12\` performs a single \`fetch\` and throws on \`!res.ok\`.
- The webhook route already dedupes on \`X-GitHub-Delivery\`, so retrying a POST cannot double-apply.
- \`apps/control-plane/src/util/wait.ts\` duplicates \`integrations/github/sleep.ts\`; remove one.

## Acceptance criteria

1. \`post\` retries on 5xx and 429 only, never on other 4xx.
2. At most 5 attempts, exponential backoff from 250ms with full jitter, capped at 8s.
3. The public signature of \`GithubClient.post\` is unchanged.
4. New tests cover: retry on 502, no retry on 422, gives up after 5 attempts.
5. \`bun test apps/control-plane\` is green.

## Constraints

- Do not add a dependency for backoff.
- Do not change the webhook route.
- Keep the diff under 200 lines; if you need more, stop and explain why.

When you are done, open a PR against \`main\` titled \`WI-2481: retry GitHub webhook deliveries with backoff\` and hand off to the reviewer.`;

export const THOUGHT_1 = `The investigator already located the call site, so I don't need to grep for it. I'll read the client first to see how errors are constructed — if it throws a plain Error with the status in the message I'll need a typed error to branch on.

The duplicated sleep helper matters: whichever one I keep needs to accept a signal or the tests will hang on the last attempt.`;

export const THOUGHT_2 = `The test failed because \`isRetryable\` is checked after the throw — I wrapped the wrong scope. The loop needs to catch, check the status from the response, and only then decide. Also the fake fetch returns 502 for every call, so "gives up after 5 attempts" should assert 5 calls, not 1.`;

export const THOUGHT_3 = `Green. The diff is 118 lines, under the cap. One thing I'm not doing: the investigator flagged that the route retries on its side too, so worst case is 5 × 3 attempts. That is a product decision, not mine — I'll note it in the PR body rather than change the route.`;

export const THOUGHT_STREAMING = `Tests are green and the diff is small. Before opening the PR I should check whether the reviewer prompt asks for a changelog entry — the last three PRs in this repo all have one under`;

const TEST_LINE = (i: number, ok: boolean, name: string, ms: number) => `${ok ? "✓" : "✗"} ${name} [${ms.toFixed(2)}ms]`;

function repeatLines(n: number, mk: (i: number) => string): string {
  const out: string[] = [];
  for (let i = 0; i < n; i++) out.push(mk(i));
  return out.join("\n");
}

/** ~2 KB head + ~2 KB tail of a long test run, as the backend would deliver it. */
export const LONG_TEST_OUTPUT_FAILED: ToolOutput = {
  head: `bun test v1.2.4 (a1b2c3d4)

apps/control-plane/src/integrations/github/client.test.ts:
${repeatLines(22, (i) => TEST_LINE(i, i !== 7, i === 7 ? "retries when response is 502" : `GithubClient > ${["headers", "url", "post body", "json", "auth", "user-agent", "timeout"][i % 7]} case ${Math.floor(i / 7) + 1}`, 0.4 + (i % 5) * 0.37))}

apps/control-plane/src/integrations/github/webhooks.test.ts:
${repeatLines(9, (i) => TEST_LINE(i, true, `webhook route > dedupes delivery ${i + 1}`, 1.1 + i * 0.2))}

apps/control-plane/src/api/routes/webhooks.test.ts:`,
  tail: `${repeatLines(14, (i) => TEST_LINE(i, true, `routes/webhooks > ${["POST returns 202", "rejects bad signature", "ignores unknown event", "records delivery id"][i % 4]} #${i + 1}`, 0.8 + (i % 3) * 0.5))}

# Unhandled error between tests
-------------------------------
error: expected 5 calls, received 1

  at <anonymous> (apps/control-plane/src/integrations/github/client.test.ts:41:22)
-------------------------------

 61 pass
 1 fail
 1 error
 142 expect() calls
Ran 62 tests across 3 files. [412.00ms]`,
  omittedBytes: 12_611,
};

export const LONG_TEST_OUTPUT_PASSED: ToolOutput = {
  head: `bun test v1.2.4 (a1b2c3d4)

apps/control-plane/src/integrations/github/client.test.ts:
${repeatLines(25, (i) => TEST_LINE(i, true, i === 7 ? "retries when response is 502" : i === 8 ? "does not retry 422" : i === 9 ? "gives up after 5 attempts" : `GithubClient > ${["headers", "url", "post body", "json", "auth", "user-agent", "timeout"][i % 7]} case ${Math.floor(i / 7) + 1}`, 0.4 + (i % 5) * 0.37))}
`,
  tail: ` 65 pass
 0 fail
 151 expect() calls
Ran 65 tests across 3 files. [388.00ms]`,
  omittedBytes: 9_204,
};

export const TSC_STDOUT = `$ tsc --noEmit -p apps/control-plane/tsconfig.json`;
export const TSC_STDERR = `apps/control-plane/src/integrations/github/client.ts(31,11): error TS2339: Property 'status' does not exist on type 'unknown'.
apps/control-plane/src/integrations/github/client.ts(44,7): error TS2554: Expected 1 arguments, but got 2.`;

export const CLIENT_SOURCE = `import type { GithubConfig } from "./config.ts";

export class GithubClient {
  constructor(private readonly config: GithubConfig) {}

  async post<T>(path: string, body: unknown): Promise<T> {
    const res = await fetch(this.url(path), { method: "POST", headers: this.headers(), body: JSON.stringify(body) });
    if (!res.ok) throw new Error(\`GitHub \${res.status}\`);
    return (await res.json()) as T;
  }
}`;

export const CLIENT_DIFF = `diff --git a/apps/control-plane/src/integrations/github/client.ts b/apps/control-plane/src/integrations/github/client.ts
--- a/apps/control-plane/src/integrations/github/client.ts
+++ b/apps/control-plane/src/integrations/github/client.ts
@@ -1,11 +1,33 @@
 import type { GithubConfig } from "./config.ts";
+import { sleep } from "./sleep.ts";
+
+const MAX_ATTEMPTS = 5;
+const BASE_DELAY_MS = 250;
+const MAX_DELAY_MS = 8_000;
+
+function isRetryable(status: number): boolean {
+  return status >= 500 || status === 429;
+}

 export class GithubClient {
   constructor(private readonly config: GithubConfig) {}

   async post<T>(path: string, body: unknown): Promise<T> {
-    const res = await fetch(this.url(path), { method: "POST", headers: this.headers(), body: JSON.stringify(body) });
-    if (!res.ok) throw new Error(\`GitHub \${res.status}\`);
-    return (await res.json()) as T;
+    for (let attempt = 1; ; attempt++) {
+      const res = await fetch(this.url(path), { method: "POST", headers: this.headers(), body: JSON.stringify(body) });
+      if (res.ok) return (await res.json()) as T;
+      if (!isRetryable(res.status) || attempt >= MAX_ATTEMPTS) throw new Error(\`GitHub \${res.status}\`);
+      const cap = Math.min(MAX_DELAY_MS, BASE_DELAY_MS * 2 ** (attempt - 1));
+      await sleep(Math.random() * cap);
+    }
   }
 }
`;

export const MSG_1 = `Reading the client and the two sleep helpers before I touch anything.`;

export const MSG_2 = `Added \`isRetryable\` (5xx and 429 only) and a bounded loop around \`post\`. Kept \`integrations/github/sleep.ts\` and deleted \`util/wait.ts\`. Running the suite.`;

export const MSG_3 = `The 502 test failed: I was checking \`isRetryable\` after the throw. Fixed the scope and corrected the assertion — with a fake that always returns 502, "gives up" means **5** calls, not 1.`;

/** The implementer stops for a product decision it should not make alone. */
export const QUESTION_TEXT = `The webhook route already retries a failed delivery **3 times** on its own (\`routes/webhooks.ts:48\`), so with my 5 attempts inside \`post\` the worst case is 15 calls before a delivery is declared lost.

Should I leave the route's retry in place, or fold it into this change?`;
export const QUESTION_OPTIONS = ["Leave the route as is; note it in the PR", "Remove the route's retry", "Cap both so the total stays at 5"] as const;
export const ANSWER_TEXT = `Leave it — the route's retry covers a different failure (our own handler crashing). Note the 15-call worst case in the PR body so the reviewer sees it.`;

export const MSG_4 = `All green: 65 pass, 0 fail. The diff is 118 lines.

Opening the PR now. One note for the reviewer: the webhook route has its own retry on the outer edge, so worst case is 5 × 3 attempts before a delivery is declared lost. I have not changed that — it is a product decision — but it is worth a follow-up work item.`;

export const CONTEXT_WINDOW = 744_000;

// ---------------------------------------------------------------------------
// Coloured output. The container forces colour (FORCE_COLOR, CLICOLOR_FORCE,
// TERM=xterm-256color, git color.ui=always), so this is what the backend
// delivers: the tool's own SGR codes, and whatever else it printed.
// ---------------------------------------------------------------------------

const E = "\u001b";
const sgr = (codes: string, text: string) => `${E}[${codes}m${text}${E}[0m`;

/** pytest with `-p no:cacheprovider --color=yes`: green dots, a red F, a bold red summary. */
export const PYTEST_OUTPUT = [
  `${sgr("1", "============================= test session starts ==============================")}`,
  `platform linux -- Python 3.12.4, pytest-8.3.2, pluggy-1.5.0`,
  `rootdir: /workspace`,
  `${sgr("1", "collected 7 items")}`,
  ``,
  `tests/test_client.py ${sgr("32", ".")}${sgr("32", ".")}${sgr("31", "F")}${sgr("32", ".")}${sgr("33", "s")}${sgr("32", ".")}${sgr("32", ".")}${sgr("36", "                                     [100%]")}`,
  ``,
  `${sgr("1", "=================================== FAILURES ===================================")}`,
  `${sgr("31;1", "_________________________ test_retries_when_502 _________________________")}`,
  ``,
  `    def test_retries_when_502(client, fake_fetch):`,
  `        fake_fetch.always(502)`,
  `${sgr("1", ">")}       assert client.post("/x", {}) is None`,
  `${sgr("1;31", "E       AssertionError: expected 5 calls, received 1")}`,
  ``,
  `${sgr("1;31", "tests/test_client.py")}:41: AssertionError`,
  `${sgr("36", "=========================== short test summary info ============================")}`,
  `${sgr("31", "FAILED")} tests/test_client.py::${sgr("1", "test_retries_when_502")} - AssertionError: expected 5 calls, received 1`,
  `${sgr("31", "========================= ")}${sgr("31;1", "1 failed")}${sgr("31", ", ")}${sgr("32;1", "5 passed")}${sgr("31", ", ")}${sgr("33;1", "1 skipped")}${sgr("31", " in 0.31s ==========================")}`,
].join("\n");

/** `git diff --color`: bold headers, cyan hunk, red/green lines, and the OSC hyperlink some gits emit. */
export const GIT_DIFF_COLOR = [
  `${sgr("1", "diff --git a/apps/control-plane/src/integrations/github/client.ts b/apps/control-plane/src/integrations/github/client.ts")}`,
  `${sgr("1", "index 3f2a9c1..8b1e0d4 100644")}`,
  `${sgr("1", "--- a/apps/control-plane/src/integrations/github/client.ts")}`,
  `${sgr("1", "+++ b/apps/control-plane/src/integrations/github/client.ts")}`,
  `${sgr("36", "@@ -12,7 +12,14 @@")} ${E}]8;;https://github.com/dude/dude/blob/main/apps/control-plane/src/integrations/github/client.ts#L12\u0007export class GithubClient {${E}]8;;\u0007`,
  `   async post<T>(path: string, body: unknown): Promise<T> {`,
  `${sgr("31", "-    const res = await fetch(this.url(path), this.init(body));")}`,
  `${sgr("31", "-    if (!res.ok) throw new Error(\`GitHub \${res.status}\`);")}`,
  `${sgr("32", "+    let res: Response | undefined;")}`,
  `${sgr("32", "+    for (let attempt = 1; attempt <= MAX_ATTEMPTS; attempt++) {")}`,
  `${sgr("32", "+      res = await fetch(this.url(path), this.init(body));")}`,
  `${sgr("32", "+      if (res.ok || !isRetryable(res.status)) break;")}`,
  `${sgr("32", "+      await sleep(backoff(attempt));")}`,
  `${sgr("32", "+    }")}`,
  `${sgr("32", "+    if (!res || !res.ok) throw new Error(\`GitHub \${res?.status ?? \"no response\"}\`);")}`,
  `     return (await res.json()) as T;`,
  `   }`,
].join("\n");

/** `ls -la --color=always`: bold blue directories, green executables, cyan symlinks, dim link targets. */
export const LS_COLOR = [
  `total 48`,
  `drwxr-xr-x  8 agent agent 4096 Sep 24 09:12 ${sgr("01;34", ".")}`,
  `drwxr-xr-x  3 agent agent 4096 Sep 24 09:01 ${sgr("01;34", "..")}`,
  `drwxr-xr-x  2 agent agent 4096 Sep 24 09:12 ${sgr("01;34", "apps")}`,
  `-rw-r--r--  1 agent agent  612 Sep 24 09:01 bunfig.toml`,
  `-rwxr-xr-x  1 agent agent  188 Sep 24 09:01 ${sgr("01;32", "dev.sh")}`,
  `lrwxrwxrwx  1 agent agent   19 Sep 24 09:01 ${sgr("01;36", "node_modules")} -> ${sgr("2", "../.cache/node_modules")}`,
  `drwxr-xr-x  4 agent agent 4096 Sep 24 09:12 ${sgr("01;34", "packages")}`,
  `-rw-r--r--  1 agent agent 1204 Sep 24 09:01 package.json`,
  `-rw-r--r--  1 agent agent  382 Sep 24 09:01 ${sgr("01;31", "release.tar.gz")}`,
].join("\n");

/**
 * A capped, coloured test run: the head ends inside `ESC[32m` (cut after
 * `[3`), the tail begins with the trailing `2m` of another. Neither
 * fragment may show, and the tail's first line is not green just
 * because the head's last one was.
 */
export const COLOR_OUTPUT_TRUNCATED: ToolOutput = {
  head: `${sgr("1", "bun test v1.2.4 (a1b2c3d4)")}\n\napps/control-plane/src/integrations/github/client.test.ts:\n${repeatLines(20, (i) => `${sgr("32", "✓")} GithubClient > ${["headers", "url", "post body", "json", "auth", "user-agent", "timeout"][i % 7]} case ${Math.floor(i / 7) + 1} ${sgr("2", `[${(0.4 + (i % 5) * 0.37).toFixed(2)}ms]`)}`)}\n${E}[3`,
  tail: `2m✓${E}[0m routes/webhooks > records delivery id #14 ${sgr("2", "[1.80ms]")}\n\n${sgr("31;1", "# Unhandled error between tests")}\n${sgr("31", "-------------------------------")}\n${sgr("31", "error")}: expected 5 calls, received 1\n\n  ${sgr("2", "at <anonymous> (apps/control-plane/src/integrations/github/client.test.ts:41:22)")}\n${sgr("31", "-------------------------------")}\n\n ${sgr("32", "61 pass")}\n ${sgr("31", "1 fail")}\n ${sgr("31", "1 error")}\n 142 expect() calls\nRan 62 tests across 3 files. ${sgr("2", "[412.00ms]")}`,
  omittedBytes: 12_611,
};

/** Progress bars and cursor control the way `bun install` and cargo print them, plus 256/truecolor. */
export const CONTROL_CODES_OUTPUT = [
  `${E}[?25l${E}[2K\r${sgr("36", "⠋")} Resolving...  10%\r${E}[2K${sgr("36", "⠙")} Resolving...  60%\r${E}[2K${sgr("32", "✓")} Resolved 412 packages${E}[?25h`,
  `${E}[1;32m   Compiling${E}[0m dude-orchestrator v0.1.0 ${E}[1A${E}[2K${E}[1;32m    Finished${E}[0m \`dev\` profile in 11.82s`,
  `${sgr("38;5;208", "warning")}${sgr("1", ": unused variable: \`attempt\`")}`,
  `  ${sgr("38;5;39", "-->")} src/lux.rs:41:9`,
  `${sgr("38;2;255;255;255", "truecolor white")} ${sgr("38;2;20;20;20", "truecolor near-black")} ${sgr("38;2;255;100;0", "truecolor orange")} ${sgr("38;5;240", "grey 240")}`,
  `${sgr("7", " inverse ")} ${sgr("4", "underlined")} ${sgr("3", "italic")} ${sgr("2", "dim")} ${sgr("1", "bold")} ${sgr("43;30", " black on yellow ")} ${sgr("48;2;40;60;120", " on rgb ")}`,
].join("\n");

/** A structured agent reply: every block kind the Markdown renderer has, as an agent would use them. */
export const MD_SUMMARY = `Here is where the change stands. The retry now lives in **one place**, \`GithubClient.post\`, and the jitter is *injectable*, so tests pass a fixed source. Background is in the [GitHub webhook docs](https://docs.github.com/webhooks).

### What changed

- \`isRetryable\` retries 5xx and 429 only
  - 4xx other than 429 fails at once, with the response body in the error
- Backoff doubles from 200 ms and caps at 3.2 s
- \`util/wait.ts\` is gone; everything sleeps through \`sleep.ts\`

1. Map the existing retry patterns
2. Add the bounded loop and the jitter seam
3. Fix the 502 test and rerun the suite

\`\`\`ts
export async function post<T>(req: GithubRequest, opts: RetryOptions = {}): Promise<T> {
  const { attempts = 5, jitter = Math.random } = opts;
  for (let i = 1; ; i++) {
    const res = await send(req);
    if (res.ok || !isRetryable(res) || i === attempts) return unwrap<T>(res);
    await sleep(backoff(i, jitter));
  }
}
\`\`\`

> The route's own retry still wraps this, so a lost delivery costs up to 15 calls. That is the decision I need from you below.

| Case | Calls | Outcome |
| --- | --- | --- |
| 502 then 200 | 2 | delivered |
| 502 every time | 5 | gives up |
| 404 | 1 | fails at once |`;

/** A short structured reply between tool calls. */
export const MD_SHORT = `Two things left before the PR:

- rerun \`bun run typecheck\` after the jitter change
- check the route's own retry in \`routes/webhooks.ts\``;
