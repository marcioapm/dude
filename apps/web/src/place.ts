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
  // A task's page may name the tab it opens on: its Servers, where a Run's servers live.
  | { view: "tree"; ref: NavRef; tab?: TaskTab }
  | { view: "projectSettings"; projectId: string; page?: string }
  // sub: deeper than a page, as the page reads it (Images: "<image>/<tab>", "<image>/builds/<build>").
  | { view: "orgSettings"; page?: string; sub?: string }
  | { view: "mySettings" }
  | { view: "inbox" };

/** The task tabs a URL can name. */
export type TaskTab = "servers";

const TREE_KINDS: ReadonlyArray<NavRef["kind"]> = ["project", "epic", "task", "run", "session"];

export function parsePlace(hash: string): Place | null {
  const [given, id, view, page, ...rest] = hash.replace(/^#\/?/, "").split("/");
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
  return kind === "task" && view === "servers" ? { view: "tree", ref, tab: "servers" } : { view: "tree", ref };
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
      return `#/${place.ref.kind}/${encodeURIComponent(place.ref.id)}${place.tab ? `/${place.tab}` : ""}`;
  }
}

/** A place in the tree, from a tree reference. */
export const inTree = (ref: NavRef, tab?: TaskTab): Place => (tab ? { view: "tree", ref, tab } : { view: "tree", ref });

/** What the tree has selected: nothing, for the places it does not contain. */
export const treeSelection = (place: Place | null): NavRef | null => (place?.view === "tree" ? place.ref : null);
