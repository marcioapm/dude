/**
 * The organization's people: who you are, who else is here, their keys,
 * and — for its admins — inviting, changing and removing members.
 *
 * A person is a `people` row (migration 035) that any number of API keys
 * act for. Everywhere the API names one it is a `PersonRef`
 * (`person_ref()` in SQL): id, name, photo, and whether they were seen in
 * the last five minutes. A task's owner is its first active person;
 * `tasks.owner_key_id` is only an optional legacy mirror.
 */

import { z } from "zod";
import { EventTypes, personRoleSchema, type PersonRef } from "@dude/domain";
import type { OrgScope } from "../../db/client.ts";
import { withOrg, withoutTenant } from "../../db/client.ts";
import { appendInScope } from "../../events/ledger.ts";
import { auditActor, insertApiKey, insertPerson } from "../auth.ts";
import { ACCESS_LOGOUT_PATH } from "../cloudflareAccess.ts";
import { HttpError, badRequest, conflict, json, noContent, notFound, parseBody } from "../http.ts";
import { replaceImage, serveImage } from "../faces.ts";
import { deleteObject } from "../../storage.ts";
import type { PublicContext, RequestContext, Router } from "../router.ts";

/** The SELECT expression for a task's owner (a `PersonRef`) or null, for the `tasks` rows under `alias`. */
export function ownerJson(alias = "tasks"): string {
  return `(SELECT person_ref(p) FROM task_people tp JOIN people p ON p.id = tp.person_id
   WHERE tp.task_id = ${alias}.id AND p.removed_at IS NULL
   ORDER BY tp.position, tp.person_id LIMIT 1) AS owner`;
}

/** The SELECT expression for everyone on a task, the owner first, for the `tasks` rows under `alias`. */
export function peopleJson(alias = "tasks"): string {
  return `(SELECT COALESCE(json_agg(person_ref(p) ORDER BY tp.position), '[]'::json)
  FROM task_people tp JOIN people p ON p.id = tp.person_id
  WHERE tp.task_id = ${alias}.id AND p.removed_at IS NULL) AS people`;
}

/**
 * The key a task's owner is mirrored as: the person's most recently used
 * one. Null when they have none left that signs in.
 */
export async function keyOf(scope: OrgScope, personId: string): Promise<string | null> {
  const rows = (await scope.sql`
    SELECT k.id FROM api_keys k JOIN people p ON p.id = k.person_id
    WHERE k.person_id = ${personId} AND k.revoked_at IS NULL AND p.removed_at IS NULL
    ORDER BY k.last_used_at DESC NULLS LAST, k.created_at DESC LIMIT 1`) as Array<{ id: string }>;
  return rows[0]?.id ?? null;
}

/**
 * The person `given` names — a person's id, or one of their keys' (what
 * the API took before people) — if they are still a member; else null.
 */
export async function personOf(scope: OrgScope, given: string): Promise<string | null> {
  const rows = (await scope.sql`
    SELECT p.id FROM people p
    WHERE p.removed_at IS NULL AND (p.id = ${given}
      OR p.id = (SELECT k.person_id FROM api_keys k WHERE k.id = ${given} AND k.revoked_at IS NULL))`) as Array<{ id: string }>;
  return rows[0]?.id ?? null;
}

/**
 * Put `people` on a task in that order, the first its owner. With `keep`,
 * whoever else was on it stays, after them; otherwise the list replaces
 * theirs. A usable key is mirrored in `tasks.owner_key_id` only for legacy
 * readers. Records what changed.
 */
