/**
 * Images: the organization's image library.
 *
 * An image is a name and a history of Containerfiles (migration 068).
 * Saving edits its one draft; Build & publish numbers the draft and queues
 * it for dude-image-builder, which builds, pushes and finishes it and only
 * then publishes it. Publishing an older version again needs no build: its
 * image is still in the registry. Everything that runs in an image names it
 * by id (a role, a project's runtime and previews, the organization's
 * default base) and gets its published version on its next Run.
 *
 * Anyone in the organization reads; its admins change. The Containerfile
 * lint the editor runs (@dude/domain lintContainerfile) is run again here
 * on every save, and an error refuses it; so does a FROM image:<name> that
 * would build an image on its own descendant.
 */

import {
  BUILDER_OFFLINE_SECONDS,
  EventTypes,
  firstFrom,
  fromImage,
  imageCycle,
  imageDraftSchema,
  imagePatchSchema,
  imageReferences,
  lintContainerfile,
  newId,
  newImageSchema,
  type AgentModels,
  type ImageBuild,
  type ImageBuilderInfo,
  type ImageBuildWithLog,
  type ImageChoice,
  type ImageDetail,
  type ImageDraftInput,
  type NewImageInput,
  type ImagesResponse,
  type ImageSummary,
  type ImageUse,
  type ImageVersion,
} from "@dude/domain";
import { SQL } from "bun";
import { config } from "../../config.ts";
import { withOrg, type OrgScope } from "../../db/client.ts";
import { appendInScope } from "../../events/ledger.ts";
import { isOrgAdmin, requireOrgAdmin } from "../access.ts";
import { auditActor } from "../auth.ts";
import { badRequest, conflict, HttpError, json, notFound, parseBody } from "../http.ts";
import type { RequestContext, Router } from "../router.ts";

type Json = Record<string, unknown>;

/** The builder as configured, and whether it is alive: builds run only with a dude layer to finish them with. */
export async function builderInfo(scope: OrgScope): Promise<ImageBuilderInfo> {
  const c = config();
  const layer = c.string("DUDE_LAYER_IMAGE") ?? null;
  const memory = c.string("DUDE_BUILDER_MEMORY") ?? "1536m";
  const [beat] = (await scope.sql`
    SELECT seen_at AS "seenAt", seen_at < now() - make_interval(secs => ${BUILDER_OFFLINE_SECONDS}) AS stale FROM image_builder`) as Array<{ seenAt: string; stale: boolean }>;
  return {
    available: layer !== null, layer, cpus: c.float("DUDE_BUILDER_CPUS"), memoryMiB: mebibytes(memory),
    lastSeenAt: beat?.seenAt ?? null, offline: layer !== null && (beat?.stale ?? true),
  };
}

/** Whether builds can run at all, without a database read. */
const buildsConfigured = () => Boolean(config().string("DUDE_LAYER_IMAGE"));

/**
 * The builder's memory setting in MiB: whole bytes or a whole number of
 * k, m or g, the notation dude-image-builder validates (images.MemoryBytes).
 */
function mebibytes(s: string): number {
  const m = /^(\d+)([bkmg]?)$/i.exec(s.trim());
  if (!m) return 0;
  const shift = { "": 0, b: 0, k: 10, m: 20, g: 30 }[(m[2] ?? "").toLowerCase() as "" | "b" | "k" | "m" | "g"];
  return Math.round((Number(m[1]) * 2 ** shift) / 2 ** 20);
}

/** The person `column` names, as `{id, name}`, read through `alias`. */
const person = (alias: string, column: string) =>
  `(SELECT json_build_object('id', ${alias}.id, 'name', ${alias}.name) FROM people ${alias} WHERE ${alias}.id = ${column})`;

