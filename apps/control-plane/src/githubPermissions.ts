/**
 * What the organization's GitHub token may do on each of its repositories,
 * found without changing anything on GitHub: GETs, git receive-pack
 * discovery (which publishes nothing), and POSTs whose bodies are invalid by
 * construction. GitHub checks permission before validating a body, so a 422
 * to one of those means the permission is granted and nothing was created.
 *
 * Every result says why. A probe whose answer cannot tell granted from
 * missing is `untested`, never `ok`: GitHub answers a check-runs listing on
 * a commit with no runs 200 with none, even to a token that may not read
 * them, so only a commit that has runs decides Checks.
 */

export type Outcome = "ok" | "missing" | "untested";
export type Level = "required" | "optional";
export type TokenKind = "classic" | "fine_grained" | "unknown";

export interface PermissionResult {
  permission: string;
  level: Level;
  outcome: Outcome;
  reason: string;
}

export interface RepositoryPermissions {
  id: string;
  name: string;
  projectName: string;
  slug: string | null;
  /** Set when the repository could not be probed at all. */
  error?: string;
  permissions: PermissionResult[];
}

export interface RepositoryRow {
  id: string;
  name: string;
  url: string;
  defaultBranch: string;
  projectName: string;
}

export interface TokenUnderTest {
  secret: string;
  apiBaseUrl: string;
  kind: TokenKind;
  /** A classic token's scopes, from X-OAuth-Scopes; empty otherwise. */
  scopes: string[];
}

type Fetch = (input: string, init: RequestInit) => Promise<Response>;

const REQUEST_TIMEOUT_MS = 10_000;
const REPOSITORY_CONCURRENCY = 3;
// Commits whose check runs are read before giving up on finding one that has any.
const MAX_CHECK_CANDIDATES = 8;

export const PERMISSIONS = {
  metadata: "Metadata: Read",
  contents: "Contents: Read and write",
  pulls: "Pull requests: Read and write",
  statuses: "Commit statuses: Read",
  checks: "Checks: Read",
  hooks: "Webhooks: Read and write",
  workflows: "Workflows: Read and write",
  actions: "Actions: Read and write",
  members: "Members: Read (organization)",
} as const;

const REQUIRED = new Set<string>([PERMISSIONS.metadata, PERMISSIONS.contents, PERMISSIONS.pulls, PERMISSIONS.statuses, PERMISSIONS.checks]);

export const FINE_GRAINED_CHECKS =
  "GitHub refused to list check runs, and fine-grained tokens cannot be granted Checks: Read (GitHub offers no such permission for them): use a classic token with the repo scope, or a GitHub App once dude supports one.";

/** The token's kind: classic tokens report scopes on every answer; fine-grained ones start github_pat_. */
export function tokenKind(secret: string, scopesHeader: string | null): TokenKind {
  if (scopesHeader !== null) return "classic";
  return secret.startsWith("github_pat_") ? "fine_grained" : "unknown";
}

const remotePattern = /^(?:https?:\/\/|ssh:\/\/|git:\/\/|[^@/\s]+@[^:/\s]+:)/;
const slugPattern = /[:/]([^/:]+)\/([^/]+?)(?:\.git)?\/?$/;

/** `owner/repo` from a clone URL, or null for anything that is not a remote (as forge.SlugFromURL). */
export function slugFromUrl(url: string): string | null {
  if (!remotePattern.test(url)) return null;
  const m = slugPattern.exec(url);
  return m ? `${m[1]}/${m[2]}` : null;
}

/**
 * Where git's receive-pack discovery for a repository is, on the configured
 * GitHub's origin — the orchestrator's CheckPushAccess, ported. The token is
 * sent there, so a repository on any other origin is refused, never contacted.
 */