export async function setTaskPeople(scope: OrgScope, ctx: RequestContext, taskId: string, projectId: string,
  people: readonly string[], keep: boolean): Promise<void> {
  const before = (await scope.sql`
    SELECT tp.person_id AS id FROM task_people tp WHERE tp.task_id = ${taskId}
    ORDER BY tp.position`) as Array<{ id: string }>;
  const was = before.map((r) => r.id);
  const next = keep ? [...people, ...was.filter((id) => !people.includes(id))] : [...people];
  if (next.length === was.length && next.every((id, i) => id === was[i])) return;

  await scope.sql`DELETE FROM task_people WHERE task_id = ${taskId}`;
  await scope.sql`
    INSERT INTO task_people (task_id, person_id, organization_id, position)
    SELECT ${taskId}, id, ${scope.organizationId}, n - 1
    FROM jsonb_array_elements_text(${next}::jsonb) WITH ORDINALITY AS t(id, n)`;
  const owner = next[0] ?? null;
  if (owner !== (was[0] ?? null)) {
    await scope.sql`UPDATE tasks SET owner_key_id = ${owner ? await keyOf(scope, owner) : null} WHERE id = ${taskId}`;
    await recordAs(scope, ctx, EventTypes.TaskOwnerChanged, { from: was[0] ?? null, to: owner }, { projectId, taskId });
  }
  await recordAs(scope, ctx, EventTypes.TaskPeopleChanged, { people: next }, { projectId, taskId });
}

const PERSON_DETAIL = `person_ref(p)::jsonb || jsonb_build_object('email', p.email, 'role', p.role,
  'lastSeenAt', p.last_seen_at, 'lastSeenWhere', p.last_seen_where) AS person`;

async function personDetail(scope: OrgScope, id: string) {
  const rows = (await scope.sql`
    SELECT ${scope.sql.unsafe(PERSON_DETAIL)} FROM people p WHERE p.id = ${id}`) as Array<{ person: PersonRef }>;
  return rows[0]?.person ?? null;
}

/**
 * Refuse anyone but an organization admin. Changes to an organization's
 * members take turns (a transaction-scoped lock), so two admins demoting
 * each other at once cannot both pass the last-admin check.
 */
async function requireAdmin(scope: OrgScope, ctx: RequestContext): Promise<void> {
  await scope.sql`SELECT pg_advisory_xact_lock(hashtext('people:' || ${scope.organizationId}))`;
  const rows = (await scope.sql`
    SELECT role FROM people WHERE id = ${ctx.principal.personId} AND removed_at IS NULL`) as Array<{ role: string }>;
  if (rows[0]?.role !== "admin") {
    throw new HttpError(403, "only an organization admin can manage its members", "not_admin");
  }
}

/** How many admins the organization would have without `except`. */
async function otherAdmins(scope: OrgScope, except: string): Promise<number> {
  const rows = (await scope.sql`
    SELECT count(*)::int AS n FROM people
    WHERE role = 'admin' AND removed_at IS NULL AND id <> ${except}`) as Array<{ n: number }>;
  return rows[0]!.n;
}

/**
 * Record a change as the person making the request, in the part of the
 * hierarchy it belongs to (none: the organization's). A task's changes
 * correlate by the task.
 */
export function recordAs(scope: OrgScope, ctx: RequestContext, eventType: string, payload: Record<string, unknown>,
  where: { projectId?: string; taskId?: string | null } = {}) {
  return appendInScope(scope, {
    eventType,
    organizationId: ctx.principal.organizationId,
    projectId: where.projectId ?? null,
    taskId: where.taskId ?? null,
    correlationId: where.taskId ?? null,
    actor: { type: auditActor(ctx.principal).kind, id: auditActor(ctx.principal).id },
    source: "control-plane",
    payload,
  });
}

// ---------------------------------------------------------------------------
// You
// ---------------------------------------------------------------------------

async function getMe(ctx: RequestContext): Promise<Response> {
  const out = await withOrg(ctx.principal.organizationId, async (scope) => {
    const person = await personDetail(scope, ctx.principal.personId);
    // organizations is not a tenant table; the row is the caller's own.
    const org = (await scope.sql`
      SELECT id, name FROM organizations WHERE id = ${ctx.principal.organizationId}`) as Array<{ id: string; name: string }>;
    return { person, organization: org[0]! };
  });
  // How the browser signed in decides how it signs out: a key is forgotten
  // locally; an Access session ends at Access's own fixed logout path.
  const auth = ctx.principal.credentialKind === "api_key"
    ? { authMethod: "api_key" }
    : { authMethod: "cloudflare_access", logoutUrl: ACCESS_LOGOUT_PATH };
  return json({ ...out, ...auth });
}

