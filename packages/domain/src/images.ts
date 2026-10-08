import { z } from "zod";
import type { AgentModels } from "./hierarchy.ts";

/**
 * The image library: the organization's images, each a history of
 * Containerfiles. Everything that runs in an image names it by id and gets
 * its latest published version on its next Run. A version is a draft until
 * "Build & publish" numbers and queues it; dude-image-builder builds it,
 * pushes it, adds the dude layer, and only then publishes it.
 * Design: docs/design/images.md.
 */

/** An image's name: what `FROM image:<name>` says. Immutable. */
export const IMAGE_NAME = /^[a-z0-9][a-z0-9-]{0,62}$/;
export const IMAGE_NAME_MESSAGE = "Lowercase letters, digits and dashes, starting with a letter or digit; at most 63";
export const CONTAINERFILE_MAX_BYTES = 64 * 1024;
export const BUILD_ARGS_MAX = 50;
const BUILD_ARG_NAME = /^[A-Za-z_][A-Za-z0-9_]{0,127}$/;

export const imageNameSchema = z.string().regex(IMAGE_NAME, IMAGE_NAME_MESSAGE);

/** Build arguments: not secrets (the history shows them). */
export const buildArgsSchema = z
  .record(z.string().regex(BUILD_ARG_NAME, "A build argument's name: letters, digits and _"), z.string().max(4096))
  .refine((a) => Object.keys(a).length <= BUILD_ARGS_MAX, `At most ${BUILD_ARGS_MAX} build arguments`)
  .default({});

const containerfileSchema = z
  .string()
  .refine((s) => new TextEncoder().encode(s).length <= CONTAINERFILE_MAX_BYTES, "A Containerfile is at most 64 KiB");

/** `PUT /v1/images/:id/draft`, and `POST /v1/images/:id/build` with a body. */
export const imageDraftSchema = z
  .object({
    containerfile: containerfileSchema,
    buildArgs: buildArgsSchema,
    note: z.string().trim().max(500).default(""),
    /** Omitted: the draft keeps its own value; a new draft starts from the published version's. */
    canRunContainers: z.boolean().optional(),
  })
  .strict();
export type ImageDraftInput = z.infer<typeof imageDraftSchema>;

/** `POST /v1/images`. */
export const newImageSchema = z
  .object({
    name: imageNameSchema,
    description: z.string().trim().max(500).default(""),
    containerfile: containerfileSchema.optional(),
    buildArgs: buildArgsSchema,
    note: z.string().trim().max(500).default(""),
    /** Omitted: the published value of the library image its first FROM names, else false. */
    canRunContainers: z.boolean().optional(),
  })
  .strict();
export type NewImageInput = z.infer<typeof newImageSchema>;

/** `PATCH /v1/images/:id`. */
export const imagePatchSchema = z
  .object({
    description: z.string().trim().max(500).optional(),
    archived: z.boolean().optional(),
  })
  .strict();

export const IMAGE_VERSION_STATES = ["draft", "queued", "building", "pushing", "published", "failed", "superseded", "cancelled"] as const;
export type ImageVersionState = (typeof IMAGE_VERSION_STATES)[number];
export type ImageBuildState = "queued" | "running" | "succeeded" | "failed" | "cancelled";
export type ImageBuildKind = "build" | "finish";
/** Where a running build is. */
export type ImageBuildStage = "resolving" | "building" | "pushing" | "finishing" | "publishing";

export interface ImagePerson {
  id: string;
  name: string;
}

/** A version as an image's history lists it. */
export interface ImageVersion {
  id: string;
  imageId: string;
  /** null for the draft. */
  number: number | null;
  state: ImageVersionState;
  containerfile: string;
  buildArgs: Record<string, string>;
  note: string;
  source: "person" | "base_rebuild";
  createdAt: string;
  updatedAt: string;
  createdBy: ImagePerson | null;
  /** The image without the dude layer, by digest, once built. */
  userRef: string | null;
  builtAt: string | null;
  error: string | null;
  /** Runs in it may start containers: the builder checks it can, and lux places them on hosts that allow it. */
  canRunContainers: boolean;
  /** What it is built FROM in the library, and the parent version its build used. */
  parents: Array<{ imageId: string; name: string; versionId: string | null; version: number | null }>;
}

