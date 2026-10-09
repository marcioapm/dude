/**
 * Artifacts: files agents publish for people — notes, a design, a report,
 * a screenshot, a recording — listed with the task that asked for them.
 *
 * The orchestrator records them as lux collects them; lux keeps the bytes.
 * Here a person lists them, with every version of each name (a resumed or
 * later Run that publishes the same name again adds a version), reads one,
 * or takes the latest of each as a zip — the bytes streamed from lux
 * through the orchestrator, which holds the lux key.
 */

import { withOrg } from "../../db/client.ts";
import { isSessionMember } from "../../events/visibility.ts";
import { orchestratorStream } from "../../orchestrator/client.ts";
import type { Principal } from "../auth.ts";
import { badRequest, HttpError, json, notFound } from "../http.ts";
import type { RequestContext, Router } from "../router.ts";
import { zipStream, type ZipEntry } from "../zip.ts";

/** Agents are careless with media types; a name is often the better word. */
const BY_EXTENSION: Record<string, string> = {
  html: "text/html", htm: "text/html",
  mp4: "video/mp4", m4v: "video/mp4", webm: "video/webm", mov: "video/quicktime", ogv: "video/ogg",
  png: "image/png", jpg: "image/jpeg", jpeg: "image/jpeg", gif: "image/gif", webp: "image/webp", svg: "image/svg+xml",
  md: "text/markdown", json: "application/json", pdf: "application/pdf",
};

/** Types that say nothing about what a file is. */
const GENERIC = new Set(["", "application/octet-stream", "binary/octet-stream", "text/plain"]);
const bare = (type: string) => type.split(";")[0]!.trim().toLowerCase();

/**
 * The type an artifact is listed and served as: what lux recorded, unless
 * that says nothing (octet-stream, or text/plain for a .html) and the name
 * does. The bytes have the last word when the name says nothing either
 * (sniffBytes).
 */
export function artifactType(recorded: string | null | undefined, name: string): string {
  const type = recorded ?? "";
  if (!GENERIC.has(bare(type))) return type;
  return BY_EXTENSION[name.toLowerCase().split(".").pop() ?? ""] ?? (type || "application/octet-stream");
}

/** A type from a file's first bytes, for the kinds a name may hide: video, and HTML. */
export function sniffBytes(head: Uint8Array): string | null {
  const ascii = (from: number, to: number) => String.fromCharCode(...head.subarray(from, to));
  if (head.length >= 12 && ascii(4, 8) === "ftyp") return ascii(8, 10) === "qt" ? "video/quicktime" : "video/mp4";
  if (head.length >= 4 && head[0] === 0x1a && head[1] === 0x45 && head[2] === 0xdf && head[3] === 0xa3) return "video/webm";
  const text = new TextDecoder().decode(head.subarray(0, 512)).trimStart().toLowerCase();
  if (text.startsWith("<!doctype html") || text.startsWith("<html")) return "text/html";
  return null;
}

/** A type a browser would run as a page — scripts and all — if it opened it. */
const isActive = (type: string) => /^(text\/html|application\/xhtml\+xml|image\/svg\+xml)$/.test(bare(type));

interface ArtifactRow {
  id: string;
  taskId: string | null;
  sessionId: string | null;
  runId: string;
  name: string;
  contentType: string;
  sizeBytes: number;
  sha256: string;
  epoch: number;
  createdAt: Date;
  phase: string | null;
  role: string | null;
  /** 1 for the first of its name, counting up. */
  version: number;
  /** How many of its name there are. */
  versions: number;
}

/** Whose files: a task's Runs', or a brainstorm session's. */
type Owner = { taskId: string } | { sessionId: string };

