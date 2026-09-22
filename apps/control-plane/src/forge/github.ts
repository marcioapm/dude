/**
 * Git forge integration: push a branch, open a pull request, read its state.
 *
 * An interface rather than direct API calls, for two reasons. The first is the
 * one the plan names (§38): authentication will move from a personal access
 * token to a GitHub App minting short-lived installation tokens, and that
 * swap should not reach any caller. The second is that a forge is the one
 * dependency here that is genuinely remote — it rate-limits, it has outages,
 * and it is the thing a test must be able to replace.
 *
 * Nothing in this module talks to an agent, and the agent never sees a
 * credential: the runner asks the control plane for a token scoped to one
 * push, uses it, and discards it (plan §61).
 */

import { withOrg } from "../db/client.ts";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** How an organization authenticates to a forge. */
export interface ForgeCredential {
  organizationId: string;
  forge: "github";
  auth: "pat" | "github_app";
  secret: string;
  appId: string | null;
  installationId: string | null;
  apiBaseUrl: string;
}

export interface OpenPullRequestInput {
  /** `owner/repo`, as the forge names it. */
  slug: string;
  title: string;
  body: string;
  headBranch: string;
  baseBranch: string;
  /** A draft PR signals "not ready for review" without closing anything. */
  draft?: boolean;
}

export interface PullRequestRef {
  number: number;
  nodeId: string | null;
  url: string;
  state: "draft" | "open" | "merged" | "closed";
  headSha: string;
}

/**
 * Coarser than any one forge's vocabulary on purpose: the question a person
 * asks is "can this merge", not which of GitHub's twelve conclusion values
 * applies to the third of five check suites.
 */
export interface PullRequestStatus extends PullRequestRef {
  checks: "pending" | "passing" | "failing" | "unknown";
  review: "pending" | "approved" | "changes_requested";
}

/**
 * One piece of feedback left on a pull request by a person.
 *
 * Comments on the conversation, comments on a line, and the body of a review
 * that requested changes all arrive here in one shape, because to a fixer
 * they are the same thing: something a reviewer asked to be different.
 */
export interface PullRequestFeedback {
  id: string;
  author: string;
  body: string;
  /** The file a line comment is on; absent for conversation comments. */
  path?: string;
  createdAt: string;
  kind: "comment" | "line_comment" | "changes_requested";
}

export interface Forge {
  /**
   * A credential for one git operation, in a form git understands.
   *
   * Returned to the runner rather than stored on it: the runner holds no
   * long-lived forge credential, so a compromised node cannot push after its
   * lease ends.
   */
  pushToken(): Promise<string>;
  openPullRequest(input: OpenPullRequestInput): Promise<PullRequestRef>;
  getPullRequest(slug: string, number: number): Promise<PullRequestStatus>;
  /** Feedback left since `since`, oldest first. */
  listFeedback(slug: string, number: number, since: string | null): Promise<PullRequestFeedback[]>;
}

/** The forge rejected a request; `status` is its HTTP status. */
export class ForgeError extends Error {
  constructor(
    readonly status: number,
    override readonly message: string,
    readonly details?: unknown,
  ) {
    super(message);
    this.name = "ForgeError";
  }

  /** A PR already exists for this branch — normal on a re-run, not a failure. */
  get isAlreadyExists(): boolean {
    return this.status === 422;
  }
}

// ---------------------------------------------------------------------------
// GitHub
// ---------------------------------------------------------------------------

/** How long one forge request may take before it is abandoned. */
const FORGE_TIMEOUT_MS = Number(process.env.DUDE_FORGE_TIMEOUT_MS ?? 15_000);

export class GitHubForge implements Forge {
  constructor(private readonly credential: ForgeCredential) {}

  /**
   * With a PAT this is the token itself. With a GitHub App it will be a
   * freshly minted installation token, which is why callers must treat the
   * result as single-use and never cache it.
   */
  async pushToken(): Promise<string> {
    if (this.credential.auth === "pat") return this.credential.secret;
    throw new Error("github_app authentication is not implemented yet");
  }

