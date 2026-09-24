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
