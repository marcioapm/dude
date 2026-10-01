/**
 * The image library's words, for the Images pages and the Run's header:
 * an image's state in a row, who uses it, where a build is, and what the
 * Containerfile editor completes. Pure, so tests read them without a page.
 */

import {
  COMMON_BASES,
  CONTAINERFILE_INSTRUCTIONS,
  SETTINGS_ROLE_LABEL,
  type ImageBuild,
  type ImageSummary,
  type ImageUse,
  type PromptRole,
} from "@dude/domain";
import type { BuildStage, CodeCompletion, CodeCompletionContext, ImageStateKind } from "@dude/design-system/components";

const ordinal = (n: number) => `${n}${n % 10 === 1 && n % 100 !== 11 ? "st" : n % 10 === 2 && n % 100 !== 12 ? "nd" : n % 10 === 3 && n % 100 !== 13 ? "rd" : "th"}`;

/** Where in the line a waiting job is: "next", "2nd", … (ahead counts every organization's jobs). */
export function queuePlace(ahead: number | null): string {
  if (ahead === null) return "";
  return ahead === 0 ? "next" : ordinal(ahead + 1);
}

/** An image's state in its row: published, building, waiting, failed, or nothing yet. */
export function imageState(image: ImageSummary, queue: readonly ImageBuild[], ago: (iso: string) => string): { kind: ImageStateKind; words: string } {
  if (image.archivedAt) return { kind: "archived", words: "Archived" };
  const p = image.pending;
  const job = p ? queue.find((b) => b.versionId === p.versionId && b.kind === "build") : undefined;
  if (p && (p.state === "building" || p.state === "pushing" || job?.state === "running")) {
    return { kind: "building", words: `Building v${p.number}${job?.startedAt ? ` · ${ago(job.startedAt).replace(/ ago$/, "")}` : ""}` };
  }
  if (p && p.state === "queued") {
    const parent = image.parents.find((x) => queue.some((b) => b.imageId === x.id));
    if (parent) return { kind: "waiting", words: `Rebuilds after ${parent.name}` };
    return { kind: "waiting", words: `Waiting${job?.ahead !== null && job?.ahead !== undefined ? ` · ${queuePlace(job.ahead)}` : ""}` };
  }
  if (p && p.state === "failed") {
    return { kind: "failed", words: image.published ? `v${p.number} failed · v${image.published.number} still live` : `v${p.number} failed` };
  }
  if (image.published) return { kind: "published", words: `Published${image.published.builtAt ? ` · ${ago(image.published.builtAt)}` : ""}` };
  if (image.draft) return { kind: "draft", words: "Draft not built" };
  return { kind: "none", words: "Nothing published" };
}

const roleName = (role: string | undefined) => (role ? (SETTINGS_ROLE_LABEL[role as PromptRole] ?? role) : "");

/** Who uses an image, in a few words: "Default · Greeter · 4 images FROM it". */
export function usedByWords(uses: readonly ImageUse[], orgName: string): string {
  const parts: string[] = [];
  if (uses.some((u) => u.kind === "organization_default")) parts.push("Default");
  const roles = uses.filter((u) => u.kind === "role").map((u) => `${roleName(u.role)} (${orgName})`);
  parts.push(...roles);
  const projects = new Map<string, string[]>();
  for (const u of uses) {
    if (!u.project || !["runtime", "preview", "project_role"].includes(u.kind)) continue;
    const what = u.kind === "runtime" ? "runtime" : u.kind === "preview" ? "previews" : roleName(u.role);
    projects.set(u.project.name, [...(projects.get(u.project.name) ?? []), what]);
  }
  for (const [name, what] of projects) parts.push(`${name} · ${what.join(", ")}`);
  const children = uses.filter((u) => u.kind === "child").length;
  if (children) parts.push(children === 1 ? `${uses.find((u) => u.kind === "child")!.image!.name} FROM it` : `${children} images FROM it`);
  return parts.length ? parts.join("; ") : "Nobody yet";
}