// An image's summary, over `images i` in scope and `organizations o`.
const SUMMARY_COLUMNS = `
  i.id, i.name, i.description, i.archived_at AS "archivedAt", i.created_at AS "createdAt",
  ${person("cp", "i.created_by")} AS "createdBy",
  (i.id IS NOT DISTINCT FROM o.default_image_id) AS "isDefault",
  (SELECT json_build_object('versionId', v.id, 'number', v.number, 'builtAt', v.built_at, 'userRef', v.user_ref,
     'canRunContainers', v.can_run_containers)
   FROM image_versions v WHERE v.id = i.published_version_id) AS published,
  (SELECT json_build_object('versionId', v.id, 'number', v.number, 'state', v.state, 'error', v.error)
   FROM image_versions v WHERE v.image_id = i.id AND v.number IS NOT NULL
     AND v.number > COALESCE((SELECT p.number FROM image_versions p WHERE p.id = i.published_version_id), 0)
     AND v.state IN ('queued', 'building', 'pushing', 'failed')
   ORDER BY v.number DESC LIMIT 1) AS pending,
  (SELECT json_build_object('versionId', v.id, 'updatedAt', v.updated_at, 'updatedBy', ${person("dp", "v.created_by")})
   FROM image_versions v WHERE v.image_id = i.id AND v.state = 'draft') AS draft,
  (SELECT COALESCE(json_agg(json_build_object('id', pi.id, 'name', pi.name) ORDER BY pi.name), '[]')
   FROM image_version_parents vp JOIN images pi ON pi.id = vp.parent_image_id
   WHERE vp.version_id = latest.id) AS parents,
  latest.containerfile AS "latestContainerfile",
  json_build_object('at', latest.updated_at, 'by', ${person("lp", "latest.created_by")}, 'source', latest.source) AS "lastChange"`;

// The version whose Containerfile an image is now: its draft, else its newest.
const SUMMARY_FROM = `
  images i JOIN organizations o ON o.id = i.organization_id
  LEFT JOIN LATERAL (SELECT * FROM image_versions v WHERE v.image_id = i.id
    ORDER BY (v.state = 'draft') DESC, v.number DESC NULLS LAST LIMIT 1) latest ON true`;

type SummaryRow = Omit<ImageSummary, "from" | "usedBy" | "lastChange"> & {
  latestContainerfile: string | null;
  lastChange: { at: string | null; by: ImageSummary["lastChange"]["by"]; source: ImageSummary["lastChange"]["source"] | null };
};

async function summaries(scope: OrgScope, id?: string): Promise<ImageSummary[]> {
  const rows = (await scope.sql`
    SELECT ${scope.sql.unsafe(SUMMARY_COLUMNS)} FROM ${scope.sql.unsafe(SUMMARY_FROM)}
    WHERE (${id ?? null}::text IS NULL OR i.id = ${id ?? null})
    ORDER BY i.archived_at IS NOT NULL, i.name`) as SummaryRow[];
  const uses = await usage(scope);
  return rows.map(({ latestContainerfile, lastChange, ...r }) => ({
    ...r,
    from: latestContainerfile ? firstFrom(latestContainerfile) : null,
    usedBy: uses.get(r.id) ?? [],
    lastChange: { at: lastChange.at ?? r.createdAt, by: lastChange.by ?? r.createdBy, source: lastChange.source ?? "person" },
  }));
}

/**
 * Who names each image: the organization's default base, each role (the
 * organization's and each project's — a fixer that takes the implementer's
 * is not listed again), each project's runtime and previews, and the
 * images whose published version is built FROM it.
 */
async function usage(scope: OrgScope): Promise<Map<string, ImageUse[]>> {
  const out = new Map<string, ImageUse[]>();
  const add = (id: unknown, use: ImageUse) => {
    if (typeof id !== "string") return;
    out.set(id, [...(out.get(id) ?? []), use]);
  };
  const [org] = (await scope.sql`
    SELECT default_image_id AS "defaultImageId", default_agent_models AS models FROM organizations WHERE id = ${scope.organizationId}`) as Array<{
    defaultImageId: string | null;
    models: AgentModels;
  }>;
  add(org?.defaultImageId, { kind: "organization_default" });
  for (const [role, c] of Object.entries((org?.models ?? {}) as Record<string, { image?: string }>)) add(c?.image, { kind: "role", role });
  const projects = (await scope.sql`
    SELECT id, name, agent_models AS models, runtime_image_id AS "runtimeImageId", preview_image_id AS "previewImageId"
    FROM projects ORDER BY name`) as Array<{ id: string; name: string; models: AgentModels; runtimeImageId: string | null; previewImageId: string | null }>;
  for (const p of projects) {
    const project = { id: p.id, name: p.name };
    for (const [role, c] of Object.entries((p.models ?? {}) as Record<string, { image?: string }>)) add(c?.image, { kind: "project_role", role, project });
    add(p.runtimeImageId, { kind: "runtime", project });
    add(p.previewImageId, { kind: "preview", project });
  }
  const children = (await scope.sql`
    SELECT vp.parent_image_id AS parent, c.id, c.name FROM images c
    JOIN image_version_parents vp ON vp.version_id = c.published_version_id
    ORDER BY c.name`) as Array<{ parent: string; id: string; name: string }>;
  for (const c of children) add(c.parent, { kind: "child", image: { id: c.id, name: c.name } });
  return out;
}