/** The artifacts of a task's or a session's Runs, newest first, each with its version among those of its name. */
async function artifactsOf(organizationId: string, owner: Owner): Promise<ArtifactRow[]> {
  const taskId = "taskId" in owner ? owner.taskId : null;
  const sessionId = "sessionId" in owner ? owner.sessionId : null;
  const rows = await withOrg(organizationId, async ({ sql }) => (await sql`
    SELECT a.id, r.task_id AS "taskId", r.session_id AS "sessionId", a.run_id AS "runId", a.name,
      a.content_type AS "contentType", a.size_bytes::float8 AS "sizeBytes", a.sha256, a.epoch,
      a.created_at AS "createdAt", r.phase::text AS phase, r.role::text AS role,
      row_number() OVER (PARTITION BY a.name ORDER BY a.created_at, a.epoch, a.id)::int AS version,
      count(*) OVER (PARTITION BY a.name)::int AS versions
    FROM artifacts a JOIN runs r ON r.id = a.run_id
    -- dude's own (the final diff its beforeStop hook leaves) is never a
    -- file for people; the collector keeps it out, and so does this.
    WHERE (r.task_id = ${taskId} OR r.session_id = ${sessionId}) AND a.name NOT LIKE '.dude-%'
    ORDER BY a.created_at DESC, a.epoch DESC, a.id DESC
    LIMIT 500`) as ArtifactRow[]);
  return rows.map((a) => ({ ...a, contentType: artifactType(a.contentType, a.name) }));
}

async function listArtifacts(ctx: RequestContext): Promise<Response> {
  const taskId = ctx.url.searchParams.get("taskId");
  const sessionId = ctx.url.searchParams.get("sessionId");
  const { organizationId, personId } = ctx.principal;
  if (sessionId && !taskId) {
    if (!(await isSessionMember(organizationId, personId, sessionId))) throw notFound(`session ${sessionId} not found`);
    return json({ artifacts: await artifactsOf(organizationId, { sessionId }) });
  }
  if (!taskId || sessionId) throw badRequest("taskId or sessionId is required, not both");
  return json({ artifacts: await artifactsOf(organizationId, { taskId }) });
}

/** An artifact's row, in the caller's organization; a session's Run's, for its members only. */
async function artifactNamed(organizationId: string, personId: string, id: string): Promise<{ name: string; contentType: string } | undefined> {
  return withOrg(organizationId, async ({ sql }) => {
    const [row] = (await sql`SELECT a.name, a.content_type AS "contentType" FROM artifacts a
      LEFT JOIN runs r ON r.id = a.run_id
      WHERE a.id = ${id} AND (r.session_id IS NULL OR session_role(r.session_id, ${personId}) IS NOT NULL)`) as Array<{
      name: string; contentType: string }>;
    return row;
  });
}

/**
 * An artifact's bytes from lux, through the orchestrator, as the person
 * asking: it checks a session Run's artifact against them too.
 */
function fetchContent(principal: Principal, id: string): Promise<Response> {
  return orchestratorStream(principal.organizationId, `/internal/artifacts/${encodeURIComponent(id)}/content`, principal);
}

/**
 * The bytes as the agent wrote them, never run as one of our pages: every
 * response is sandboxed by CSP (no scripts, an opaque origin), and one that
 * could be active — HTML, SVG — is an attachment unless the app asks for it
 * `?inline=1`, to show in an iframe that is sandboxed as well.
 */
async function artifactContent(ctx: RequestContext): Promise<Response> {
  const { organizationId } = ctx.principal;
  const id = ctx.params.id!;
  // Asked for first: one the caller may not see is never fetched.
  const artifact = await artifactNamed(organizationId, ctx.principal.personId, id);
  if (!artifact) throw notFound(`artifact ${id} not found`);
  const res = await fetchContent(ctx.principal, id);
  const headers = new Headers({
    "x-content-type-options": "nosniff",
    "content-security-policy": "sandbox; default-src 'none'; img-src data:; style-src 'unsafe-inline'",
  });
  for (const name of ["content-length", "x-content-sha256"]) {
    const value = res.headers.get(name);
    if (value) headers.set(name, value);
  }
  if (!res.ok || !res.body) {
    headers.set("content-type", res.headers.get("content-type") ?? "application/json");
    return new Response(res.body, { status: res.status, headers });
  }
  const name = artifact.name || "file";
  let type = artifactType(res.headers.get("content-type"), name);
  let body: ReadableStream<Uint8Array> = res.body;
  if (GENERIC.has(bare(type))) {
    const [head, rest] = await peek(res.body);
    type = sniffBytes(head) ?? type;
    body = rest;
  }
  headers.set("content-type", type);
  if (isActive(type) && ctx.url.searchParams.get("inline") !== "1") {
    headers.set("content-disposition", `attachment; filename*=UTF-8''${encodeURIComponent(name.split("/").pop() || "file")}`);
  }
  return new Response(body, { status: res.status, headers });
}