/** A photo someone keeps elsewhere: an https URL. Uploads go to PUT /v1/me/photo. */
const photoInput = z
  .string()
  .max(2000)
  .regex(/^https:\/\/[^\s"'<>]+$/, "a photo is an https URL; upload an image to /v1/me/photo")
  .nullable();

const updateMeInput = z
  .object({
    name: z.string().trim().min(1).max(100),
    photoUrl: photoInput,
  })
  .partial()
  .strict();

async function updateMe(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, updateMeInput);
  if (input.name === undefined && input.photoUrl === undefined) throw badRequest("nothing to change");
  const setPhoto = input.photoUrl !== undefined;
  const { person, old } = await withOrg(ctx.principal.organizationId, async (scope) => {
    const [before] = (await scope.sql`
      SELECT photo_key AS key FROM people WHERE id = ${ctx.principal.personId} FOR UPDATE`) as Array<{ key: string | null }>;
    // A photo by URL, or none, replaces an uploaded one.
    await scope.sql`
      UPDATE people SET
        name = COALESCE(${input.name ?? null}, name),
        photo_url = CASE WHEN ${setPhoto} THEN ${input.photoUrl ?? null} ELSE photo_url END,
        photo_key = CASE WHEN ${setPhoto} THEN NULL ELSE photo_key END,
        photo_token = CASE WHEN ${setPhoto} THEN NULL ELSE photo_token END
      WHERE id = ${ctx.principal.personId}`;
    return { person: await personDetail(scope, ctx.principal.personId), old: setPhoto ? before?.key : null };
  });
  if (old) await deleteObject(old);
  return json({ person });
}

/** Your photo, uploaded: the image is the body (see api/faces.ts). */
async function uploadMyPhoto(ctx: RequestContext): Promise<Response> {
  // The person is the key's own, found when it authenticated.
  const { organizationId, personId } = ctx.principal;
  const person = await replaceImage(ctx.request, `${organizationId}/people/${personId}`, ({ key, token }) =>
    withOrg(organizationId, async (scope) => {
      const [before] = (await scope.sql`
        SELECT photo_key AS key FROM people WHERE id = ${personId} FOR UPDATE`) as Array<{ key: string | null }>;
      await scope.sql`
        UPDATE people SET photo_key = ${key}, photo_token = ${token}, photo_url = NULL WHERE id = ${personId}`;
      return { result: await personDetail(scope, personId), old: before?.key };
    }));
  return json({ person });
}

/** A photo for an `<img>`: it sends no key, so the URL carries a token. */
async function getPhoto(ctx: PublicContext): Promise<Response> {
  const token = ctx.url.searchParams.get("t") ?? "";
  const rows = await withoutTenant(async ({ sql }) =>
    (await sql`SELECT person_photo(${ctx.params.id!}, ${token}) AS key`) as Array<{ key: string | null }>);
  return serveImage(rows[0]?.key);
}

// ---------------------------------------------------------------------------
// Your keys
// ---------------------------------------------------------------------------

const KEY_SELECT = `id, name, key_prefix AS prefix, created_at AS "createdAt", last_used_at AS "lastUsedAt"`;

async function listMyKeys(ctx: RequestContext): Promise<Response> {
  const keys = await withOrg(ctx.principal.organizationId, async (scope) => {
    const rows = (await scope.sql`
      SELECT ${scope.sql.unsafe(KEY_SELECT)} FROM api_keys
      WHERE person_id = ${ctx.principal.personId} AND revoked_at IS NULL
      ORDER BY created_at`) as Array<{ id: string }>;
    return rows.map((k) => ({ ...k, current: ctx.principal.credentialKind === "api_key" && k.id === ctx.principal.apiKeyId }));
  });
  return json({ keys });
}

const createKeyInput = z.object({ name: z.string().trim().min(1).max(100) }).strict();

async function createMyKey(ctx: RequestContext): Promise<Response> {
  const { name } = await parseBody(ctx.request, createKeyInput);
  const made = await withOrg(ctx.principal.organizationId, (scope) =>
    insertApiKey(scope, { name, personId: ctx.principal.personId }));
  // The plaintext, this once; only its hash is kept.
  return json({ id: made.id, name, prefix: made.keyPrefix, key: made.key }, 201);
}

async function revokeMyKey(ctx: RequestContext): Promise<Response> {
  const id = ctx.params.id!;
  const result = await withOrg(ctx.principal.organizationId, async (scope) => {
    const rows = (await scope.sql`
      SELECT id FROM api_keys WHERE person_id = ${ctx.principal.personId} AND revoked_at IS NULL
      FOR UPDATE`) as Array<{ id: string }>;
    if (!rows.some((k) => k.id === id)) return "missing";
    // A verified person credential remains usable without an API key.
    if (rows.length === 1 && ctx.principal.credentialKind !== "person") return "last";
    await scope.sql`UPDATE api_keys SET revoked_at = now() WHERE id = ${id}`;
    return "revoked";
  });
  if (result === "missing") throw notFound(`key ${id} is not one of yours`);
  if (result === "last") throw conflict("that is your only key; make another before revoking it");
  return noContent();
}

// ---------------------------------------------------------------------------
// The organization's people
// ---------------------------------------------------------------------------

/** Everyone in the organization, by name; `you` is the caller. */
async function listPeople(ctx: RequestContext): Promise<Response> {
  const people = await withOrg(ctx.principal.organizationId, async (scope) => {
    const rows = (await scope.sql`
      SELECT ${scope.sql.unsafe(PERSON_DETAIL)} FROM people p
      WHERE p.removed_at IS NULL ORDER BY p.name, p.created_at`) as Array<{ person: PersonRef }>;
    return rows.map((r) => r.person);
  });
  return json({ people, you: ctx.principal.personId });
}

const inviteInput = z
  .object({
    name: z.string().trim().min(1).max(100),
    email: z.string().trim().email().max(320),
    role: personRoleSchema.default("member"),
  })
  .strict();

/**
 * Invite someone: they become one of the organization's people, with a
 * key shown this once. There is no sign-in by email yet, so the key is
 * the invitation — the admin hands it over.
 */
async function invite(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, inviteInput);
  const out = await withOrg(ctx.principal.organizationId, async (scope) => {
    await requireAdmin(scope, ctx);
    const made = await insertPerson(scope, input);
    if (!made) return null;
    await recordAs(scope, ctx, EventTypes.PersonInvited, { personId: made.personId, role: input.role });
    return { person: await personDetail(scope, made.personId), key: made.key };
  });
  if (!out) throw conflict(`${input.email} is already a member`);
  return json(out, 201);
}