const BUILD_COLUMNS = `
  b.id, i.id AS "imageId", i.name AS "imageName", v.id AS "versionId", v.number AS version, b.kind, b.state, b.stage,
  b.layer_ref AS "layerRef", ${person("rp", "b.requested_by")} AS "requestedBy", b.requested_at AS "requestedAt",
  b.started_at AS "startedAt", b.finished_at AS "finishedAt", b.error, b.build_seconds AS "buildSeconds",
  b.push_seconds AS "pushSeconds", v.can_run_containers AS "canRunContainers", b.containers_check AS "containersCheck",
  b.check_seconds AS "checkSeconds", CASE WHEN b.state = 'queued' THEN image_queue_ahead(b.id) END AS ahead`;
const BUILD_FROM = `image_builds b JOIN image_versions v ON v.id = b.image_version_id JOIN images i ON i.id = v.image_id`;

/** The organization's running and waiting jobs, in the builder's order. */
async function queue(scope: OrgScope): Promise<ImageBuild[]> {
  return (await scope.sql`
    SELECT ${scope.sql.unsafe(BUILD_COLUMNS)} FROM ${scope.sql.unsafe(BUILD_FROM)}
    WHERE b.state IN ('queued', 'running')
    ORDER BY b.state = 'running' DESC, b.kind = 'build', b.requested_at, b.id`) as ImageBuild[];
}

async function imagesResponse(ctx: RequestContext): Promise<ImagesResponse> {
  return withOrg(ctx.principal.organizationId, async (scope) => {
    const [org] = (await scope.sql`SELECT default_image_id AS id FROM organizations WHERE id = ${scope.organizationId}`) as Array<{ id: string | null }>;
    return {
      images: await summaries(scope),
      queue: await queue(scope),
      defaultImageId: org?.id ?? null,
      builder: await builderInfo(scope),
      canEdit: await isOrgAdmin(ctx),
    };
  });
}

const VERSION_COLUMNS = `
  v.id, v.image_id AS "imageId", v.number, v.state, v.containerfile, v.build_args AS "buildArgs", v.note, v.source,
  v.created_at AS "createdAt", v.updated_at AS "updatedAt", ${person("vp_", "v.created_by")} AS "createdBy",
  v.user_ref AS "userRef", v.built_at AS "builtAt", v.error, v.can_run_containers AS "canRunContainers",
  (SELECT COALESCE(json_agg(json_build_object('imageId', p.parent_image_id, 'name', pi.name, 'versionId', p.parent_version_id,
     'version', pv.number) ORDER BY pi.name), '[]')
   FROM image_version_parents p JOIN images pi ON pi.id = p.parent_image_id
   LEFT JOIN image_versions pv ON pv.id = p.parent_version_id WHERE p.version_id = v.id) AS parents`;

async function detail(ctx: RequestContext, id: string): Promise<ImageDetail> {
  return withOrg(ctx.principal.organizationId, async (scope) => {
    const [image] = await summaries(scope, id);
    if (!image) throw notFound(`no image ${id}`);
    const versions = (await scope.sql`
      SELECT ${scope.sql.unsafe(VERSION_COLUMNS)} FROM image_versions v WHERE v.image_id = ${id}
      ORDER BY (v.state = 'draft') DESC, v.number DESC`) as ImageVersion[];
    const builds = (await scope.sql`
      SELECT ${scope.sql.unsafe(BUILD_COLUMNS)} FROM ${scope.sql.unsafe(BUILD_FROM)}
      WHERE i.id = ${id} ORDER BY b.requested_at DESC, b.id DESC LIMIT 100`) as ImageBuild[];
    return { image, versions, builds, builder: await builderInfo(scope), canEdit: await isOrgAdmin(ctx) };
  });
}

/**
 * A build with its log. `?after=<n>`, n the logTotal a reader already has:
 * only the bytes after it, when the kept log still starts at or before n;
 * else the whole kept log. logStart says which.
 */