/** The first chunk of a stream, and the stream again from its start. */
async function peek(stream: ReadableStream<Uint8Array>): Promise<[Uint8Array, ReadableStream<Uint8Array>]> {
  const reader = stream.getReader();
  const first = await reader.read();
  const head = first.value ?? new Uint8Array();
  return [head, new ReadableStream<Uint8Array>({
    start(controller) {
      if (head.length > 0) controller.enqueue(head);
      if (first.done) controller.close();
    },
    async pull(controller) {
      const { done, value } = await reader.read();
      if (done) controller.close();
      else controller.enqueue(value);
    },
    cancel: (reason) => reader.cancel(reason),
  })];
}

/**
 * The latest version of each of a task's files, as one zip, streamed: each
 * file is read from lux only when the archive reaches it. A file lux no
 * longer keeps is left out and named in MISSING.txt, rather than failing
 * the whole download halfway.
 */
async function artifactsZip(ctx: RequestContext): Promise<Response> {
  const taskId = ctx.params.id!;
  const { organizationId } = ctx.principal;
  const key = await withOrg(organizationId, async ({ sql }) => {
    const [row] = (await sql`
      SELECT p.key_prefix || '-' || t.number AS key -- see navigation.ts
      FROM tasks t JOIN projects p ON p.id = t.project_id WHERE t.id = ${taskId}`) as Array<{ key: string }>;
    return row?.key;
  });
  if (!key) throw notFound(`task ${taskId} not found`);
  return zipOf(ctx.principal, await artifactsOf(organizationId, { taskId }), key);
}

/**
 * A brainstorm session's files as one zip, for its members alone. Named
 * by no title: a session's name stays in the session.
 */
async function sessionArtifactsZip(ctx: RequestContext): Promise<Response> {
  const sessionId = ctx.params.id!;
  const { organizationId, personId } = ctx.principal;
  if (!(await isSessionMember(organizationId, personId, sessionId))) throw notFound(`session ${sessionId} not found`);
  return zipOf(ctx.principal, await artifactsOf(organizationId, { sessionId }), "session");
}

async function zipOf(principal: Principal, artifacts: ArtifactRow[], name: string): Promise<Response> {
  const latest = artifacts.filter((a) => a.version === a.versions).sort((x, y) => x.name.localeCompare(y.name));

  async function* entries(): AsyncGenerator<ZipEntry> {
    const missing: string[] = [];
    for (const a of latest) {
      // The orchestrator unreachable throws rather than answers: that file
      // is missing too, not the end of the archive.
      const res = await fetchContent(principal, a.id).catch(
        (e: unknown) => new Response(null, { status: e instanceof HttpError ? e.status : 502 }),
      );
      if (!res.ok || !res.body) {
        missing.push(`${a.name}: ${res.status === 410 ? "no longer kept" : `unavailable (${res.status})`}`);
        await res.body?.cancel();
        continue;
      }
      yield { name: a.name, modified: new Date(a.createdAt), open: async () => res.body! };
    }
    if (missing.length > 0) {
      yield { name: "MISSING.txt", open: async () => new TextEncoder().encode(`Not in this archive:\n${missing.join("\n")}\n`) };
    }
  }
  return new Response(zipStream(entries()), {
    headers: {
      "content-type": "application/zip",
      "content-disposition": `attachment; filename="${name.replace(/[^\w.-]/g, "_")}-files.zip"`,
      "x-content-type-options": "nosniff",
    },
  });
}

export function registerArtifactRoutes(router: Router): void {
  router.get("/v1/artifacts", listArtifacts);
  router.get("/v1/artifacts/:id/content", artifactContent);
  router.get("/v1/tasks/:id/artifacts.zip", artifactsZip);
  router.get("/v1/brainstorms/:id/artifacts.zip", sessionArtifactsZip);
}