export function receivePackDiscoveryUrl(apiBaseUrl: string, repository: string): { url: string } | { error: string } {
  let base: URL;
  try {
    base = new URL(apiBaseUrl);
  } catch {
    return { error: "the GitHub API URL is not a URL" };
  }
  if (!base.host || base.username || base.password || base.search || base.hash || (base.protocol !== "https:" && base.protocol !== "http:")) {
    return { error: "the GitHub API URL is not a plain http(s) origin" };
  }
  const basePath = base.pathname.replace(/\/+$/, "");
  if (basePath !== "" && basePath !== "/api/v3") return { error: "the GitHub API URL must be an origin or end in /api/v3" };
  let origin = `${base.protocol}//${base.host}`;
  let host = base.host;
  if (base.host === "api.github.com") {
    if (base.protocol !== "https:") return { error: "GitHub requires HTTPS" };
    origin = "https://github.com";
    host = "github.com";
  }
  const hostname = host.replace(/:\d+$/, "");
  let repo = repository;
  if (repo.startsWith("git@")) {
    const rest = repo.slice("git@".length);
    const colon = rest.indexOf(":");
    if (colon < 0 || rest.slice(0, colon).toLowerCase() !== hostname.toLowerCase() || host !== hostname) {
      return { error: "the repository's SSH host is not the configured GitHub" };
    }
    repo = `${origin}/${rest.slice(colon + 1)}`;
  }
  let clone: URL;
  try {
    clone = new URL(repo);
  } catch {
    return { error: "the repository URL is not a URL" };
  }
  if (clone.protocol === "ssh:" && clone.username === "git" && !clone.password && clone.host.toLowerCase() === host.toLowerCase()) {
    clone = new URL(`${origin}${clone.pathname}`);
  }
  if (clone.username || clone.password || clone.search || clone.hash || !clone.host) return { error: "the repository URL carries more than a repository" };
  // The test suite's git daemon serves on another port of the API's loopback host.
  const loopback = base.hostname === "127.0.0.1" || base.hostname === "localhost";
  const localGit = clone.protocol === "git:" && base.protocol === "http:" && loopback && clone.hostname === base.hostname;
  if (!localGit && (clone.protocol !== new URL(origin).protocol || clone.host.toLowerCase() !== host.toLowerCase())) {
    return { error: "the repository is not on the configured GitHub" };
  }
  const slug = clone.pathname.replace(/^\//, "").replace(/\/$/, "").replace(/\.git$/, "");
  if (!/^[A-Za-z0-9_-]+\/[A-Za-z0-9_.-]+$/.test(slug) || slug.endsWith("/.") || slug.endsWith("/..")) {
    return { error: "the repository URL is not owner/repository" };
  }
  return { url: `${origin}/${slug}.git/info/refs?service=git-receive-pack` };
}

interface Answer {
  /** 0: no answer at all. */
  status: number;
  headers: Headers;
  body: unknown;
  failure?: string;
}

async function call(fetcher: Fetch, url: string, init: RequestInit, readBody = true): Promise<Answer> {
  try {
    const res = await fetcher(url, { ...init, redirect: "manual", signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS) });
    let body: unknown = null;
    if (readBody) {
      body = await res.json().catch(() => null);
    } else {
      await res.body?.cancel().catch(() => undefined);
    }
    return { status: res.status, headers: res.headers, body };
  } catch (err) {
    return { status: 0, headers: new Headers(), body: null, failure: err instanceof Error ? err.name : String(err) };
  }
}

/** A 403 GitHub marks as a rate limit only by its headers, or a 429. */
function rateLimited(a: Answer): boolean {
  if (a.status === 429) return true;
  return a.status === 403 && (a.headers.get("x-ratelimit-remaining") === "0" || a.headers.get("retry-after") !== null);
}

function wants(a: Answer): string {
  const accepted = a.headers.get("x-accepted-github-permissions");
  return accepted ? ` (GitHub accepts ${accepted})` : "";
}

/** What an answer that is not a plain grant or refusal says, as an untested reason; null when it is one. */
function inconclusive(a: Answer, what: string): string | null {
  if (a.status === 0) return `GitHub did not answer ${what}${a.failure ? ` (${a.failure})` : ""}.`;
  if (rateLimited(a)) return `Rate limited: GitHub refused ${what} until its limit resets.`;
  return null;
}