async function buildDetail(ctx: RequestContext): Promise<Response> {
  const id = ctx.params.buildId!;
  const raw = new URL(ctx.request.url).searchParams.get("after");
  const after = raw !== null && /^\d{1,15}$/.test(raw) ? Number(raw) : null;
  if (raw !== null && after === null) throw badRequest("after must be a byte count");
  const build = await withOrg(ctx.principal.organizationId, async (scope) => {
    const row = await readBuild(scope, id, after);
    if (!row) return null;
    const whole = (r: BuildRow) => ({ log: r.chunks.map((c) => c.chunk).join(""), logStart: r.keptStart ?? r.logTotal });
    let out = { row, ...whole(row) };
    if (after !== null && after >= out.logStart && after <= row.logTotal) {
      // The chunks that end after n: the first cut at n, which must be a
      // rune's first byte (a logTotal a reader was given always is).
      const [first, ...rest] = row.chunks;
      const bytes = first ? new TextEncoder().encode(first.chunk) : new Uint8Array();
      const cut = first ? after - first.start : 0;
      if (cut < bytes.length && ((bytes[cut] ?? 0) & 0xc0) === 0x80) {
        const all = (await readBuild(scope, id, null))!;
        out = { row: all, ...whole(all) };
      } else {
        out = { row, log: new TextDecoder().decode(bytes.subarray(cut)) + rest.map((c) => c.chunk).join(""), logStart: after };
      }
    }
    const { keptStart: _k, chunks: _c, ...fields } = out.row;
    return { ...fields, log: out.log, logStart: out.logStart, builder: await builderInfo(scope) } satisfies ImageBuildWithLog;
  });
  if (!build) throw notFound(`no image build ${id}`);
  return json(build);
}

type BuildRow = Omit<ImageBuildWithLog, "log" | "logStart" | "builder"> & {
  keptStart: number | null;
  chunks: Array<{ start: number; chunk: string }>;
};

/**
 * The build and, in one statement (one snapshot, so they agree with its
 * logTotal), its kept log's first offset and its chunks in order: those
 * ending after `after` when the kept log reaches back to it, else all.
 */
async function readBuild(scope: OrgScope, id: string, after: number | null): Promise<BuildRow | undefined> {
  const [row] = (await scope.sql`
    WITH kept AS (SELECT min(start_offset) AS start FROM image_build_log WHERE build_id = ${id})
    SELECT ${scope.sql.unsafe(BUILD_COLUMNS)}, v.note, b.log_total::float8 AS "logTotal",
      (SELECT start::float8 FROM kept) AS "keptStart",
      (SELECT COALESCE(json_agg(json_build_object('start', l.start_offset::float8, 'chunk', l.chunk) ORDER BY l.start_offset), '[]')
       FROM image_build_log l
       WHERE l.build_id = b.id AND l.start_offset + octet_length(l.chunk) >
         CASE WHEN ${after}::bigint BETWEEN (SELECT start FROM kept) AND b.log_total THEN ${after}::bigint ELSE 0 END
         -- The same bound as an index range: no chunk before the one holding byte after.
         AND l.start_offset >= COALESCE((SELECT max(start_offset) FROM image_build_log
           WHERE build_id = b.id AND start_offset <= ${after}::bigint
             AND ${after}::bigint BETWEEN (SELECT start FROM kept) AND b.log_total), 0)) AS chunks,
      (SELECT json_build_object('versionId', pv.id, 'number', pv.number) FROM image_versions pv WHERE pv.id = i.published_version_id) AS published
    FROM ${scope.sql.unsafe(BUILD_FROM)} WHERE b.id = ${id}`) as BuildRow[];
  return row;
}

/** What a picker lists: every image, archived ones marked (a picker shows one only while it is chosen). */
async function picker(ctx: RequestContext): Promise<Response> {
  const out = await withOrg(ctx.principal.organizationId, async (scope) => {
    const rows = (await scope.sql`
      SELECT i.id, i.name, i.description, pv.number AS version, (i.id IS NOT DISTINCT FROM o.default_image_id) AS "isDefault",
        i.archived_at IS NOT NULL AS archived, COALESCE(pv.can_run_containers, false) AS "canRunContainers",
        (SELECT json_build_object('kind', CASE v.state WHEN 'queued' THEN 'waiting' WHEN 'failed' THEN 'failed' ELSE 'building' END,
           'version', v.number)
         FROM image_versions v WHERE v.image_id = i.id AND v.number > COALESCE(pv.number, 0)
           AND v.state IN ('queued', 'building', 'pushing', 'failed')
         ORDER BY v.number DESC LIMIT 1) AS status
      FROM images i JOIN organizations o ON o.id = i.organization_id
      LEFT JOIN image_versions pv ON pv.id = i.published_version_id
      ORDER BY (i.id IS NOT DISTINCT FROM o.default_image_id) DESC, i.name`) as ImageChoice[];
    const [org] = (await scope.sql`SELECT default_image_id AS id FROM organizations WHERE id = ${scope.organizationId}`) as Array<{ id: string | null }>;
    return { images: rows, defaultImageId: org?.id ?? null };
  });
  return json(out);
}