/** A build's stages for BuildStages: waiting, building (and finishing), pushed and published. */
export function buildStages(b: Pick<ImageBuild, "state" | "stage" | "kind" | "error">, limits: { cpus: number; memoryMiB: number }, publishedNumber: number | null): BuildStage[] {
  const spec = `rootless · ${limits.cpus} CPU · ${trimGb(limits.memoryMiB)}`;
  const finish = b.kind === "finish";
  const last = finish ? "Dude layer added" : "Pushed and published";
  const lastDetail = finish ? "for the Runs waiting on it" : "dude/custom, then live";
  if (b.state === "queued") {
    return [
      { id: "wait", label: "Waiting", detail: "in the queue", state: "current" },
      { id: "build", label: finish ? "Adding the dude layer" : "Building", detail: spec, state: "todo" },
      { id: "done", label: last, detail: lastDetail, state: "todo" },
    ];
  }
  if (b.state === "running") {
    const late = b.stage === "publishing";
    return [
      { id: "wait", label: "Waiting", detail: "in the queue", state: "done" },
      { id: "build", label: b.stage === "pushing" ? "Pushing" : b.stage === "finishing" ? "Adding the dude layer" : b.stage === "resolving" ? "Resolving its base" : "Building", detail: spec, state: late ? "done" : "current" },
      { id: "done", label: last, detail: lastDetail, state: late ? "current" : "todo" },
    ];
  }
  if (b.state === "succeeded") {
    return [
      { id: "wait", label: "Waiting", detail: "in the queue", state: "done" },
      { id: "build", label: finish ? "Dude layer built" : "Built", detail: spec, state: "done" },
      { id: "done", label: finish ? "Dude layer added" : "Published", detail: finish ? "Runs start on it" : "every user gets it on their next Run", state: "done" },
    ];
  }
  return [
    { id: "wait", label: "Waiting", detail: "in the queue", state: "done" },
    { id: "build", label: b.state === "cancelled" ? "Cancelled" : "Failed", detail: b.error ?? "", state: "failed" },
    { id: "done", label: "Not published", detail: publishedNumber ? `v${publishedNumber} is still live` : "nothing changed", state: "todo" },
  ];
}

function trimGb(mib: number): string {
  const gb = mib / 1024;
  return `${Number.isInteger(gb) ? gb : gb.toFixed(1)} GB`;
}

/** The builder's limits, as the queue strip shows them. */
export const builderLimits = (b: { cpus: number; memoryMiB: number }) => ["Rootless", `${b.cpus} CPU`, trimGb(b.memoryMiB), "One at a time"];

/**
 * What the Containerfile editor offers: after FROM (or --from=), the
 * library's images then common bases; at a line's start, the instructions.
 */
export function containerfileCompletions(
  ctx: CodeCompletionContext,
  images: ReadonlyArray<{ name: string; version: number | null; isDefault: boolean }>,
  orgName: string,
  self?: string,
): CodeCompletion[] | null {
  const before = ctx.before.slice(0, ctx.before.length - ctx.word.length);
  const word = ctx.word.toLowerCase();
  if (/^\s*FROM\s+(--\S+\s+)*$/i.test(before) || /--from=$/i.test(before)) {
    const lib = images
      .filter((i) => i.name !== self)
      .map((i) => ({ label: `image:${i.name}`, detail: `${i.isDefault ? `${orgName}’s default base` : orgName}${i.version ? ` · v${i.version}` : " · nothing published"}`, type: "image" as const, boost: i.isDefault ? 2 : 1 }));
    const bases = COMMON_BASES.map((b) => ({ label: b, detail: "registry", type: "image" as const }));
    return [...lib, ...bases].filter((c) => c.label.toLowerCase().startsWith(word) || c.label.toLowerCase().startsWith(`image:${word}`));
  }
  if (/^\s*$/.test(before)) {
    return CONTAINERFILE_INSTRUCTIONS.filter((i) => i.name.toLowerCase().startsWith(word)).map((i) => ({ label: i.name, detail: i.detail, type: "keyword" as const }));
  }
  return null;
}

/** "+4 −3 against v4": a draft's change against the version it was made from. */
export function draftCounts(before: string, after: string): { add: number; del: number } {
  const a = before.split("\n");
  const b = after.split("\n");
  const inA = new Map<string, number>();
  for (const l of a) inA.set(l, (inA.get(l) ?? 0) + 1);
  let common = 0;
  for (const l of b) {
    const n = inA.get(l) ?? 0;
    if (n > 0) {
      common++;
      inA.set(l, n - 1);
    }
  }
  return { add: b.length - common, del: a.length - common };
}
