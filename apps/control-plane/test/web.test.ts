/**
 * The backend serving the built web app (DUDE_WEB_DIR): files, the SPA
 * fallback, cache headers, and what it must refuse or leave to the API.
 */

import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { mkdtemp, mkdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { buildRouter } from "../src/index.ts";
import type { Router } from "../src/api/router.ts";

const INDEX = "<!doctype html><title>dude</title>";
let parent: string;
let router: Router;

const get = (path: string, method = "GET") => router.handle(new Request(`http://dude.test${path}`, { method }));

beforeAll(async () => {
  parent = await mkdtemp(join(tmpdir(), "dude-web-"));
  const web = join(parent, "web");
  await mkdir(join(web, "assets"), { recursive: true });
  await writeFile(join(web, "index.html"), INDEX);
  await writeFile(join(web, "sw.js"), "self.addEventListener('push', () => {});");
  await writeFile(join(web, "assets", "index-abc123.js"), "console.log(1);");
  // Beside the web root, so an escape from it has something to find.
  await writeFile(join(parent, "secret.txt"), "not for the web");
  router = buildRouter(web);
});

afterAll(async () => {
  await rm(parent, { recursive: true, force: true });
});

describe("DUDE_WEB_DIR", () => {
  test("serves index.html at the root, never cached", async () => {
    const res = await get("/");
    expect(res.status).toBe(200);
    expect(res.headers.get("content-type")).toStartWith("text/html");
    expect(res.headers.get("cache-control")).toBe("no-cache");
    expect(await res.text()).toBe(INDEX);
  });

  test("falls back to index.html for an app path", async () => {
    const res = await get("/p/some-project/tasks/42");
    expect(res.status).toBe(200);
    expect(res.headers.get("cache-control")).toBe("no-cache");
    expect(await res.text()).toBe(INDEX);
  });

  test("serves hashed assets as immutable", async () => {
    const res = await get("/assets/index-abc123.js");
    expect(res.status).toBe(200);
    expect(res.headers.get("content-type")).toStartWith("text/javascript");
    expect(res.headers.get("cache-control")).toBe("public, max-age=31536000, immutable");
    expect(await res.text()).toBe("console.log(1);");
  });

  test("revalidates unhashed files such as the service worker", async () => {
    const res = await get("/sw.js");
    expect(res.status).toBe(200);
    expect(res.headers.get("cache-control")).toBe("no-cache");
  });

  test("a missing hashed asset is a 404, not the page", async () => {
    const res = await get("/assets/index-gone.js");
    expect(res.status).toBe(404);
  });

  test("HEAD answers headers without a body", async () => {
    const res = await get("/", "HEAD");
    expect(res.status).toBe(200);
    expect(res.headers.get("content-length")).toBe(String(INDEX.length));
    expect(await res.text()).toBe("");
  });

  test("an unknown API path stays a JSON 404", async () => {
    for (const path of ["/v1/nothing-here", "/v1", "/health/extra"]) {
      const res = await get(path);
      expect(res.status).toBe(404);
      expect(res.headers.get("content-type")).toStartWith("application/json");
    }
  });

  test("other methods are not served", async () => {
    const res = await get("/", "POST");
    expect(res.status).toBe(404);
    expect(res.headers.get("content-type")).toStartWith("application/json");
  });

  test("refuses to leave the web directory", async () => {
    // Encoded separators survive URL parsing and are decoded only here.
    for (const path of ["/..%2fsecret.txt", "/assets/..%2f..%2fsecret.txt", "/%5c..%5csecret.txt"]) {
      const res = await get(path);
      expect(res.status).toBe(400);
      expect(await res.text()).not.toContain("not for the web");
    }
    // The URL parser folds these to /secret.txt, inside the root: the page.
    for (const path of ["/%2e%2e/secret.txt", "/a/../../secret.txt"]) {
      const res = await get(path);
      expect(await res.text()).toBe(INDEX);
    }
  });
});

describe("without DUDE_WEB_DIR", () => {
  test("an unmatched path is the API's 404", async () => {
    const plain = buildRouter("");
    for (const path of ["/", "/p/some-project", "/assets/index-abc123.js"]) {
      const res = await plain.handle(new Request(`http://dude.test${path}`));
      expect(res.status).toBe(404);
      expect(res.headers.get("content-type")).toStartWith("application/json");
    }
  });
});