// ---------------------------------------------------------------------------
// Changing images
// ---------------------------------------------------------------------------

async function record(scope: OrgScope, ctx: RequestContext, type: string, payload: Json) {
  const actor = auditActor(ctx.principal);
  await appendInScope(scope, {
    eventType: type,
    organizationId: scope.organizationId,
    projectId: null,
    actor: { type: actor.kind, id: actor.id },
    source: "control-plane",
    payload,
  });
}

/** The image, locked for the change; 404 for one the organization does not have. */
async function lockImage(scope: OrgScope, id: string): Promise<{ id: string; name: string; publishedVersionId: string | null }> {
  const [row] = (await scope.sql`
    SELECT id, name, published_version_id AS "publishedVersionId" FROM images WHERE id = ${id} FOR UPDATE`) as Array<{
    id: string;
    name: string;
    publishedVersionId: string | null;
  }>;
  if (!row) throw notFound(`no image ${id}`);
  return row;
}

/**
 * A Containerfile checked as the editor checks it, its library parents
 * resolved to ids, and refused if building on them would make an image its
 * own ancestor. The parents' rows are held FOR SHARE until the save commits.
 */
async function checkContainerfile(scope: OrgScope, image: { id: string; name: string }, containerfile: string): Promise<string[]> {
  const all = (await scope.sql`SELECT id, name FROM images ORDER BY name`) as Array<{ id: string; name: string }>;
  const problems = lintContainerfile(containerfile, { images: all.map((i) => i.name), self: image.name }).filter((d) => d.severity === "error");
  if (problems.length > 0) {
    throw new HttpError(422, `the Containerfile won't build: ${problems.map((p) => `line ${p.line}: ${p.message}`).join("; ")}`, "invalid_containerfile", {
      problems,
    });
  }
  const names = imageReferences(containerfile);
  const byName = new Map(all.map((i) => [i.name, i.id]));
  const parents = names.map((n) => byName.get(n)!);
  if (parents.length > 0) await scope.sql`SELECT 1 FROM images WHERE id = ANY(${scope.sql.array(parents, "TEXT")}) FOR SHARE`;
  // What every image is built FROM now, in any version that may still build or run.
  const edges = new Map<string, string[]>();
  const rows = (await scope.sql`
    SELECT DISTINCT c.name AS child, p.name AS parent FROM image_version_parents vp
    JOIN image_versions v ON v.id = vp.version_id JOIN images c ON c.id = v.image_id JOIN images p ON p.id = vp.parent_image_id
    WHERE v.state IN ('draft', 'queued', 'building', 'pushing', 'published') AND c.id <> ${image.id}`) as Array<{ child: string; parent: string }>;
  for (const r of rows) edges.set(r.child, [...(edges.get(r.child) ?? []), r.parent]);
  const loop = imageCycle(image.name, names, edges);
  if (loop) throw new HttpError(422, `that would build ${image.name} on itself: ${loop.join(" → ")}`, "image_cycle", { cycle: loop });
  return parents;
}

/**
 * Save the image's draft (one per image), replacing its Containerfile and
 * parents. "Can run containers" is the input's when it names it; else the
 * draft keeps its own, and a new draft starts from the image's published
 * version, or with none published, from the library image its first FROM
 * names (its published version's).
 */
async function saveDraft(scope: OrgScope, ctx: RequestContext, image: { id: string; name: string }, input: ImageDraftInput): Promise<string> {
  const parents = await checkContainerfile(scope, image, input.containerfile);
  const [existing] = (await scope.sql`
    SELECT id, can_run_containers AS "canRunContainers" FROM image_versions WHERE image_id = ${image.id} AND state = 'draft' FOR UPDATE`) as Array<{
    id: string;
    canRunContainers: boolean;
  }>;
  const id = existing?.id ?? newId("imageVersion");
  const canRunContainers = input.canRunContainers ?? existing?.canRunContainers ?? (await inheritedContainers(scope, image.id, input.containerfile));
  if (existing) {
    await scope.sql`
      UPDATE image_versions SET containerfile = ${input.containerfile}, build_args = ${input.buildArgs}::jsonb, note = ${input.note},
        can_run_containers = ${canRunContainers}, created_by = ${ctx.principal.personId}, updated_at = now()
      WHERE id = ${id}`;
    await scope.sql`DELETE FROM image_version_parents WHERE version_id = ${id}`;
  } else {
    await scope.sql`
      INSERT INTO image_versions (id, organization_id, image_id, containerfile, build_args, note, can_run_containers, created_by)
      VALUES (${id}, ${scope.organizationId}, ${image.id}, ${input.containerfile}, ${input.buildArgs}::jsonb, ${input.note},
        ${canRunContainers}, ${ctx.principal.personId})`;
  }
  for (const parent of parents) {
    await scope.sql`INSERT INTO image_version_parents (organization_id, version_id, parent_image_id) VALUES (${scope.organizationId}, ${id}, ${parent})`;
  }
  return id;
}