/** One job of the builder's queue. */
export interface ImageBuild {
  id: string;
  imageId: string;
  imageName: string;
  versionId: string;
  version: number | null;
  kind: ImageBuildKind;
  state: ImageBuildState;
  stage: ImageBuildStage | null;
  layerRef: string | null;
  requestedBy: ImagePerson | null;
  requestedAt: string;
  startedAt: string | null;
  finishedAt: string | null;
  error: string | null;
  buildSeconds: number | null;
  pushSeconds: number | null;
  /** Its version can run containers, so the build checks it can (the "Check containers" stage). */
  canRunContainers: boolean;
  /** What that check found, one line; null before it ran. */
  containersCheck: string | null;
  checkSeconds: number | null;
  /** Jobs the builder takes before this one, of any organization; null unless queued. */
  ahead: number | null;
}

export interface ImageBuildWithLog extends ImageBuild {
  /**
   * The log from byte logStart of everything the build ever wrote to
   * logTotal: the whole kept log (its last 1 MiB), or with `?after=<n>`
   * only what came after byte n when the kept log still holds it.
   */
  log: string;
  logStart: number;
  logTotal: number;
  /** The image's version that is live while this one builds. */
  published: { versionId: string; number: number } | null;
  note: string;
  /** The limits every build runs under. */
  builder: ImageBuilderInfo;
}

/** Who names an image. */
export interface ImageUse {
  kind: "organization_default" | "role" | "project_role" | "runtime" | "preview" | "child";
  role?: string;
  project?: { id: string; name: string } | null;
  /** For a child: the image built FROM this one. */
  image?: { id: string; name: string };
}

/** An image as the list and the pickers show it. */
export interface ImageSummary {
  id: string;
  name: string;
  description: string;
  archivedAt: string | null;
  createdAt: string;
  createdBy: ImagePerson | null;
  isDefault: boolean;
  published: { versionId: string; number: number; builtAt: string | null; userRef: string | null; canRunContainers: boolean } | null;
  /** The newest numbered version, when it is not the published one: building, waiting or failed. */
  pending: { versionId: string; number: number; state: ImageVersionState; error: string | null } | null;
  /** Its draft, if someone is editing it. */
  draft: { versionId: string; updatedAt: string; updatedBy: ImagePerson | null } | null;
  /** The library images its latest version is built FROM. */
  parents: Array<{ id: string; name: string }>;
  /** Its latest version's first FROM, as written. */
  from: string | null;
  usedBy: ImageUse[];
  lastChange: { at: string; by: ImagePerson | null; source: "person" | "base_rebuild" };
}

/** The builder, as the settings page explains it. */
export interface ImageBuilderInfo {
  /** Builds can run: the dude layer is configured. */
  available: boolean;
  /** The dude layer, a digest ref; null when the library is off. */
  layer: string | null;
  cpus: number;
  memoryMiB: number;
  /** dude-image-builder's last heartbeat (every 30 s), null if it never ran. */
  lastSeenAt: string | null;
  /** Builds are on, and the builder has not been heard from for BUILDER_OFFLINE_SECONDS. */
  offline: boolean;
}

export interface ImagesResponse {
  images: ImageSummary[];
  /** The organization's running and waiting jobs, the builder's order. */
  queue: ImageBuild[];
  defaultImageId: string | null;
  builder: ImageBuilderInfo;
  canEdit: boolean;
}

export interface ImageDetail {
  image: ImageSummary;
  /** Newest first; the draft first of all. */
  versions: ImageVersion[];
  /** Newest first, without logs. */
  builds: ImageBuild[];
  builder: ImageBuilderInfo;
  canEdit: boolean;
}

/** What a picker lists (`GET /v1/images/picker`). */
export interface ImageChoice {
  id: string;
  name: string;
  description: string;
  version: number | null;
  isDefault: boolean;
  archived: boolean;
  /** Its published version can run containers. */
  canRunContainers: boolean;
  /** A newer version building or waiting, or failed since the published one. */
  status: { kind: "building" | "waiting" | "failed"; version: number } | null;
}

/** What a Run got (`runs.image`). */
export interface RunImage {
  imageId: string;
  name: string;
  versionId: string;
  version: number;
  /** The final image lux pulls, by digest. */
  ref: string;
  layer: string;
  /** The version could run containers, so the Run asked lux for a host that allows them. Absent on Runs from before it. */
  canRunContainers?: boolean;
}

// ---------------------------------------------------------------------------
// Containerfile lint
// ---------------------------------------------------------------------------