const refused = (a: Answer) => a.status === 401 || a.status === 403 || a.status === 404;

function result(permission: string, outcome: Outcome, reason: string): PermissionResult {
  return { permission, level: REQUIRED.has(permission) ? "required" : "optional", outcome, reason };
}

/** A GET's verdict: 2xx granted, 401/403/404 refused, anything else unknown. */
function judgeRead(permission: string, a: Answer, what: string, missing: string): PermissionResult {
  const unknown = inconclusive(a, what);
  if (unknown) return result(permission, "untested", unknown);
  if (a.status >= 200 && a.status < 300) return result(permission, "ok", `GitHub answered ${what}.`);
  if (refused(a)) return result(permission, "missing", `${missing}${wants(a)}.`);
  return result(permission, "untested", `Could not test: GitHub answered ${a.status} to ${what}.`);
}

/** An invalid POST's verdict: 422 means the permission check passed. */
function judgeInvalidPost(permission: string, a: Answer, what: string, missing: string): PermissionResult {
  const unknown = inconclusive(a, what);
  if (unknown) return result(permission, "untested", unknown);
  if (a.status === 422) return result(permission, "ok", `GitHub validated ${what}, which it does only for a token allowed to make it.`);
  if (refused(a)) return result(permission, "missing", `${missing}${wants(a)}.`);
  return result(permission, "untested", `Could not test: GitHub answered ${a.status} to ${what}.`);
}

/** Run `work` on every item, at most `limit` at once, keeping the order. */
async function mapLimit<T, R>(items: readonly T[], limit: number, work: (item: T) => Promise<R>): Promise<R[]> {
  const out = new Array<R>(items.length);
  let next = 0;
  const lanes = Array.from({ length: Math.min(limit, items.length) }, async () => {
    while (next < items.length) {
      const i = next++;
      out[i] = await work(items[i]!);
    }
  });
  await Promise.all(lanes);
  return out;
}

export async function verifyRepositories(fetcher: Fetch, token: TokenUnderTest, repositories: readonly RepositoryRow[]): Promise<RepositoryPermissions[]> {
  return mapLimit(repositories, REPOSITORY_CONCURRENCY, (repo) => verifyRepository(fetcher, token, repo));
}

