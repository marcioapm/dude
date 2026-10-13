import { z } from "zod";
import { newId } from "@dude/domain";
import { withOrg, withoutTenant } from "../../db/client.ts";
import { HttpError, json, noContent, parseBody, conflict } from "../http.ts";
import type { RequestContext, Router } from "../router.ts";
import { changeInvitation, invitationAdmin, inviteBatch, listInvitations, lockPeople } from "./invitations.ts";
import { removePerson, updatePerson } from "./people.ts";
function requireSuper(ctx: RequestContext) {
  if (ctx.principal.credentialKind !== "person" || !ctx.principal.access?.superAdmin) {
    throw new HttpError(403, "super_admin_required", "super_admin_required");
  }
}
const invitationInput = z.object({ invitations: z.array(z.object({ email: z.string(), name: z.string().optional() }).strict()).min(1).max(100),
  role: z.enum(["admin", "member"]).default("member") }).strict();
function inOrg(ctx: RequestContext, organizationId: string, personId = ""): RequestContext {
  return { ...ctx, params: { ...ctx.params, id: personId }, principal: { ...ctx.principal, organizationId } };
}
export function registerAdminRoutes(router: Router) {
  router.get("/v1/admin/organizations", async (ctx) => {
    requireSuper(ctx);
    const organizations = await withoutTenant(async ({ sql }) => {
      const orgs = await sql`SELECT id, name, slug, created_at AS "createdAt" FROM organizations ORDER BY name, id`;
      for (const org of orgs) {
        const detail = await withOrg(org.id, async ({ sql }) => {
          const [counts] = await sql`SELECT count(*)::int AS members, count(*) FILTER (WHERE role='admin')::int AS admins FROM people WHERE removed_at IS NULL`;
          const [inv] = await sql`SELECT count(*)::int AS invitations FROM invitations WHERE accepted_at IS NULL AND revoked_at IS NULL AND expires_at>now()`;
          return { ...counts, pendingInvitations: inv.invitations };
        }); Object.assign(org, detail);
      }
      return orgs;
    }); return json({ organizations });
  });
  router.post("/v1/admin/organizations", async (ctx) => {
    requireSuper(ctx);
    const input = await parseBody(ctx.request, z.object({ name: z.string().trim().min(1).max(100), slug: z.string().regex(/^[a-z0-9]+(?:-[a-z0-9]+)*$/).max(100),
      email: z.string().trim().email().max(320) }).strict());
    const id = newId("organization");
    const result = await withoutTenant(async ({ sql }) => {
      const rows = await sql`INSERT INTO organizations (id,name,slug) VALUES (${id},${input.name},${input.slug}) ON CONFLICT (slug) DO NOTHING RETURNING id`;
      if (!rows.length) throw conflict("slug is already used");
      // Tenant scope stays transaction-local; org and first invitation commit together.
      await sql`SELECT set_config('app.organization_id', ${id}, true)`;
      return inviteBatch({ organizationId: id, sql }, { invitations: [{ email: input.email }], role: "admin" }, null);
    }); return json({ organization: { id, name: input.name, slug: input.slug }, ...result[0] }, 201);
  });
  router.get("/v1/admin/organizations/:id", async (ctx) => {
    requireSuper(ctx);
    return json(await withOrg(ctx.params.id!, async ({ sql }) => {
      const [organization] = await sql`SELECT id,name,slug FROM organizations WHERE id=${ctx.params.id!}`;
      if (!organization) throw new HttpError(404,"not_found","not_found");
      const people = await sql`SELECT person_ref(p)::jsonb || jsonb_build_object('email',email,'role',role,'accountId',account_id) AS person FROM people p WHERE removed_at IS NULL ORDER BY name`;
      return { organization, people: people.map((p: { person: unknown })=>p.person), ...await listInvitations({ organizationId: ctx.params.id!, sql }, ctx.url) };
    }));
  });
  const path = "/v1/admin/organizations/:id/invitations";
  router.get(path, async (ctx) => { requireSuper(ctx); return json(await withOrg(ctx.params.id!, s=>listInvitations(s,ctx.url))); });
  router.post(path, async (ctx) => {
    requireSuper(ctx); const input=await parseBody(ctx.request,invitationInput);
    return json({ results:await withOrg(ctx.params.id!,async s=>{ await lockPeople(s); return inviteBatch(s,input,null); }) });
  });
  router.post(`${path}/:invId/renew`,async ctx=>{ requireSuper(ctx);return json(await withOrg(ctx.params.id!,async s=>{await lockPeople(s);return changeInvitation(s,ctx.params.invId!,true,null);})); });
  router.delete(`${path}/:invId`,async ctx=>{requireSuper(ctx);await withOrg(ctx.params.id!,async s=>{await lockPeople(s);await changeInvitation(s,ctx.params.invId!,false,null);});return noContent();});
  router.delete("/v1/admin/organizations/:id/people/:personId",async ctx=>{
    requireSuper(ctx);return removePerson(inOrg(ctx,ctx.params.id!,ctx.params.personId!));
  });
  router.patch("/v1/admin/people/:id",async ctx=>{
    requireSuper(ctx);
    const input=await parseBody(ctx.request,z.object({email:z.string().trim().email().max(320).optional(),role:z.enum(["admin","member"]).optional()}).strict());
    if ((!input.email && !input.role) || (input.email && input.role)) throw new HttpError(400,"change email or role","bad_request");
    const orgs=await withoutTenant(({sql})=>sql`SELECT id FROM organizations`);
    for (const org of orgs) {
      const found=await withOrg(org.id,async s=>{
        await lockPeople(s);
        const [p]=await s.sql`SELECT * FROM people WHERE id=${ctx.params.id!} AND removed_at IS NULL FOR UPDATE`;
        if (!p) return false;
        if (input.email) {
          if (p.account_id) throw conflict("only email-less members can be linked here");
          const [a]=await s.sql`INSERT INTO accounts (id,email,name,photo_url,photo_key,photo_token)
            VALUES (${`acc_${crypto.randomUUID()}`},${input.email.toLowerCase()},${p.name},${p.photo_url},${p.photo_key},${p.photo_token})
            ON CONFLICT(email) DO UPDATE SET email=EXCLUDED.email RETURNING *`;
          const existing=await s.sql`SELECT id FROM people WHERE account_id=${a.id} AND removed_at IS NULL`;
          if(existing.length) throw conflict("already a member");
          await s.sql`UPDATE people SET account_id=${a.id},email=${a.email},name=${a.name},photo_url=${a.photo_url},photo_key=${a.photo_key},photo_token=${a.photo_token} WHERE id=${p.id}`;
        } return true;
      });
      if(found) {
        if(input.role) {
          const request=new Request(ctx.request.url,{method:"PATCH",headers:{"content-type":"application/json"},body:JSON.stringify({role:input.role})});
          return updatePerson({...inOrg(ctx,org.id,ctx.params.id!),request});
        } return json({id:ctx.params.id,email:input.email});
      }
    } throw new HttpError(404,"not_found","not_found");
  });
}
