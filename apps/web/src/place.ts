/**
 * Where the app is, and its URL.
 *
 * A place is something in the tree (a project's board, an epic's, a work
 * item, an agent's conversation), a project's settings, the organization's,
 * or your own (this browser's: how dude looks, what it tells you). The URL hash is written from it and read back into it by
 * one pair of inverse functions, so a reload lands where you were.
 */

import type { NavRef } from "@dude/design-system";

export type Place =
  // A task's page may name the tab it opens on, and the attempt it shows
  // when that is not the current one (`?attempt=1`).
  | { view: "tree"; ref: NavRef; tab?: TaskTab; attempt?: number }
  | { view: "projectSettings"; projectId: string; page?: string }
  // sub: deeper than a page, as the page reads it (Images: "<image>/<tab>", "<image>/builds/<build>").
  | { view: "orgSettings"; page?: string; sub?: string }
  | { view: "mySettings" }
  | { view: "inbox" };

/** The task tabs a URL can name; Overview is the task's own URL. */
export type TaskTab = "findings" | "sessions" | "files" | "servers" | "activity";
const TASK_TABS: ReadonlyArray<TaskTab> = ["findings", "sessions", "files", "servers", "activity"];

/** Tabs whose content is one attempt's: the others show the whole task, whatever attempt is picked. */
export const attemptScoped = (tab: TaskTab | undefined): boolean => tab !== "servers" && tab !== "activity";

const TREE_KINDS: ReadonlyArray<NavRef["kind"]> = ["project", "epic", "task", "run", "session"];

export function parsePlace(hash: string): Place | null {
  const [path = "", query = ""] = hash.replace(/^#\/?/, "").split("?", 2);
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
  if (!kind || !id || !TREE_KINDS.includes(kind as NavRef["kind"])) return null;
  const decoded = decodeURIComponent(id);
  if (kind === "project" && view === "settings") {
    return page ? { view: "projectSettings", projectId: decoded, page } : { view: "projectSettings", projectId: decoded };
  }
  const ref = { kind: kind as NavRef["kind"], id: decoded };
  if (kind !== "task") return { view: "tree", ref };
  const tab = TASK_TABS.find((t) => t === view);
  const asked = new URLSearchParams(query).get("attempt");
  const attempt = asked && /^[1-9]\d{0,8}$/.test(asked) ? Number(asked) : undefined;
  return inTree(ref, tab, attempt);
}

export function formatPlace(place: Place | null): string {
  if (!place) return "";
  switch (place.view) {
    case "orgSettings":
      return place.page
        ? `#/org/settings/${place.page}${place.sub ? `/${place.sub.split("/").map(encodeURIComponent).join("/")}` : ""}`
        : "#/org/settings";
    case "mySettings":
      return "#/me/settings";
    case "inbox":
      return "#/waiting";
    case "projectSettings":
      return `#/project/${encodeURIComponent(place.projectId)}/settings${place.page ? `/${place.page}` : ""}`;
    case "tree":
      return `#/${place.ref.kind}/${encodeURIComponent(place.ref.id)}${place.tab ? `/${place.tab}` : ""}${place.attempt ? `?attempt=${place.attempt}` : ""}`;
  }
}

/**
 * A place in the tree, from a tree reference. The attempt is kept only on
 * a tab that shows one attempt: Activity and Servers show them all.
 */
export const inTree = (ref: NavRef, tab?: TaskTab, attempt?: number): Place => ({
  view: "tree", ref, ...(tab ? { tab } : {}), ...(attempt && attemptScoped(tab) ? { attempt } : {}),
});

/** What the tree has selected: nothing, for the places it does not contain. */
export const treeSelection = (place: Place | null): NavRef | null => (place?.view === "tree" ? place.ref : null);
