/**
 * Where the app is, and its URL.
 *
 * A place is the welcome, something in the tree (a project's board, an epic's, a work
 * item, an agent's conversation), a project's settings, the organization's,
 * or your own (this browser's: how dude looks, what it tells you). The URL hash is written from it and read back into it by
 * one pair of inverse functions, so a reload lands where you were.
 */

import type { NavRef } from "@dude/design-system";

export type Place =
  // The welcome (`#/`): what dude opens on, and where New session goes.
  | { view: "welcome" }
  // Welcome to <organisation>: where Join lands (`#/o/<slug>/welcome`).
  | { view: "joined" }
  // A task's page may name the tab it opens on, and the attempt it shows
  // when that is not the current one (`?attempt=1`). An epic's page, its
  // Coordination view (`#/epic/<id>/coordination`) in place of its board.
  | { view: "tree"; ref: NavRef; tab?: TaskTab; attempt?: number; epicView?: EpicView }
  | { view: "projectSettings"; projectId: string; page?: string }
  // sub: deeper than a page, as the page reads it (Images: "<image>/<tab>", "<image>/builds/<build>").
  | { view: "orgSettings"; page?: string; sub?: string }
  | { view: "mySettings" }
  | { view: "inbox" }
  // Brainstorm sessions: the list, and one (`#/sessions/<id>`; `#/session/<id>`
  // is an agent's session inside a task's Run).
  | { view: "sessions" }
  | { view: "brainstorm"; id: string };

/** The task tabs a URL can name; Overview is the task's own URL. */
export type TaskTab = "chat" | "findings" | "sessions" | "files" | "servers" | "activity";
const TASK_TABS: ReadonlyArray<TaskTab> = ["chat", "findings", "sessions", "files", "servers", "activity"];

/** An epic's page shows its board, or how its tasks' agents coordinate. */
export type EpicView = "board" | "coordination";

/** Tabs whose content is one attempt's: the others show the whole task, whatever attempt is picked. */
export const attemptScoped = (tab: TaskTab | undefined): boolean => tab !== "servers" && tab !== "activity" && tab !== "chat";

const TREE_KINDS: ReadonlyArray<NavRef["kind"]> = ["project", "epic", "task", "run", "session"];

/**
 * The organisation this tab's places are in: every hash formatPlace writes
 * starts `#/o/<slug>/`, so a link copied from it opens in the same
 * organisation. Set by the shell when it mounts an organisation; null (an
 * older link, a test) writes the bare hash.
 */
let organizationSlug: string | null = null;

export function setPlaceOrganization(slug: string | null): void {
  organizationSlug = slug;
}

/** `#/o/<slug>/rest` → the slug and `#/rest`; anything else has no slug. */
export function splitOrganization(hash: string): { slug: string | null; rest: string } {
  const m = /^#?\/?o\/([^/?#]+)\/?(.*)$/.exec(hash);
  if (!m) return { slug: null, rest: hash };
  return { slug: decodeURIComponent(m[1]!), rest: `#/${m[2] ?? ""}` };
}

/** A tenant hash in `slug`: what a place's link is once its organisation is known. */
export function inOrganization(slug: string, hash: string): string {
  return `#/o/${encodeURIComponent(slug)}/${hash.replace(/^#?\/?/, "")}`;
}

export function parsePlace(full: string): Place | null {
  const hash = splitOrganization(full).rest;
  const [path = "", query = ""] = hash.replace(/^#\/?/, "").split("?", 2);
  // Nothing in the URL is the welcome.
  if (path === "" || path === " ") return { view: "welcome" };
  // The first page in an organisation just joined.
  if (path === "welcome") return { view: "joined" };
  const [given, id, view, page, ...rest] = path.split("/");
  // Links from before "work item" became "task": bookmarks, and
  // notifications already delivered.
  const kind = given === "workItem" ? "task" : given;
  // A settings page (a role under Agents, Delivery) is part of the place.
  if (kind === "org" && id === "settings") {
    if (!view) return { view: "orgSettings" };
    const sub = [page, ...rest].filter((p): p is string => Boolean(p)).map((p) => decodeURIComponent(p)).join("/");
    return sub ? { view: "orgSettings", page: view, sub } : { view: "orgSettings", page: view };
  }
  if (kind === "me" && id === "settings") return { view: "mySettings" };
  if (kind === "waiting") return { view: "inbox" };
  if (kind === "sessions") return id ? { view: "brainstorm", id: decodeURIComponent(id) } : { view: "sessions" };
  if (!kind || !id || !TREE_KINDS.includes(kind as NavRef["kind"])) return null;
  const decoded = decodeURIComponent(id);
  if (kind === "project" && view === "settings") {
    return page ? { view: "projectSettings", projectId: decoded, page } : { view: "projectSettings", projectId: decoded };
  }
  const ref = { kind: kind as NavRef["kind"], id: decoded };
  if (kind === "epic" && view === "coordination") return { view: "tree", ref, epicView: "coordination" };
  if (kind !== "task") return { view: "tree", ref };
  const tab = TASK_TABS.find((t) => t === view);
  const asked = new URLSearchParams(query).get("attempt");
  const attempt = asked && /^[1-9]\d{0,8}$/.test(asked) ? Number(asked) : undefined;
  return inTree(ref, tab, attempt);
}

export function formatPlace(place: Place | null): string {
  const hash = bareHash(place);
  return hash && organizationSlug ? inOrganization(organizationSlug, hash) : hash;
}

function bareHash(place: Place | null): string {
  if (!place) return "";
  switch (place.view) {
    case "welcome":
      return "#/";
    case "joined":
      return "#/welcome";
    case "orgSettings":
      return place.page
        ? `#/org/settings/${place.page}${place.sub ? `/${place.sub.split("/").map(encodeURIComponent).join("/")}` : ""}`
        : "#/org/settings";
    case "mySettings":
      return "#/me/settings";
    case "inbox":
      return "#/waiting";
    case "sessions":
      return "#/sessions";
    case "brainstorm":
      return `#/sessions/${encodeURIComponent(place.id)}`;
    case "projectSettings":
      return `#/project/${encodeURIComponent(place.projectId)}/settings${place.page ? `/${place.page}` : ""}`;
    case "tree":
      if (place.ref.kind === "epic" && place.epicView === "coordination") return `#/epic/${encodeURIComponent(place.ref.id)}/coordination`;
      return `#/${place.ref.kind}/${encodeURIComponent(place.ref.id)}${place.tab ? `/${place.tab}` : ""}${place.attempt ? `?attempt=${place.attempt}` : ""}`;
  }
}

/**
 * A place in the tree, from a tree reference. The attempt is kept only on
 * a tab that shows one attempt: Chat, Activity and Servers show them all.
 */
export const inTree = (ref: NavRef, tab?: TaskTab, attempt?: number): Place => ({
  view: "tree", ref, ...(tab ? { tab } : {}), ...(attempt && attemptScoped(tab) ? { attempt } : {}),
});

/** What the tree has selected: nothing, for the places it does not contain. */
export const treeSelection = (place: Place | null): NavRef | null => (place?.view === "tree" ? place.ref : null);