/** A new draft's "Can run containers": the image's published version's, else its first FROM image:'s published version's. */
async function inheritedContainers(scope: OrgScope, imageId: string, containerfile: string): Promise<boolean> {
  const parent = fromImage(containerfile);
  const [row] = (await scope.sql`
    SELECT COALESCE(
      (SELECT v.can_run_containers FROM images i JOIN image_versions v ON v.id = i.published_version_id WHERE i.id = ${imageId}),
      (SELECT v.can_run_containers FROM images i JOIN image_versions v ON v.id = i.published_version_id WHERE i.name = ${parent}),
      false) AS can`) as Array<{ can: boolean }>;
  return row?.can ?? false;
}

/** A taken name, as the unique constraint refuses it (SQLSTATE 23505 in Bun's errno). */
function nameTaken(err: unknown, name: string): never {
  if (err instanceof SQL.PostgresError && err.errno === "23505" && err.constraint === "images_organization_id_name_key") {
    throw conflict(`there is already an image named ${name}`);
  }
  throw err;
}

async function createImage(ctx: RequestContext): Promise<Response> {
  await requireOrgAdmin(ctx);
  const input = (await parseBody(ctx.request, newImageSchema)) as NewImageInput;
  const id = newId("image");
  await withOrg(ctx.principal.organizationId, async (scope) => {
    await scope.sql`
      INSERT INTO images (id, organization_id, name, description, created_by)
      VALUES (${id}, ${scope.organizationId}, ${input.name}, ${input.description}, ${ctx.principal.personId})`.catch((err: unknown) => nameTaken(err, input.name));
    if (input.containerfile !== undefined) {
      await saveDraft(scope, ctx, { id, name: input.name }, {
        containerfile: input.containerfile, buildArgs: input.buildArgs, note: input.note, canRunContainers: input.canRunContainers,
      });
    }
    await record(scope, ctx, EventTypes.ImageUpdated, { imageId: id, name: input.name, changed: { added: true } });
  });
  return json(await detail(ctx, id), 201);
}

async function patchImage(ctx: RequestContext): Promise<Response> {
  await requireOrgAdmin(ctx);
  const id = ctx.params.id!;
  const input = await parseBody(ctx.request, imagePatchSchema);
  await withOrg(ctx.principal.organizationId, async (scope) => {
    const image = await lockImage(scope, id);
    await scope.sql`
      UPDATE images SET description = COALESCE(${input.description ?? null}, description),
        archived_at = CASE WHEN ${input.archived ?? null}::boolean IS NULL THEN archived_at
                           WHEN ${input.archived ?? false} THEN COALESCE(archived_at, now()) END
      WHERE id = ${id}`;
    await record(scope, ctx, EventTypes.ImageUpdated, { imageId: id, name: image.name, changed: input });
  });
  return json(await detail(ctx, id));
}

async function putDraft(ctx: RequestContext): Promise<Response> {
  await requireOrgAdmin(ctx);
  const id = ctx.params.id!;
  const input = (await parseBody(ctx.request, imageDraftSchema)) as ImageDraftInput;
  await withOrg(ctx.principal.organizationId, async (scope) => {
    const image = await lockImage(scope, id);
    await saveDraft(scope, ctx, image, input);
  });
  return json(await detail(ctx, id));
}

async function discardDraft(ctx: RequestContext): Promise<Response> {
  await requireOrgAdmin(ctx);
  const id = ctx.params.id!;
  await withOrg(ctx.principal.organizationId, async (scope) => {
    await lockImage(scope, id);
    await scope.sql`DELETE FROM image_versions WHERE image_id = ${id} AND state = 'draft'`;
  });
  return json(await detail(ctx, id));
}

