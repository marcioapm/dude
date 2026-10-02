import { describe, expect, test } from "bun:test";
import {
  BUILDER_GIVE_UP_MINUTES,
  BUILDER_OFFLINE_SECONDS,
  buildArgsSchema,
  firstFrom,
  imageCycle,
  imageDraftSchema,
  imageNameSchema,
  imageReferences,
  lintContainerfile,
  resolveRoleImage,
  shortDigest,
  type LintContext,
} from "../src/images.ts";

const LIB: LintContext = { images: ["acme-base", "node-pnpm"] };
const lint = (text: string, ctx = LIB) => lintContainerfile(text, ctx);
const messages = (text: string, ctx = LIB) => lint(text, ctx).map((d) => `${d.severity} ${d.line}: ${d.message}`);
const DIGEST = "sha256:" + "a".repeat(64);

describe("Containerfile lint", () => {
  test("a Containerfile FROM a library image, with RUN and ENV, is clean", () => {
    expect(lint("# comment\nFROM image:acme-base\nRUN apt-get update \\\n  && apt-get install -y git\nENV A=1\n")).toEqual([]);
  });

  test("COPY or ADD from the build context is an error naming why", () => {
    const d = lint("FROM image:acme-base\nCOPY package.json /app/\n  ADD src /src\n");
    expect(d.map((x) => [x.severity, x.line])).toEqual([["error", 2], ["error", 3]]);
    expect(d[0]!.message).toBe("An image has no build files: COPY only --from a stage or another image");
    // The mark covers the instruction, from its first non-space column.
    expect(d[1]).toMatchObject({ from: 2, to: "  ADD src /src".length });
  });

  test("COPY --from a stage, an outside image or a library image is allowed", () => {
    const text = `FROM golang:1.25@${DIGEST} AS build\nRUN go build\nFROM image:acme-base\nCOPY --from=build /out /usr/local/bin/\nCOPY --from=image:node-pnpm /usr/local/bin/pnpm /usr/local/bin/\nCOPY --from=docker.io/library/busybox:1 /bin/busybox /bin/\n`;
    expect(messages(text)).toEqual([]);
  });

  test("ADD of a URL needs no build files; a heredoc COPY neither", () => {
    expect(messages("FROM image:acme-base\nADD https://example.com/x.tgz /tmp/\nCOPY <<EOF /etc/motd\nhi\nEOF\n")).toEqual([]);
  });

  test("COPY --from an image the library lacks is an error", () => {
    expect(messages("FROM image:acme-base\nCOPY --from=image:nope /a /b\n")).toEqual(["error 2: There is no image named nope in the library"]);
  });

  test("FROM named by a build argument is an error, in FROM or --from", () => {
    expect(messages("ARG BASE=debian\nFROM ${BASE}\n")).toEqual([
      "error 2: FROM can't be named by a build argument: dude must know what an image is built on when it is saved",
    ]);
    expect(messages("FROM image:acme-base\nCOPY --from=$X /a /b\n")).toEqual(["error 2: --from can't be named by a build argument"]);
  });

  test("FROM image:<unknown>, or FROM itself, is an error", () => {
    expect(messages("FROM image:nope\n")).toEqual(["error 1: There is no image named nope in the library"]);
    expect(messages("FROM image:node-pnpm\n", { ...LIB, self: "node-pnpm" })).toEqual(["error 1: node-pnpm can't be built FROM itself"]);
    expect(messages("FROM image:\n")).toEqual(["error 1: image: names one of the library's images"]);
  });

  test("a registry tag with no digest is a warning; a digest, scratch or a stage is not", () => {
    const d = lint("FROM debian:bookworm-slim\n");
    expect(d).toEqual([
      { severity: "warning", line: 1, from: 5, to: 25, message: "dude records its digest when this version builds; it won't follow the tag later" },
    ]);
    expect(lint(`FROM debian@${DIGEST}\n`)).toEqual([]);
    expect(lint(`FROM debian:12@${DIGEST}\n`)).toEqual([]);
    expect(lint("FROM scratch\n")).toEqual([]);
    expect(messages(`FROM debian@${DIGEST} AS base\nFROM base\n`)).toEqual([]);
  });

  test("FROM --platform is read past", () => {
    expect(messages("FROM --platform=linux/arm64 image:acme-base\n")).toEqual([]);
  });

  test("no FROM at all is an error", () => {
    expect(messages("RUN true\n")).toEqual(["error 1: A Containerfile starts FROM an image"]);
    expect(messages("")).toEqual(["error 1: A Containerfile starts FROM an image"]);
  });

  test("a continuation joins lines; a comment inside it is skipped", () => {
    expect(messages("FROM image:acme-base\nRUN a \\\n# note\n  && b\nCOPY x /y\n")).toEqual([
      "error 5: An image has no build files: COPY only --from a stage or another image",
    ]);
  });

  test("an escape directive changes the continuation character", () => {
    expect(messages("# escape=`\nFROM image:acme-base\nRUN a `\n  COPY x y\n")).toEqual([]);
  });
});

