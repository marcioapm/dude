#!/usr/bin/env bun
/**
 * Point the factory at a GitHub repository and run the PR loop against it.
 *
 * Creates a project whose repository is the given GitHub URL, stores the
 * forge credential, and creates a work item — so a runner can claim it, an
 * agent can commit, the runner can push, and a pull request can be opened.
 *
 *   GITHUB_TOKEN=ghp_… bun run scripts/seed-github.ts <owner/repo>
 *
 * Prints the ids and the API key, so the rest of the loop can be driven with
 * curl or from the UI.
 */

const [slug] = process.argv.slice(2);
if (!slug?.includes("/")) {
  console.error("usage: bun run scripts/seed-github.ts <owner/repo>");
  process.exit(1);
}

const token = process.env.GITHUB_TOKEN;
if (!token) {
  console.error("GITHUB_TOKEN is required");
  process.exit(1);
}

const API = process.env.DUDE_API ?? "http://localhost:3000";
const key = process.env.DUDE_KEY;
if (!key) {
  console.error("DUDE_KEY is required (an existing user API key)");
  process.exit(1);
}

const auth = { authorization: `Bearer ${key}`, "content-type": "application/json" };

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`${API}${path}`, {
    method,
    headers: auth,
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  });
  const text = await res.text();
  if (!res.ok) throw new Error(`${method} ${path} → ${res.status}: ${text}`);
  return text ? JSON.parse(text) : (null as T);
}

// The model is deliberately the scripted fake: this script exists to prove
// the *publishing* path, and a real model would make the test slow, costly
// and non-deterministic for no added coverage.
const model = process.env.DUDE_MODEL ?? "fake/scripted";

const project = await call<{ id: string; repositories: Array<{ id: string; name: string }> }>(
  "POST",
  "/v1/projects",
  {
    name: `GitHub (${slug})`,
    slug: `gh-${slug.replace(/[^a-z0-9]+/gi, "-").toLowerCase()}-${Date.now().toString(36)}`,
    agentModels: { orchestrator: { model } },
    repositories: [
      {
        name: "target",
        url: `https://github.com/${slug}.git`,
        defaultBranch: "main",
        trust: "trusted_internal",
      },
    ],
  },
);

await call("POST", "/v1/forge/credential", { auth: "pat", secret: token });

const workItem = await call<{ id: string }>("POST", "/v1/work-items", {
  projectId: project.id,
  title: "Prove the pull request loop works",
  goal: "Make a small change, push the branch, and open a pull request.",
});

console.log(
  JSON.stringify(
    {
      projectId: project.id,
      repositoryId: project.repositories[0]!.id,
      workItemId: workItem.id,
      slug,
    },
    null,
    2,
  ),
);
