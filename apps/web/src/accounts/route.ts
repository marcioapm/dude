/**
 * What the address names before any organisation is mounted
 * (docs/design/accounts.md, "How a request knows its organisation"): a
 * tenant page `#/o/<slug>/…`, an invitation `#/invite/<code>`, the
 * super-admin's `#/admin` and `#/admin/o/<slug>`, the invitations waiting
 * for you `#/invitations`, the bare address, or a link from before URLs
 * named an organisation (`#/task/wi_…`), which is found among your
 * organisations and rewritten.
 */

import { splitOrganization } from "../place.ts";

export type Route =
  | { kind: "tenant"; slug: string; rest: string }
  | { kind: "invite"; code: string }
  | { kind: "admin"; slug: string | null }
  | { kind: "invitations" }
  | { kind: "home" }
  // An older tenant link: `id` is what `/v1/locate` can place, when it names one.
  | { kind: "legacy"; rest: string; id: string | null };

/** The id an older link names, for `/v1/locate`: a tree item, a project's settings, a brainstorm session. */
function idOf(path: string): string | null {
  const [kind, id] = path.split("/");
  if (!id) return null;
  if (["task", "workItem", "project", "epic", "run", "session", "sessions"].includes(kind ?? "")) return decodeURIComponent(id);
  return null;
}

export function parseRoute(hash: string): Route {
  const { slug, rest } = splitOrganization(hash);
  if (slug) return { kind: "tenant", slug, rest };
  const path = hash.replace(/^#\/?/, "").split("?")[0]!.trim();
  if (path === "") return { kind: "home" };
  const parts = path.split("/");
  if (parts[0] === "invite" && parts[1]) return { kind: "invite", code: decodeURIComponent(parts[1]) };
  if (parts[0] === "admin") return { kind: "admin", slug: parts[1] === "o" && parts[2] ? decodeURIComponent(parts[2]) : null };
  if (parts[0] === "invitations" && !parts[1]) return { kind: "invitations" };
  return { kind: "legacy", rest: hash.startsWith("#") ? hash : `#${hash}`, id: idOf(path) };
}

export const adminHash = (slug?: string | null) => (slug ? `#/admin/o/${encodeURIComponent(slug)}` : "#/admin");
export const inviteHash = (code: string) => `#/invite/${encodeURIComponent(code)}`;

/** The browser's last organisation: only where a bare address opens (`dude.org`). */
export const LAST_ORGANIZATION = "dude.org";