const updatePersonInput = z
  .object({ name: z.string().trim().min(1).max(100), role: personRoleSchema })
  .partial()
  .strict();

async function updatePerson(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, updatePersonInput);
  if (input.name === undefined && input.role === undefined) throw badRequest("nothing to change");
  const id = ctx.params.id!;
  const out = await withOrg(ctx.principal.organizationId, async (scope) => {
    await requireAdmin(scope, ctx);
    const rows = (await scope.sql`
      SELECT role FROM people WHERE id = ${id} AND removed_at IS NULL FOR UPDATE`) as Array<{ role: string }>;
    if (!rows[0]) return "missing" as const;
    if (input.role === "member" && rows[0].role === "admin" && (await otherAdmins(scope, id)) === 0) {
      return "lastAdmin" as const;
    }
    await scope.sql`
      UPDATE people SET name = COALESCE(${input.name ?? null}, name), role = COALESCE(${input.role ?? null}, role)
      WHERE id = ${id}`;
    if (input.role && input.role !== rows[0].role) {
      await recordAs(scope, ctx, EventTypes.PersonRoleChanged, { personId: id, from: rows[0].role, to: input.role });
    }
    return { person: await personDetail(scope, id) };
  });
  if (out === "missing") throw notFound(`person ${id} not found`);
  if (out === "lastAdmin") throw conflict("the organization needs an admin; make someone else one first");
  return json(out);
}

/**
 * Take someone who is leaving off their tasks. One they owned passes to
 * the next person on it, so its first person and its owner stay the same
 * one; with nobody else on it, it is nobody's, which anyone may answer.
 */