  async #request<T>(method: string, path: string, body?: unknown): Promise<T> {
    const token = await this.pushToken();
    const res = await fetch(`${this.credential.apiBaseUrl}${path}`, {
      /*
       * Bounded. `fetch` waits forever by default, and the poller syncs PRs
       * one after another — so a single forge that stops answering would
       * stall every other organization's pull requests behind it. Found when
       * one test's forge went away and the next test's PR was never polled.
       */
      signal: AbortSignal.timeout(FORGE_TIMEOUT_MS),
      method,
      headers: {
        authorization: `Bearer ${token}`,
        accept: "application/vnd.github+json",
        "x-github-api-version": "2022-11-28",
        "content-type": "application/json",
      },
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    });

    const text = await res.text();
    const payload = text ? JSON.parse(text) : null;

    if (!res.ok) {
      throw new ForgeError(
        res.status,
        payload?.message ?? `${method} ${path} failed`,
        payload?.errors,
      );
    }
    return payload as T;
  }

  async openPullRequest(input: OpenPullRequestInput): Promise<PullRequestRef> {
    const pr = await this.#request<GitHubPullRequest>("POST", `/repos/${input.slug}/pulls`, {
      title: input.title,
      body: input.body,
      head: input.headBranch,
      base: input.baseBranch,
      draft: input.draft ?? false,
    });
    return toRef(pr);
  }

  async getPullRequest(slug: string, number: number): Promise<PullRequestStatus> {
    const pr = await this.#request<GitHubPullRequest>("GET", `/repos/${slug}/pulls/${number}`);

    /*
     * Two separate concepts on GitHub — the legacy commit status API and the
     * checks API — and a repository can use either. The combined status
     * rolls both up, which is the only reading that is correct regardless of
     * which one a project's CI uses.
     */
    const combined = await this.#request<{ state: string; total_count: number }>(
      "GET",
      `/repos/${slug}/commits/${pr.head.sha}/status`,
    );

    const reviews = await this.#request<GitHubReview[]>(
      "GET",
      `/repos/${slug}/pulls/${number}/reviews`,
    );

    return { ...toRef(pr), checks: checkState(combined), review: reviewState(reviews) };
  }

  /*
   * Three GitHub endpoints for what a fixer sees as one thing. Conversation
   * comments live on the issue, line comments on the pull, and a review that
   * requested changes carries its summary in the review body — a reviewer
   * who writes only "please rename this" in the review box would otherwise
   * be invisible.
   */
  async listFeedback(
    slug: string,
    number: number,
    since: string | null,
  ): Promise<PullRequestFeedback[]> {
    const sinceQuery = since ? `?since=${encodeURIComponent(since)}` : "";
    const [issueComments, lineComments, reviews] = await Promise.all([
      this.#request<GitHubComment[]>("GET", `/repos/${slug}/issues/${number}/comments${sinceQuery}`),
      this.#request<GitHubComment[]>("GET", `/repos/${slug}/pulls/${number}/comments${sinceQuery}`),
      this.#request<GitHubReview[]>("GET", `/repos/${slug}/pulls/${number}/reviews`),
    ]);

    const feedback: PullRequestFeedback[] = [
      ...issueComments.map((c) => ({
        id: `issue-comment-${c.id}`,
        author: c.user?.login ?? "unknown",
        body: c.body ?? "",
        createdAt: c.created_at,
        kind: "comment" as const,
      })),
      ...lineComments.map((c) => ({
        id: `line-comment-${c.id}`,
        author: c.user?.login ?? "unknown",
        body: c.body ?? "",
        ...(c.path ? { path: c.path } : {}),
        createdAt: c.created_at,
        kind: "line_comment" as const,
      })),
      ...reviews
        .filter((r) => r.state === "CHANGES_REQUESTED" && r.body && r.submitted_at)
        .map((r) => ({
          id: `review-${r.id}`,
          author: r.user?.login ?? "unknown",
          body: r.body ?? "",
          createdAt: r.submitted_at!,
          kind: "changes_requested" as const,
        })),
    ];

    /*
     * Inclusive of `since`, deliberately. GitHub timestamps are whole
     * seconds and its own `since` filter is inclusive, so a strict
     * comparison would drop a comment posted in the same second as the last
     * one seen. The caller dedupes by id instead, which is exact.
     */
    return feedback
      .filter((f) => !since || f.createdAt >= since)
      .sort((a, b) => a.createdAt.localeCompare(b.createdAt));
  }
}