export type LintSeverity = "error" | "warning";

export interface LintDiagnostic {
  severity: LintSeverity;
  /** 1-based line, and 0-based columns on it [from, to). */
  line: number;
  from: number;
  to: number;
  message: string;
}

/** One instruction, its continuation lines joined. */
interface Instruction {
  keyword: string;
  /** Its text after the keyword, continuations joined with a space. */
  args: string;
  line: number;
  /** Where args start on the first line. */
  argsColumn: number;
  /** The first line's text. */
  text: string;
}

/** The instructions a Containerfile has, in order; comments and blank lines left out. */
function instructions(text: string): Instruction[] {
  const lines = text.split(/\r?\n/);
  const out: Instruction[] = [];
  let escape = "\\";
  let seenInstruction = false;
  for (let i = 0; i < lines.length; i++) {
    const raw = lines[i] ?? "";
    const trimmed = raw.trim();
    if (!seenInstruction) {
      const directive = /^#\s*escape\s*=\s*(\S)\s*$/i.exec(trimmed);
      if (directive) {
        escape = directive[1] ?? "\\";
        continue;
      }
    }
    if (trimmed === "" || trimmed.startsWith("#")) continue;
    seenInstruction = true;
    const head = /^(\s*)([A-Za-z]+)(\s+|$)/.exec(raw);
    if (!head) continue;
    const keyword = (head[2] ?? "").toUpperCase();
    const argsColumn = head[0].length;
    let args = raw.slice(argsColumn);
    const start = i;
    while (args.trimEnd().endsWith(escape) && i + 1 < lines.length) {
      args = args.trimEnd().slice(0, -1);
      i++;
      const next = lines[i] ?? "";
      if (next.trim().startsWith("#")) continue;
      args += " " + next.trim();
    }
    out.push({ keyword, args: args.trim(), line: start + 1, argsColumn, text: raw });
  }
  return out;
}

/** `--name=value` flags at the start of an instruction's arguments, and what follows them. */
function flags(args: string): { flags: Record<string, string>; rest: string } {
  const out: Record<string, string> = {};
  let rest = args;
  for (;;) {
    const m = /^--([a-z-]+)(?:=(\S*))?\s*/.exec(rest);
    if (!m) break;
    out[m[1] ?? ""] = m[2] ?? "";
    rest = rest.slice(m[0].length);
  }
  return { flags: out, rest };
}

function columnOf(ins: Instruction, word: string): { from: number; to: number } {
  const at = ins.text.indexOf(word, ins.argsColumn);
  if (at < 0) return { from: ins.argsColumn, to: ins.text.length };
  return { from: at, to: at + word.length };
}

const IMAGE_REF = /^image:([^\s@:]*)$/;

/** A registry reference names an exact image only by digest. */
const pinned = (ref: string) => /@sha256:[0-9a-f]{64}$/.test(ref);

export interface LintContext {
  /** The organization's image names (archived ones too: they still build). */
  images: ReadonlyArray<string>;
  /** The image being edited, which cannot be built FROM itself. */
  self?: string;
}

/**
 * What is wrong with a Containerfile, as the editor marks it and the API
 * refuses it (errors only). Errors: copying build files (an image has
 * none), FROM named by a build argument, FROM an image the library lacks
 * or the image itself, no FROM at all. Warnings: a registry tag with no
 * digest, which dude records when the version builds but never follows.
 */
