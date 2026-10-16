/**
 * The backend serving the built web app (DUDE_WEB_DIR): files, the SPA
 * fallback, cache headers, and what it must refuse or leave to the API.
 */

import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { mkdtemp, mkdir, rm, utimes, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { buildRouter } from "../src/index.ts";
import { Config, useConfig } from "../src/config.ts";
import type { Router } from "../src/api/router.ts";

const INDEX = "<!doctype html><title>dude</title>";
const OVERLAY = "console.debug('overlay');";
let parent: string;
let router: Router;

const get = (path: string, method = "GET") => router.handle(new Request(`http://dude.test${path}`, { method }));

beforeAll(async () => {
  parent = await mkdtemp(join(tmpdir(), "dude-web-"));
  const web = join(parent, "web");
  await mkdir(join(web, "assets"), { recursive: true });
  await mkdir(join(web, "feedback"), { recursive: true });
  await writeFile(join(web, "feedback", "overlay.js"), OVERLAY);
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

  test("the preview overlay answers anyone, from any origin, as a short-lived script", async () => {
    // A preview page on another origin, no session, no key.
    const res = await router.handle(new Request("http://dude.test/feedback/overlay.js", {
      headers: { origin: "https://web-abs-1-shop.preview.example", "sec-fetch-dest": "script", "sec-fetch-site": "cross-site" },
    }));
    expect(res.status).toBe(200);
    expect(res.headers.get("content-type")).toStartWith("text/javascript");
    // Unversioned URL: revalidated, so a new overlay reaches pages at once.
    expect(res.headers.get("cache-control")).toBe("no-cache");
    expect(await res.text()).toBe(OVERLAY);
  });

  test("a missing overlay script is a 404, never the page parsed as script", async () => {
    const res = await get("/feedback/other.js");
    expect(res.status).toBe(404);
  });

  test("a feedback path under another prefix is the page, not the overlay", async () => {
    const res = await get("/x/feedback/overlay.js");
    expect(res.status).toBe(200);
    expect(await res.text()).toBe(INDEX);
  });

  test("the overlay may be embedded cross-origin; the page may not", async () => {
    const overlay = await get("/feedback/overlay.js");
    expect(overlay.headers.get("cross-origin-resource-policy")).toBe("cross-origin");
    const page = await get("/");
    expect(page.headers.has("cross-origin-resource-policy")).toBe(false);
  });

  test("a revalidation with a validator is a 304 until the file changes", async () => {
    const path = join(parent, "web", "feedback", "overlay.js");
    const first = await get("/feedback/overlay.js");
    const etag = first.headers.get("etag");
    const modified = first.headers.get("last-modified");
    expect(etag).toBeTruthy();
    expect(modified).toBeTruthy();
    await first.text();
    for (const validator of [{ "if-none-match": etag! }, { "if-modified-since": modified! }]) {
      const again = await router.handle(new Request("http://dude.test/feedback/overlay.js", { headers: validator }));
      expect(again.status).toBe(304);
      expect(await again.text()).toBe("");
    }
    // index.html goes through the same path.
    const index = await get("/");
    const indexAgain = await router.handle(new Request("http://dude.test/", { headers: { "if-none-match": index.headers.get("etag")! } }));
    expect(indexAgain.status).toBe(304);

    const changed = "console.debug('overlay, changed');";
    await writeFile(path, changed);
    await utimes(path, new Date(), new Date(Date.now() + 5_000));
    try {
      const res = await router.handle(new Request("http://dude.test/feedback/overlay.js", { headers: { "if-none-match": etag! } }));
      expect(res.status).toBe(200);
      expect(await res.text()).toBe(changed);
      const byDate = await router.handle(new Request("http://dude.test/feedback/overlay.js", { headers: { "if-modified-since": modified! } }));
      expect(byDate.status).toBe(200);
      expect(await byDate.text()).toBe(changed);
    } finally {
      await writeFile(path, OVERLAY);
    }
  });

  test("serving the overlay opens no API route to the same anonymous caller", async () => {
    for (const path of ["/v1/tasks/wi_1/servers", "/v1/tasks/wi_1/feedback"]) {
      const res = await router.handle(new Request(`http://dude.test${path}`, {
        headers: { origin: "https://web-abs-1-shop.preview.example" },
      }));
      expect(res.status).toBe(401);
    }
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

  test("a registered route wins over a file the web app would serve", async () => {
    // /health is excluded from the fallback by name, so it cannot show
    // precedence; /sw.js is a real file in the web directory.
    const withRoute = buildRouter(join(parent, "web")).publicRoute("GET", "/sw.js", () =>
      Response.json({ from: "route" }),
    );
    const res = await withRoute.handle(new Request("http://dude.test/sw.js"));
    expect(res.status).toBe(200);
    expect(await res.json()).toEqual({ from: "route" });
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

describe("backend.web_dir in the configuration file", () => {
  test("is where the router serves the web app from when none is passed", async () => {
    const file = join(parent, "dude.toml");
    await writeFile(file, `[backend]\nweb_dir = ${JSON.stringify(join(parent, "web"))}\n`, { mode: 0o600 });
    useConfig(Config.load({ env: { DUDE_CONFIG: file }, defaultPath: join(parent, "absent.toml") }));
    try {
      const res = await buildRouter().handle(new Request("http://dude.test/some/page"));
      expect(await res.text()).toBe(INDEX);
    } finally {
      useConfig(null);
    }
  });
});

describe("the web app's preview overlay (apps/web/public, copied into the build)", () => {
  test("is a script that, on a page that loads it twice, says once that it loaded and does nothing else", async () => {
    const res = await buildRouter(join(import.meta.dir, "../../web/public"))
      .handle(new Request("http://dude.test/feedback/overlay.js", { headers: { origin: "https://web.preview.example" } }));
    expect(res.status).toBe(200);
    expect(res.headers.get("content-type")).toStartWith("text/javascript");
    const source = await res.text();
    expect(new TextEncoder().encode(source).length).toBeLessThan(8 << 10);
    const said: unknown[][] = [];
    const page: Record<string, unknown> = {};
    const run = new Function("window", "console", source);
    const console = { debug: (...a: unknown[]) => said.push(a), log: () => { throw new Error("log"); } };
    run(page, console);
    run(page, console);
    expect(said.length).toBe(1);
    expect(Object.keys(page)).toEqual(["__dudeOverlay"]);
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