interface GitHubComment {
  id: number;
  body: string | null;
  path?: string;
  created_at: string;
  user: { login: string } | null;
}

interface GitHubPullRequest {
  number: number;
  node_id: string;
  html_url: string;
  draft: boolean;
  state: string;
  merged_at: string | null;
  head: { sha: string };
}

interface GitHubReview {
  id?: number;
  body?: string | null;
  state: string;
  submitted_at: string | null;
  user: { login: string } | null;
}

function toRef(pr: GitHubPullRequest): PullRequestRef {
  return {
    number: pr.number,
    nodeId: pr.node_id,
    url: pr.html_url,
    state: pr.merged_at ? "merged" : pr.state === "closed" ? "closed" : pr.draft ? "draft" : "open",
    headSha: pr.head.sha,
  };
}

/** A repository with no CI configured reports "unknown", not "passing". */
function checkState(combined: { state: string; total_count: number }): PullRequestStatus["checks"] {
  if (combined.total_count === 0) return "unknown";
  switch (combined.state) {
    case "success":
      return "passing";
    case "failure":
    case "error":
      return "failing";
    default:
      return "pending";
  }
}

/**
 * The latest review per person decides, because a reviewer who requested
 * changes and then approved has approved.
 */
function reviewState(reviews: GitHubReview[]): PullRequestStatus["review"] {
  const latest = new Map<string, string>();
  for (const review of reviews) {
    const login = review.user?.login;
    // COMMENTED reviews express no verdict; counting them would let a
    // question overwrite an approval.
    if (!login || review.state === "COMMENTED") continue;
    latest.set(login, review.state);
  }

  const verdicts = [...latest.values()];
  if (verdicts.includes("CHANGES_REQUESTED")) return "changes_requested";
  if (verdicts.includes("APPROVED")) return "approved";
  return "pending";
}

// ---------------------------------------------------------------------------
// Resolution
// ---------------------------------------------------------------------------

/** The organization's forge credential, or null if it has none configured. */
export async function loadCredential(organizationId: string): Promise<ForgeCredential | null> {
  return withOrg(organizationId, async (scope) => {
    const rows = (await scope.sql`
      SELECT organization_id AS "organizationId", forge, auth, secret,
             app_id AS "appId", installation_id AS "installationId",
             api_base_url AS "apiBaseUrl"
      FROM forge_credentials WHERE forge = 'github' LIMIT 1`) as ForgeCredential[];
    return rows[0] ?? null;
  });
}

/** A forge client for an organization, or null when none is configured. */
export async function forgeFor(organizationId: string): Promise<Forge | null> {
  const credential = await loadCredential(organizationId);
  return credential ? new GitHubForge(credential) : null;
}

/**
 * `owner/repo` from a clone URL.
 *
 * Accepts both the HTTPS and SSH spellings, since a project is configured
 * with whichever its operator copied out of the forge.
 *
 * Returns null for anything that is not a remote URL. A local path is a
 * legitimate repository URL here — the local provisioner uses them — and
 * deriving a plausible-looking slug from one would send us to open a pull
 * request on a repository that does not exist.
 */
export function slugFromUrl(url: string): string | null {
  const remote = /^(?:https?:\/\/|ssh:\/\/|git:\/\/|[^@/\s]+@[^:/\s]+:)/.test(url);
  if (!remote) return null;

  const match = url.match(/[:/]([^/:]+)\/([^/]+?)(?:\.git)?\/?$/);
  return match ? `${match[1]}/${match[2]}` : null;
}
