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
  | { view: "tree"; ref: NavRef }
  | { view: "projectSettings"; projectId: string }
  | { view: "orgSettings" }
  | { view: "mySettings" };

const TREE_KINDS: ReadonlyArray<NavRef["kind"]> = ["project", "epic", "workItem", "run", "session"];

export function parsePlace(hash: string): Place | null {
  const [kind, id, view] = hash.replace(/^#\/?/, "").split("/");
  if (kind === "org" && id === "settings") return { view: "orgSettings" };
  if (kind === "me" && id === "settings") return { view: "mySettings" };
  if (!kind || !id || !TREE_KINDS.includes(kind as NavRef["kind"])) return null;
  const decoded = decodeURIComponent(id);
  if (kind === "project" && view === "settings") return { view: "projectSettings", projectId: decoded };
  return { view: "tree", ref: { kind: kind as NavRef["kind"], id: decoded } };
}

export function formatPlace(place: Place | null): string {
  if (!place) return "";
  switch (place.view) {
    case "orgSettings":
      return "#/org/settings";
    case "mySettings":
      return "#/me/settings";
    case "projectSettings":
      return `#/project/${encodeURIComponent(place.projectId)}/settings`;
    case "tree":
      return `#/${place.ref.kind}/${encodeURIComponent(place.ref.id)}`;
  }
}

/** A place in the tree, from a tree reference. */
export const inTree = (ref: NavRef): Place => ({ view: "tree", ref });

/** What the tree has selected: nothing, for the places it does not contain. */
export const treeSelection = (place: Place | null): NavRef | null => (place?.view === "tree" ? place.ref : null);