async function verifyRepository(fetcher: Fetch, token: TokenUnderTest, repo: RepositoryRow): Promise<RepositoryPermissions> {
  const slug = slugFromUrl(repo.url);
  const shown = { id: repo.id, name: repo.name, projectName: repo.projectName, slug };
  if (!slug) return { ...shown, error: "Its URL names no GitHub repository, so it was not tested.", permissions: [] };

  const apiBase = token.apiBaseUrl.replace(/\/+$/, "");
  const headers = { authorization: `Bearer ${token.secret}`, accept: "application/vnd.github+json" };
  const get = (path: string) => call(fetcher, `${apiBase}/repos/${slug}${path}`, { method: "GET", headers });
  const postInvalid = (path: string, body: unknown) =>
    call(fetcher, `${apiBase}/repos/${slug}${path}`, { method: "POST", headers: { ...headers, "content-type": "application/json" }, body: JSON.stringify(body) });
  const classic = token.kind === "classic";
  const scope = (...names: string[]) => names.some((n) => token.scopes.includes(n));
  const out: PermissionResult[] = [];

  const meta = await get("");
  const metadata = judgeRead(PERMISSIONS.metadata, meta, "a read of the repository",
    "The token cannot see this repository: it is not in the token's repository selection, the owner lacks access, or the organization requires SSO authorization or token approval");
  out.push(metadata);
  if (metadata.outcome !== "ok") {
    const why = metadata.outcome === "missing" ? "Not tested: the token cannot see the repository." : "Not tested: the repository could not be read.";
    for (const p of Object.values(PERMISSIONS)) if (p !== PERMISSIONS.metadata) out.push(result(p, "untested", why));
    return { ...shown, permissions: out };
  }
  const info = (meta.body ?? {}) as { private?: boolean; default_branch?: string; owner?: { login?: string; type?: string } };
  const isPrivate = info.private !== false;
  // public_repo covers public repositories only.
  const repoScope = scope("repo") || (!isPrivate && scope("public_repo"));
  const noRepoScope = `The classic token has no repo scope${scope("public_repo") ? " (public_repo covers public repositories only)" : ""}.`;

  out.push(await pushAccess(fetcher, token, repo.url, classic && !repoScope ? noRepoScope : null));

  const pulls = await get("/pulls?state=all&per_page=10");
  const pullsRead = judgeRead(PERMISSIONS.pulls, pulls, "a listing of pull requests", "GitHub refused to list pull requests");
  if (pullsRead.outcome !== "ok") {
    out.push(pullsRead);
  } else if (classic) {
    out.push(repoScope ? result(PERMISSIONS.pulls, "ok", "Pull requests list, and the classic token's repo scope lets it open them.") : result(PERMISSIONS.pulls, "missing", noRepoScope));
  } else {
    // No head or base: GitHub refuses it as invalid once the token may open pull requests.
    const write = judgeInvalidPost(PERMISSIONS.pulls, await postInvalid("/pulls", {}), "an empty pull request", "GitHub refused to open a pull request");
    out.push(write.outcome === "ok" ? { ...write, reason: "Pull requests list, and GitHub validated an empty one (base and head missing), which it does only for a token allowed to open them." } : write);
  }

  const heads = Array.isArray(pulls.body) ? (pulls.body as Array<{ head?: { sha?: string } }>).map((p) => p.head?.sha).filter((s): s is string => Boolean(s)) : [];
  const statusRef = heads[0] ?? info.default_branch ?? repo.defaultBranch;
  out.push(judgeRead(PERMISSIONS.statuses, await get(`/commits/${encodeURIComponent(statusRef)}/status`), "a read of a commit's combined status",
    "GitHub refused to read commit statuses"));

  out.push(await checksRead(get, token, heads, repoScope));

  const hooksRead = await get("/hooks?per_page=1");
  const hooks = judgeRead(PERMISSIONS.hooks, hooksRead, "a listing of webhooks",
    "GitHub refused to list webhooks: the token lacks the permission, or its owner is not an admin of the repository");
  if (hooks.outcome !== "ok") {
    out.push(hooks);
  } else if (classic) {
    out.push(repoScope || scope("admin:repo_hook", "write:repo_hook")
      ? result(PERMISSIONS.hooks, "ok", "Webhooks list, and the classic token's scopes let it register one.")
      : result(PERMISSIONS.hooks, "missing", "The classic token has neither repo nor admin:repo_hook."));
  } else {
    // A hook with no URL: invalid, so nothing is registered.
    const write = judgeInvalidPost(PERMISSIONS.hooks, await postInvalid("/hooks", { config: {} }), "a webhook with no URL", "GitHub refused to register a webhook");
    out.push(write.outcome === "ok" ? { ...write, reason: "Webhooks list, and GitHub validated one with no URL, which it does only for a token allowed to register them." } : write);
  }

  if (classic) {
    out.push(scope("workflow")
      ? result(PERMISSIONS.workflows, "ok", "The classic token has the workflow scope.")
      : result(PERMISSIONS.workflows, "missing", "The classic token has no workflow scope, so pushing changes under .github/workflows/ fails."));
    out.push(repoScope
      ? result(PERMISSIONS.actions, "ok", "The classic token's repo scope lets it re-run jobs.")
      : result(PERMISSIONS.actions, "missing", noRepoScope));
  } else {
    out.push(result(PERMISSIONS.workflows, "untested", "Not tested: only pushing a workflow file would tell, and that changes the repository."));
    out.push(result(PERMISSIONS.actions, "untested", "Not tested: only re-running a job would tell, and that changes the repository."));
  }

  if (info.owner?.type === "User") {
    out.push(result(PERMISSIONS.members, "untested", "Not tested: a user account owns this repository, so there are no organization members to read."));
  } else if (classic) {
    out.push(scope("read:org", "write:org", "admin:org")
      ? result(PERMISSIONS.members, "ok", "The classic token has the read:org scope.")
      : result(PERMISSIONS.members, "missing", "The classic token has no read:org scope, so private organization membership cannot be checked."));
  } else {
    out.push(result(PERMISSIONS.members, "untested", "Not tested: GitHub shows public members to any token, so a read cannot tell."));
  }
  return { ...shown, permissions: out };
}