async function passOnTasks(scope: OrgScope, ctx: RequestContext, personId: string): Promise<void> {
  const owned = (await scope.sql`
    SELECT tp.task_id AS "taskId", t.project_id AS "projectId"
    FROM task_people tp JOIN tasks t ON t.id = tp.task_id
    WHERE tp.person_id = ${personId} AND NOT EXISTS (
      SELECT 1 FROM task_people earlier JOIN people p ON p.id = earlier.person_id
      WHERE earlier.task_id = tp.task_id AND p.removed_at IS NULL
        AND (earlier.position, earlier.person_id) < (tp.position, tp.person_id))`) as Array<
      { taskId: string; projectId: string }
    >;
  await scope.sql`DELETE FROM task_people WHERE person_id = ${personId}`;
  for (const { taskId, projectId } of owned) {
    const next = (await scope.sql`
      SELECT tp.person_id AS id FROM task_people tp JOIN people p ON p.id = tp.person_id
      WHERE tp.task_id = ${taskId} AND p.removed_at IS NULL
      ORDER BY tp.position, tp.person_id LIMIT 1`) as Array<{ id: string }>;
    const to = next[0]?.id ?? null;
    await scope.sql`UPDATE tasks SET owner_key_id = ${to ? await keyOf(scope, to) : null} WHERE id = ${taskId}`;
    await recordAs(scope, ctx, EventTypes.TaskOwnerChanged, { from: personId, to }, { projectId, taskId });
  }
}

/**
 * Remove someone: every key of theirs stops working at once and they
 * leave the organization's lists. Their row stays, so what they did keeps
 * their name; their tasks pass on (see `passOnTasks`).
 */
async function removePerson(ctx: RequestContext): Promise<Response> {
  const id = ctx.params.id!;
  const out = await withOrg(ctx.principal.organizationId, async (scope) => {
    await requireAdmin(scope, ctx);
    if (id === ctx.principal.personId) return "yourself";
    const rows = (await scope.sql`
      SELECT role FROM people WHERE id = ${id} AND removed_at IS NULL FOR UPDATE`) as Array<{ role: string }>;
    if (!rows[0]) return "missing";
    if (rows[0].role === "admin" && (await otherAdmins(scope, id)) === 0) return "lastAdmin";
    // Their tasks are not locked ahead: holding this person's row while
    // waiting on a task deadlocks with putTaskPeople, which holds the task
    // and takes this row's KEY SHARE re-inserting them. The conductor's
    // decide_escalation (delivery.LockEscalationTx holds the task's row) is
    // serial with this: if it read the owner first, passOnTasks' UPDATE of
    // that task waits for it; if that UPDATE came first, the decision waits
    // and reads the new owner. Removal vs. task-people replacement and vs.
    // an answer (answered_by_person's KEY SHARE on this row) can still
    // deadlock: pre-existing, see dude issue #TBD.
    await scope.sql`UPDATE people SET removed_at = now() WHERE id = ${id}`;
    await scope.sql`UPDATE api_keys SET revoked_at = now() WHERE person_id = ${id} AND revoked_at IS NULL`;
    await scope.sql`DELETE FROM push_subscriptions WHERE person_id = ${id}`;
    await passOnTasks(scope, ctx, id);
    await recordAs(scope, ctx, EventTypes.PersonRemoved, { personId: id });
    return "removed";
  });
  if (out === "yourself") throw conflict("you cannot remove yourself; ask another admin");
  if (out === "missing") throw notFound(`person ${id} not found`);
  if (out === "lastAdmin") throw conflict("the organization needs an admin; make someone else one first");
  return noContent();
}

export function registerPeopleRoutes(router: Router): void {
  router.get("/v1/me", getMe);
  router.patch("/v1/me", updateMe);
  router.put("/v1/me/photo", uploadMyPhoto);
  router.get("/v1/me/keys", listMyKeys);
  router.post("/v1/me/keys", createMyKey);
  router.delete("/v1/me/keys/:id", revokeMyKey);

  router.get("/v1/people", listPeople);
  router.post("/v1/people", invite);
  router.patch("/v1/people/:id", updatePerson);
  router.delete("/v1/people/:id", removePerson);
  router.publicRoute("GET", "/v1/people/:id/photo", getPhoto);
}
