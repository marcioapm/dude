/**
 * The token verifier's probes against a scripted GitHub: the URL push
 * discovery goes to, and answers that must never read as a grant or a
 * refusal.
 */

import { describe, expect, test } from "bun:test";
import { receivePackDiscoveryUrl, verifyRepositories, type RepositoryRow } from "../src/githubPermissions.ts";

describe("receivePackDiscoveryUrl", () => {
  test("goes to the configured GitHub's git host, never another origin", () => {
    expect(receivePackDiscoveryUrl("https://api.github.com", "https://github.com/acme/app.git"))
      .toEqual({ url: "https://github.com/acme/app.git/info/refs?service=git-receive-pack" });
    expect(receivePackDiscoveryUrl("https://api.github.com/", "git@github.com:acme/app.git"))
      .toEqual({ url: "https://github.com/acme/app.git/info/refs?service=git-receive-pack" });
    expect(receivePackDiscoveryUrl("https://ghe.example/api/v3", "ssh://git@ghe.example/acme/app/"))
      .toEqual({ url: "https://ghe.example/acme/app.git/info/refs?service=git-receive-pack" });
    for (const [base, repo] of [
      ["https://api.github.com", "https://evil.example/acme/app.git"],
      ["http://api.github.com", "http://github.com/acme/app.git"],
      ["https://ghe.example/other", "https://ghe.example/acme/app.git"],
      ["https://api.github.com", "https://github.com/acme/../app.git"],
      ["https://api.github.com", "https://user:pw@github.com/acme/app.git"],
      ["https://api.github.com", "git://github.com/acme/app.git"],
    ] as const) {
      expect({ base, repo, out: "error" in receivePackDiscoveryUrl(base, repo) }).toEqual({ base, repo, out: true });
    }
  });
});

const repo: RepositoryRow = { id: "repo_1", name: "app", url: "https://github.com/acme/app.git", defaultBranch: "main", projectName: "P" };

/** A GitHub that answers each request by its method and path, after the API base. */
function github(answer: (method: string, path: string) => Response) {
  const seen: string[] = [];
  const fetcher = async (input: string, init: RequestInit) => {
    const path = input.replace(/^https:\/\/(api\.)?github\.com/, "");
    seen.push(`${init.method} ${path}`);
    return answer(init.method ?? "GET", path);
  };
  return { fetcher, seen };
}

const ok = (body: unknown) => Response.json(body);
const permissionsOf = async (fetcher: (input: string, init: RequestInit) => Promise<Response>) =>
  Object.fromEntries((await verifyRepositories(fetcher, { secret: "github_pat_x", apiBaseUrl: "https://api.github.com", kind: "fine_grained", scopes: [] }, [repo]))[0]!
    .permissions.map((p) => [p.permission, p]));

describe("verifyRepositories", () => {
  test("a secondary rate limit (Retry-After) on check runs is untested, not missing", async () => {
    const { fetcher } = github((method, path) => {
      if (path === "/repos/acme/app") return ok({ private: true, default_branch: "main", owner: { type: "Organization" } });
      if (path.startsWith("/repos/acme/app/pulls?")) return ok([{ head: { sha: "abc" } }]);
      if (path.includes("/check-runs")) return Response.json({ message: "You have exceeded a secondary rate limit." }, { status: 403, headers: { "retry-after": "60" } });
      if (path.startsWith("/acme/app.git/info/refs")) return new Response("", { headers: { "content-type": "application/x-git-receive-pack-advertisement" } });
      if (method === "POST") return Response.json({ message: "Validation Failed" }, { status: 422 });
      return ok({ total_count: 0, workflow_runs: [] });
    });
    const checks = (await permissionsOf(fetcher))["Checks: Read"]!;
    expect([checks.outcome, checks.reason]).toEqual(["untested", "Rate limited: GitHub refused a listing of check runs until its limit resets."]);
  });

  test("an invalid POST answered other than 422 or a refusal is untested, and nothing but GETs and the two invalid POSTs is sent", async () => {
    const { fetcher, seen } = github((method, path) => {
      if (path === "/repos/acme/app") return ok({ private: true, default_branch: "main", owner: { type: "Organization" } });
      if (path.startsWith("/acme/app.git/info/refs")) return new Response("", { headers: { "content-type": "application/x-git-receive-pack-advertisement" } });
      if (method === "POST") return new Response("", { status: 502 });
      return ok([]);
    });
    const perms = await permissionsOf(fetcher);
    expect(perms["Pull requests: Read and write"]!.reason).toBe("Could not test: GitHub answered 502 to an empty pull request.");
    expect(perms["Pull requests: Read and write"]!.outcome).toBe("untested");
    expect(seen.filter((s) => !s.startsWith("GET "))).toEqual(["POST /repos/acme/app/pulls", "POST /repos/acme/app/hooks"]);
  });
});