describe("what a Containerfile names", () => {
  test("library images, once each, in order", () => {
    expect(imageReferences("FROM image:acme-base AS a\nCOPY --from=image:node-pnpm /x /y\nFROM image:acme-base\n")).toEqual([
      "acme-base",
      "node-pnpm",
    ]);
    expect(imageReferences("FROM debian:bookworm-slim\n")).toEqual([]);
  });

  test("its first FROM, as written", () => {
    expect(firstFrom("# x\nFROM --platform=linux/arm64 debian:bookworm-slim AS a\nFROM image:b\n")).toBe("debian:bookworm-slim");
    expect(firstFrom("RUN x")).toBeNull();
  });
});

describe("cycles", () => {
  const edges = new Map([
    ["storybook", ["node-pnpm"]],
    ["node-pnpm", ["acme-base"]],
    ["acme-base", []],
  ]);

  test("building a base FROM its own descendant is a loop, named", () => {
    expect(imageCycle("acme-base", ["storybook"], edges)).toEqual(["acme-base", "storybook", "node-pnpm", "acme-base"]);
  });

  test("a new edge that makes no loop is none", () => {
    expect(imageCycle("storybook", ["acme-base"], edges)).toBeNull();
    expect(imageCycle("python-uv", ["acme-base", "node-pnpm"], edges)).toBeNull();
  });
});

describe("inputs", () => {
  test("a name is a slug", () => {
    expect(imageNameSchema.safeParse("node-pnpm").success).toBe(true);
    expect(imageNameSchema.safeParse("9lives").success).toBe(true);
    for (const bad of ["Node", "-x", "a_b", "", "a".repeat(64)]) expect(imageNameSchema.safeParse(bad).success).toBe(false);
    expect(imageNameSchema.safeParse("a".repeat(63)).success).toBe(true);
  });

  test("build args: at most 50, names like variables", () => {
    const fifty = Object.fromEntries(Array.from({ length: 50 }, (_, i) => [`A${i}`, "v"]));
    expect(buildArgsSchema.safeParse(fifty).success).toBe(true);
    expect(buildArgsSchema.safeParse({ ...fifty, B: "x" }).success).toBe(false);
    expect(buildArgsSchema.safeParse({ "1A": "x" }).success).toBe(false);
  });

  test("a Containerfile is at most 64 KiB, counted in bytes", () => {
    expect(imageDraftSchema.safeParse({ containerfile: "x".repeat(65536) }).success).toBe(true);
    expect(imageDraftSchema.safeParse({ containerfile: "x".repeat(65537) }).success).toBe(false);
    // 2 bytes each in UTF-8.
    expect(imageDraftSchema.safeParse({ containerfile: "é".repeat(32769) }).success).toBe(false);
  });

  test("a digest is shortened for people", () => {
    expect(shortDigest(`r.example/dude/custom@${DIGEST}`)).toBe("sha256:aaaaaaaaaaaa…");
    expect(shortDigest(null)).toBe("");
  });
});

// The same cases as orchestrator/internal/images resolve_test.go's
// RoleImage table: the backend shows what the orchestrator runs.
describe("resolveRoleImage", () => {
  const known = ["img_p", "img_o", "img_f", "img_i", "img_of", "img_oi"].map((id) => ({ id }));
  const cases: Array<[string, string, unknown, unknown, string | null, ReturnType<typeof resolveRoleImage>["from"]]> = [
    ["the project's beats the organization's", "reviewer", { reviewer: { image: "img_p" } }, { reviewer: { image: "img_o" } }, "img_p", "project"],
    ["the organization's when the project names none", "reviewer", { reviewer: { model: "m" } }, { reviewer: { image: "img_o" } }, "img_o", "organization"],
    ["the fixer's own beats the implementer's", "fixer", { fixer: { image: "img_f" }, implementer: { image: "img_i" } }, {}, "img_f", "project"],
    ["the fixer's own on the organization beats the project's implementer", "fixer", { implementer: { image: "img_i" } }, { fixer: { image: "img_of" } }, "img_of", "organization"],
    ["the fixer follows the project's implementer", "fixer", { implementer: { image: "img_i" } }, { implementer: { image: "img_oi" } }, "img_i", "implementer"],
    ["the fixer follows the organization's implementer", "fixer", {}, { implementer: { image: "img_oi" } }, "img_oi", "implementer"],
    ["only the fixer follows the implementer", "reviewer", { implementer: { image: "img_i" } }, {}, null, "none"],
    ["an image that is gone is skipped", "reviewer", { reviewer: { image: "img_gone" } }, { reviewer: { image: "img_o" } }, "img_o", "organization"],
    ["no project layer falls through to the organization", "reviewer", null, { reviewer: { image: "img_o" } }, "img_o", "organization"],
    ["none anywhere", "implementer", {}, null, null, "none"],
  ];
  for (const [name, role, project, organization, imageId, from] of cases) {
    test(name, () => {
      expect(resolveRoleImage(role, { project: project as never, organization: organization as never }, known)).toEqual({ imageId, from });
    });
  }
});

test("the builder's liveness limits are the ones the orchestrator uses (tests/fixtures/images/builder.json)", async () => {
  const shared = await Bun.file(`${import.meta.dir}/../../../tests/fixtures/images/builder.json`).json();
  expect({ offlineSeconds: BUILDER_OFFLINE_SECONDS, giveUpMinutes: BUILDER_GIVE_UP_MINUTES }).toEqual(shared);
});