async function pushAccess(fetcher: Fetch, token: TokenUnderTest, url: string, scopeMissing: string | null): Promise<PermissionResult> {
  const p = PERMISSIONS.contents;
  if (scopeMissing) return result(p, "missing", scopeMissing);
  const target = receivePackDiscoveryUrl(token.apiBaseUrl, url);
  if ("error" in target) return result(p, "untested", `Not tested: ${target.error}.`);
  const basic = Buffer.from(`x-access-token:${token.secret}`).toString("base64");
  const a = await call(fetcher, target.url, { method: "GET", headers: { authorization: `Basic ${basic}` } }, false);
  const what = "git's push discovery";
  const unknown = inconclusive(a, what);
  if (unknown) return result(p, "untested", unknown);
  if (a.status === 200) {
    const type = (a.headers.get("content-type") ?? "").split(";")[0]!.trim();
    return type === "application/x-git-receive-pack-advertisement"
      ? result(p, "ok", "Git's push discovery succeeded (nothing was pushed).")
      : result(p, "untested", "Could not test: push discovery returned no Git advertisement (possibly an authentication gateway).");
  }
  if (refused(a)) return result(p, "missing", "Git refused push discovery: the token cannot push to this repository.");
  return result(p, "untested", `Could not test: GitHub answered ${a.status} to ${what}.`);
}

/**
 * Checks: Read, on the first recent commit that has check runs — pull
 * request heads, then the head of each recent Actions run. A 0-count answer
 * decides nothing, so a repository with no such commit is untested.
 */
async function checksRead(get: (path: string) => Promise<Answer>, token: TokenUnderTest, heads: string[], repoScope: boolean): Promise<PermissionResult> {
  const p = PERMISSIONS.checks;
  const candidates = [...heads];
  // Only a source of commits: a token that may not read Actions runs simply offers none.
  const runs = await get("/actions/runs?per_page=10");
  if (runs.status === 200) {
    for (const r of ((runs.body as { workflow_runs?: Array<{ head_sha?: string }> })?.workflow_runs ?? [])) {
      if (r.head_sha) candidates.push(r.head_sha);
    }
  }
  const unique = [...new Set(candidates)].slice(0, MAX_CHECK_CANDIDATES);
  for (const sha of unique) {
    const a = await get(`/commits/${sha}/check-runs?per_page=1`);
    const unknown = inconclusive(a, "a listing of check runs");
    if (unknown) return result(p, "untested", unknown);
    if (refused(a)) {
      if (token.kind === "classic") {
        return result(p, "missing", repoScope
          ? `GitHub refused to list check runs although the classic token has the repo scope: authorize it for the organization's SSO and check its owner's access${wants(a)}.`
          : `GitHub refused to list check runs: the classic token needs the repo scope${wants(a)}.`);
      }
      return result(p, "missing", FINE_GRAINED_CHECKS);
    }
    if (a.status !== 200) return result(p, "untested", `Could not test: GitHub answered ${a.status} to a listing of check runs.`);
    const count = (a.body as { total_count?: number } | null)?.total_count ?? 0;
    if (count > 0) return result(p, "ok", "GitHub listed the check runs of a recent commit.");
  }
  return result(p, "untested", "Could not test: no recent commit has check runs, and GitHub lists none to any token.");
}