/**
 * Build & publish: the draft (saved first, when the body carries one) gets
 * the image's next number and joins the queue. A version of the same image
 * still waiting is cancelled: the newer one replaces it in the line.
 */
async function buildImage(ctx: RequestContext): Promise<Response> {
  await requireOrgAdmin(ctx);
  const id = ctx.params.id!;
  // No body, or an empty object: build the draft as saved.
  const raw = (await ctx.request.clone().text()).trim();
  const input = raw && raw !== "{}" ? ((await parseBody(ctx.request, imageDraftSchema)) as ImageDraftInput) : null;
  if (!buildsConfigured()) {
    throw new HttpError(503, "image builds are not configured on this dude: DUDE_LAYER_IMAGE is unset", "builder_unavailable");
  }
  const queued = await withOrg(ctx.principal.organizationId, async (scope) => {
    const image = await lockImage(scope, id);
    if (input) await saveDraft(scope, ctx, image, input);
    const [draft] = (await scope.sql`
      SELECT id, containerfile FROM image_versions WHERE image_id = ${id} AND state = 'draft' FOR UPDATE`) as Array<{ id: string; containerfile: string }>;
    if (!draft) throw conflict(`${image.name} has no draft to build: save one first`);
    // Checked again: an image named in it may have changed since the save.
    await checkContainerfile(scope, image, draft.containerfile);
    const superseded = (await scope.sql`
      UPDATE image_versions SET state = 'cancelled', updated_at = now()
      WHERE image_id = ${id} AND state = 'queued' RETURNING id`) as Array<{ id: string }>;
    if (superseded.length > 0) {
      await scope.sql`
        UPDATE image_builds SET state = 'cancelled', finished_at = now(), error = 'a newer version was queued'
        WHERE image_version_id = ANY(${scope.sql.array(superseded.map((s) => s.id), "TEXT")}) AND state = 'queued'`;
    }
    const [next] = (await scope.sql`SELECT COALESCE(max(number), 0) + 1 AS n FROM image_versions WHERE image_id = ${id}`) as Array<{ n: number }>;
    const n = next!.n;
    await scope.sql`UPDATE image_versions SET state = 'queued', number = ${n}, updated_at = now() WHERE id = ${draft.id}`;
    const buildId = newId("imageBuild");
    await scope.sql`
      INSERT INTO image_builds (id, organization_id, image_version_id, kind, requested_by)
      VALUES (${buildId}, ${scope.organizationId}, ${draft.id}, 'build', ${ctx.principal.personId})`;
    await record(scope, ctx, EventTypes.ImageBuildQueued, {
      imageId: id, name: image.name, versionId: draft.id, version: n, buildId, source: "person",
    });
    return { buildId, versionId: draft.id, version: n };
  });
  return json({ ...queued, image: await detail(ctx, id) }, 201);
}

/**
 * Publish a built version again: at once, no build — its image is still in
 * the registry, and a Run that needs it with a dude layer it has not been
 * finished with gets a finish job. Images built FROM this one are queued to
 * rebuild on it, as after a build.
 */
async function republish(ctx: RequestContext): Promise<Response> {
  await requireOrgAdmin(ctx);
  const id = ctx.params.id!;
  const versionId = ctx.params.versionId!;
  await withOrg(ctx.principal.organizationId, async (scope) => {
    const image = await lockImage(scope, id);
    const [v] = (await scope.sql`
      SELECT number, state, user_ref AS "userRef" FROM image_versions WHERE id = ${versionId} AND image_id = ${id} FOR UPDATE`) as Array<{
      number: number | null;
      state: string;
      userRef: string | null;
    }>;
    if (!v) throw notFound(`${image.name} has no version ${versionId}`);
    if (image.publishedVersionId === versionId) throw conflict(`v${v.number} is already published`);
    if (!v.userRef || !["published", "superseded"].includes(v.state)) throw conflict(`v${v.number ?? "draft"} never built, so it cannot be published`);
    const rebuilds = (await scope.sql`SELECT * FROM image_publish(${versionId})`) as Array<{
      image_id: string;
      image_name: string;
      version_id: string;
      version: number;
      build_id: string;
    }>;
    await record(scope, ctx, EventTypes.ImagePublished, { imageId: id, name: image.name, versionId, version: v.number, republished: true });
    for (const r of rebuilds) {
      await record(scope, ctx, EventTypes.ImageBuildQueued, {
        imageId: r.image_id, name: r.image_name, versionId: r.version_id, version: r.version, buildId: r.build_id, source: "base_rebuild",
      });
    }
  });
  return json(await detail(ctx, id));
}

