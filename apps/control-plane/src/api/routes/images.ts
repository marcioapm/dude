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
  EventTypes,
  firstFrom,
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

/** The builder as configured: builds run only with a dude layer to finish them with. */
export function builderInfo(): ImageBuilderInfo {
  const c = config();
  const layer = c.string("DUDE_LAYER_IMAGE") ?? null;
  const memory = c.string("DUDE_BUILDER_MEMORY") ?? "1536m";
  return { available: layer !== null, layer, cpus: c.float("DUDE_BUILDER_CPUS"), memoryMiB: mebibytes(memory) };
}

/** podman's memory notation (1536m, 2g, 1610612736) in MiB. */
function mebibytes(s: string): number {
  const m = /^(\d+(?:\.\d+)?)([bkmg]?)$/i.exec(s.trim());
  if (!m) return 0;
  const n = Number(m[1]);
  const unit = (m[2] ?? "").toLowerCase();
  return Math.round(unit === "g" ? n * 1024 : unit === "m" ? n : unit === "k" ? n / 1024 : n / 1024 / 1024);
}

const person = (alias: string) =>
  `(SELECT json_build_object('id', ${alias}.id, 'name', ${alias}.name) FROM people ${alias} WHERE ${alias}.id = `;

// An image's summary, over `images i` in scope and `organizations o`.
const SUMMARY_COLUMNS = `
  i.id, i.name, i.description, i.archived_at AS "archivedAt", i.created_at AS "createdAt",
  ${person("cp")} i.created_by) AS "createdBy",
  (i.id IS NOT DISTINCT FROM o.default_image_id) AS "isDefault",
  (SELECT json_build_object('versionId', v.id, 'number', v.number, 'builtAt', v.built_at, 'userRef', v.user_ref)
   FROM image_versions v WHERE v.id = i.published_version_id) AS published,
  (SELECT json_build_object('versionId', v.id, 'number', v.number, 'state', v.state, 'error', v.error)
   FROM image_versions v WHERE v.image_id = i.id AND v.number IS NOT NULL
     AND v.number > COALESCE((SELECT p.number FROM image_versions p WHERE p.id = i.published_version_id), 0)
     AND v.state IN ('queued', 'building', 'pushing', 'failed')
   ORDER BY v.number DESC LIMIT 1) AS pending,
  (SELECT json_build_object('versionId', v.id, 'updatedAt', v.updated_at, 'updatedBy', ${person("dp")} v.created_by))
   FROM image_versions v WHERE v.image_id = i.id AND v.state = 'draft') AS draft,
  (SELECT COALESCE(json_agg(json_build_object('id', pi.id, 'name', pi.name) ORDER BY pi.name), '[]')
   FROM image_version_parents vp JOIN images pi ON pi.id = vp.parent_image_id
   WHERE vp.version_id = latest.id) AS parents,
  latest.containerfile AS "latestContainerfile",
  json_build_object('at', latest.updated_at, 'by', ${person("lp")} latest.created_by), 'source', latest.source) AS "lastChange"`;

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
  b.layer_ref AS "layerRef", ${person("rp")} b.requested_by) AS "requestedBy", b.requested_at AS "requestedAt",
  b.started_at AS "startedAt", b.finished_at AS "finishedAt", b.error, b.build_seconds AS "buildSeconds",
  b.push_seconds AS "pushSeconds", CASE WHEN b.state = 'queued' THEN image_queue_ahead(b.id) END AS ahead`;
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
      builder: builderInfo(),
      canEdit: await isOrgAdmin(ctx),
    };
  });
}

const VERSION_COLUMNS = `
  v.id, v.image_id AS "imageId", v.number, v.state, v.containerfile, v.build_args AS "buildArgs", v.note, v.source,
  v.created_at AS "createdAt", v.updated_at AS "updatedAt", ${person("vp_")} v.created_by) AS "createdBy",
  v.user_ref AS "userRef", v.built_at AS "builtAt", v.error,
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
    return { image, versions, builds, builder: builderInfo(), canEdit: await isOrgAdmin(ctx) };
  });
}

async function buildDetail(ctx: RequestContext): Promise<Response> {
  const id = ctx.params.buildId!;
  const build = await withOrg(ctx.principal.organizationId, async (scope) => {
    const [row] = (await scope.sql`
      SELECT ${scope.sql.unsafe(BUILD_COLUMNS)}, b.log, v.note,
        (SELECT json_build_object('versionId', pv.id, 'number', pv.number) FROM image_versions pv WHERE pv.id = i.published_version_id) AS published
      FROM ${scope.sql.unsafe(BUILD_FROM)} WHERE b.id = ${id}`) as ImageBuildWithLog[];
    return row;
  });
  if (!build) throw notFound(`no image build ${id}`);
  return json(build);
}

/** What a picker lists: every image, archived ones marked (a picker shows one only while it is chosen). */
async function picker(ctx: RequestContext): Promise<Response> {
  const out = await withOrg(ctx.principal.organizationId, async (scope) => {
    const rows = (await scope.sql`
      SELECT i.id, i.name, i.description, pv.number AS version, (i.id IS NOT DISTINCT FROM o.default_image_id) AS "isDefault",
        i.archived_at IS NOT NULL AS archived,
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

/** Save the image's draft (one per image), replacing its Containerfile and parents. */
async function saveDraft(scope: OrgScope, ctx: RequestContext, image: { id: string; name: string }, input: ImageDraftInput): Promise<string> {
  const parents = await checkContainerfile(scope, image, input.containerfile);
  const [existing] = (await scope.sql`SELECT id FROM image_versions WHERE image_id = ${image.id} AND state = 'draft' FOR UPDATE`) as Array<{ id: string }>;
  const id = existing?.id ?? newId("imageVersion");
  if (existing) {
    await scope.sql`
      UPDATE image_versions SET containerfile = ${input.containerfile}, build_args = ${input.buildArgs}::jsonb, note = ${input.note},
        created_by = ${ctx.principal.personId}, updated_at = now()
      WHERE id = ${id}`;
    await scope.sql`DELETE FROM image_version_parents WHERE version_id = ${id}`;
  } else {
    await scope.sql`
      INSERT INTO image_versions (id, organization_id, image_id, containerfile, build_args, note, created_by)
      VALUES (${id}, ${scope.organizationId}, ${image.id}, ${input.containerfile}, ${input.buildArgs}::jsonb, ${input.note}, ${ctx.principal.personId})`;
  }
  for (const parent of parents) {
    await scope.sql`INSERT INTO image_version_parents (organization_id, version_id, parent_image_id) VALUES (${scope.organizationId}, ${id}, ${parent})`;
  }
  return id;
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
      await saveDraft(scope, ctx, { id, name: input.name }, { containerfile: input.containerfile, buildArgs: input.buildArgs, note: input.note });
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
  const raw = (await ctx.request.clone().text()).trim();
  const input = raw ? ((await parseBody(ctx.request, imageDraftSchema)) as ImageDraftInput) : null;
  if (!builderInfo().available) {
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