export function lintContainerfile(text: string, ctx: LintContext): LintDiagnostic[] {
  const out: LintDiagnostic[] = [];
  const known = new Set(ctx.images);
  const stages = new Set<string>();
  const all = instructions(text);
  if (!all.some((i) => i.keyword === "FROM")) {
    out.push({ severity: "error", line: 1, from: 0, to: (text.split(/\r?\n/)[0] ?? "").length, message: "A Containerfile starts FROM an image" });
  }
  for (const ins of all) {
    if (ins.keyword === "FROM") {
      const { rest } = flags(ins.args);
      const [ref = "", as, stage] = rest.split(/\s+/);
      const at = columnOf(ins, ref || ins.args);
      if (ref === "") {
        out.push({ severity: "error", line: ins.line, from: ins.argsColumn, to: ins.text.length, message: "FROM names an image" });
      } else if (ref.includes("$")) {
        out.push({ severity: "error", line: ins.line, ...at, message: "FROM can't be named by a build argument: dude must know what an image is built on when it is saved" });
      } else if (ref.startsWith("image:")) {
        const name = IMAGE_REF.exec(ref)?.[1] ?? "";
        if (ctx.self && name === ctx.self) {
          out.push({ severity: "error", line: ins.line, ...at, message: `${ctx.self} can't be built FROM itself` });
        } else if (!known.has(name)) {
          out.push({ severity: "error", line: ins.line, ...at, message: name ? `There is no image named ${name} in the library` : "image: names one of the library's images" });
        }
      } else if (ref !== "scratch" && !stages.has(ref.toLowerCase()) && !pinned(ref)) {
        out.push({ severity: "warning", line: ins.line, ...at, message: "dude records its digest when this version builds; it won't follow the tag later" });
      }
      if (as?.toLowerCase() === "as" && stage) stages.add(stage.toLowerCase());
    } else if (ins.keyword === "COPY" || ins.keyword === "ADD") {
      const { flags: f, rest } = flags(ins.args);
      const sources = rest.startsWith("[") ? [] : rest.split(/\s+/).slice(0, -1);
      const remote = ins.keyword === "ADD" && sources.length > 0 && sources.every((s) => /^(https?|git):\/\//.test(s) || s.startsWith("git@"));
      if (f.from === undefined && !remote && !/^<</.test(rest)) {
        out.push({
          severity: "error",
          line: ins.line,
          from: ins.text.search(/\S/),
          to: ins.text.length,
          message: `An image has no build files: ${ins.keyword} only --from a stage or another image`,
        });
      } else if (f.from?.startsWith("image:")) {
        const name = f.from.slice("image:".length);
        if (!known.has(name) || name === ctx.self) {
          out.push({ severity: "error", line: ins.line, ...columnOf(ins, f.from), message: `There is no image named ${name} in the library` });
        }
      } else if (f.from?.includes("$")) {
        out.push({ severity: "error", line: ins.line, ...columnOf(ins, f.from), message: "--from can't be named by a build argument" });
      }
    }
  }
  return out.sort((a, b) => a.line - b.line || a.from - b.from);
}

/** The library images a Containerfile names (`FROM image:x`, `--from=image:x`), in order, once each. */
export function imageReferences(text: string): string[] {
  const out: string[] = [];
  for (const ins of instructions(text)) {
    let name: string | undefined;
    if (ins.keyword === "FROM") {
      const ref = flags(ins.args).rest.split(/\s+/)[0] ?? "";
      name = IMAGE_REF.exec(ref)?.[1];
    } else if (ins.keyword === "COPY" || ins.keyword === "ADD") {
      const from = flags(ins.args).flags.from;
      name = from?.startsWith("image:") ? from.slice("image:".length) : undefined;
    }
    if (name && !out.includes(name)) out.push(name);
  }
  return out;
}

/**
 * Whether the editor warns, before a build, that a version marked "Can run
 * containers" will likely fail its container check: no instruction names
 * podman or docker, and no FROM is a library image that can run containers
 * (its published version). A registry base with an engine baked in is
 * unknowable here; the hint says "unless the base has them".
 */
export function lacksContainerEngine(text: string, images: ReadonlyArray<{ name: string; canRunContainers: boolean }>): boolean {
  const all = instructions(text);
  if (all.some((i) => /podman|docker/i.test(i.args))) return false;
  const able = new Set(images.filter((i) => i.canRunContainers).map((i) => i.name));
  return !all.some((i) => i.keyword === "FROM" && able.has(IMAGE_REF.exec(flags(i.args).rest.split(/\s+/)[0] ?? "")?.[1] ?? ""));
}

/** The library image a Containerfile's first FROM names (`image:x` → x), or null. */
export function fromImage(text: string): string | null {
  const ref = firstFrom(text);
  return ref ? (IMAGE_REF.exec(ref)?.[1] ?? null) : null;
}

/** A Containerfile's first FROM, as written ("image:acme-base", "debian:bookworm-slim"). */
export function firstFrom(text: string): string | null {
  const from = instructions(text).find((i) => i.keyword === "FROM");
  return from ? (flags(from.args).rest.split(/\s+/)[0] ?? null) : null;
}

/**
 * Whether making `image` build FROM `parents` closes a loop, given what
 * every other image is built FROM now (by name). Returns the loop's names
 * from `image` back to it, or null.
 */
export function imageCycle(image: string, parents: ReadonlyArray<string>, edges: ReadonlyMap<string, ReadonlyArray<string>>): string[] | null {
  const path: string[] = [image];
  const seen = new Set<string>();
  const visit = (name: string): boolean => {
    if (name === image) return true;
    if (seen.has(name)) return false;
    seen.add(name);
    path.push(name);
    for (const next of edges.get(name) ?? []) if (visit(next)) return true;
    path.pop();
    return false;
  };
  for (const p of parents) if (visit(p)) return [...path, image];
  return null;
}

/** The instructions the editor completes, with what each does. */
export const CONTAINERFILE_INSTRUCTIONS: ReadonlyArray<{ name: string; detail: string }> = [
  { name: "FROM", detail: "the image this one is built on" },
  { name: "RUN", detail: "run a command while building" },
  { name: "ENV", detail: "set an environment variable" },
  { name: "ARG", detail: "a build argument" },
  { name: "WORKDIR", detail: "the working directory" },
  { name: "USER", detail: "the user that runs what follows" },
  { name: "COPY", detail: "copy --from a stage or image" },
  { name: "ADD", detail: "add a remote file" },
  { name: "LABEL", detail: "image metadata" },
  { name: "SHELL", detail: "the shell RUN uses" },
  { name: "EXPOSE", detail: "a port, as documentation" },
  { name: "ENTRYPOINT", detail: "ignored: agents run their own command" },
  { name: "CMD", detail: "ignored: agents run their own command" },
  { name: "VOLUME", detail: "a mount point" },
  { name: "HEALTHCHECK", detail: "ignored by lux" },
  { name: "STOPSIGNAL", detail: "the stop signal" },
  { name: "ONBUILD", detail: "a trigger for images built on this one" },
];

/** Bases people often start from, offered after FROM. */
export const COMMON_BASES: ReadonlyArray<string> = [
  "debian:bookworm-slim",
  "debian:trixie-slim",
  "ubuntu:24.04",
  "node:24-bookworm-slim",
  "python:3.13-slim-bookworm",
  "golang:1.25-bookworm",
  "ruby:3.4-slim-bookworm",
  "eclipse-temurin:25-jdk",
  "mcr.microsoft.com/playwright:v1.55.0-noble",
];

// Go's images.Offline and images.GiveUp; tests/fixtures/images/builder.json
// holds the values, and both suites check their constants against it.
/** A builder not heard from for this long is offline. */
export const BUILDER_OFFLINE_SECONDS = 120;
/** A Run that has waited this long for its image while the builder was offline fails. */
export const BUILDER_GIVE_UP_MINUTES = 30;

/**
 * What a waiting Run, a preview or the Images page says of a builder not
 * heard from: since its last heartbeat (the same words the orchestrator
 * fails a Run with after BUILDER_GIVE_UP_MINUTES of it).
 */
export function builderOffline(lastSeenAt: string | null, format: (iso: string) => string): string {
  return lastSeenAt ? `image builder offline since ${format(lastSeenAt)}` : "image builder offline: it has never reported in";
}

/** A digest ref shortened for people: `sha256:3f9a07…`. */
export function shortDigest(ref: string | null | undefined): string {
  if (!ref) return "";
  const at = ref.lastIndexOf("@");
  const digest = at >= 0 ? ref.slice(at + 1) : ref;
  return digest.length > 19 ? `${digest.slice(0, 19)}…` : digest;
}

/**
 * The image a role names: the project's, then the organization's — for the
 * fixer, then the implementer's over the same layers — or none, and the
 * Run falls through to its project's image. An id the library lacks is
 * skipped. The orchestrator's images.RoleImage is the same rule, pinned
 * by the same table of cases in both test suites.
 */
export function resolveRoleImage(
  role: string,
  layers: { project?: AgentModels | null | undefined; organization: AgentModels | null | undefined },
  images: ReadonlyArray<{ id: string }>,
): { imageId: string | null; from: "project" | "organization" | "implementer" | "none" } {
  const chain = role === "fixer" ? ["fixer", "implementer"] : [role];
  const ordered = [["project", layers.project], ["organization", layers.organization]] as const;
  for (const r of chain) {
    for (const [name, layer] of ordered) {
      const id = (layer as Record<string, { image?: string }> | null | undefined)?.[r]?.image;
      if (id && images.some((i) => i.id === id)) return { imageId: id, from: r === role ? name : "implementer" };
    }
  }
  return { imageId: null, from: "none" };
}