/** Make an image the organization's default base, or (DELETE) none. */
async function setDefault(ctx: RequestContext, imageId: string | null): Promise<Response> {
  await requireOrgAdmin(ctx);
  await withOrg(ctx.principal.organizationId, async (scope) => {
    if (imageId) await requireImage(scope, imageId);
    await scope.sql`UPDATE organizations SET default_image_id = ${imageId}, updated_at = now() WHERE id = ${scope.organizationId}`;
    await record(scope, ctx, EventTypes.ImageUpdated, { imageId, changed: { default: imageId !== null } });
  });
  return json(await imagesResponse(ctx));
}

async function cancelBuild(ctx: RequestContext): Promise<Response> {
  await requireOrgAdmin(ctx);
  const id = ctx.params.buildId!;
  await withOrg(ctx.principal.organizationId, async (scope) => {
    const [b] = (await scope.sql`
      SELECT b.kind, b.state, b.image_version_id AS "versionId" FROM image_builds b WHERE b.id = ${id} FOR UPDATE`) as Array<{
      kind: string;
      state: string;
      versionId: string;
    }>;
    if (!b) throw notFound(`no image build ${id}`);
    if (b.state !== "queued") throw conflict("only a build still waiting can be cancelled");
    if (b.kind === "finish") throw conflict("a Run is waiting on this: it cannot be cancelled");
    await scope.sql`UPDATE image_builds SET state = 'cancelled', finished_at = now(), error = 'cancelled' WHERE id = ${id}`;
    await scope.sql`UPDATE image_versions SET state = 'cancelled', updated_at = now() WHERE id = ${b.versionId} AND state = 'queued'`;
  });
  return json({ cancelled: id });
}

/**
 * Refuse an image id the organization does not have, or has archived (a
 * new reference to an archived image; what names one already keeps it).
 * Held FOR SHARE to the end of the caller's transaction.
 */
export async function requireImage(scope: OrgScope, id: string, keep?: string | null): Promise<void> {
  const [row] = (await scope.sql`SELECT archived_at IS NOT NULL AS archived, name FROM images WHERE id = ${id} FOR SHARE`) as Array<{
    archived: boolean;
    name: string;
  }>;
  if (!row) throw badRequest(`there is no image ${id}`);
  if (row.archived && keep !== id) throw badRequest(`${row.name} is archived: pick another image`);
}

/**
 * requireImage for several images at once, with no keep: locked in id order,
 * as every multi-row lock is. Refusals name the first id in the caller's order.
 */
export async function checkImages(scope: OrgScope, ids: readonly string[]): Promise<void> {
  const distinct = [...new Set(ids)];
  if (distinct.length === 0) return;
  const rows = (await scope.sql`
    SELECT id, name, archived_at IS NOT NULL AS archived FROM images
    WHERE id = ANY(${scope.sql.array(distinct, "text")}::text[]) ORDER BY id FOR SHARE`) as Array<{ id: string; name: string; archived: boolean }>;
  const byId = new Map(rows.map((r) => [r.id, r]));
  for (const id of distinct) {
    const row = byId.get(id);
    if (!row) throw badRequest(`there is no image ${id}`);
    if (row.archived) throw badRequest(`${row.name} is archived: pick another image`);
  }
}

/** The image ids the organization has, for resolving a role's layers. */
export async function imageIds(scope: OrgScope): Promise<Array<{ id: string }>> {
  return (await scope.sql`SELECT id FROM images`) as Array<{ id: string }>;
}

export function registerImageRoutes(router: Router): void {
  router.get("/v1/images", async (ctx) => json(await imagesResponse(ctx)));
  router.post("/v1/images", createImage);
  router.get("/v1/images/picker", picker);
  router.post("/v1/images/default/:id", (ctx) => setDefault(ctx, ctx.params.id!));
  router.delete("/v1/images/default", (ctx) => setDefault(ctx, null));
  router.get("/v1/images/builds/:buildId", buildDetail);
  router.post("/v1/images/builds/:buildId/cancel", cancelBuild);
  router.get("/v1/images/:id", async (ctx) => json(await detail(ctx, ctx.params.id!)));
  router.patch("/v1/images/:id", patchImage);
  router.put("/v1/images/:id/draft", putDraft);
  router.delete("/v1/images/:id/draft", discardDraft);
  router.post("/v1/images/:id/build", buildImage);
  router.post("/v1/images/:id/versions/:versionId/publish", republish);
}
