/**
 * Serving the built web app (apps/web/dist) from the backend, so one public
 * process answers both the API and the UI on one origin (DUDE_WEB_DIR).
 *
 * Only for requests no route matched. The API's own namespaces (/v1, /health)
 * are never answered with a page: an unknown API path stays a JSON 404.
 */

import { stat } from "node:fs/promises";
import { resolve, sep } from "node:path";
import type { Fallback } from "./router.ts";

// Vite puts content-hashed files here; a changed file gets a new name.
const HASHED_PREFIX = "/assets/";
const IMMUTABLE = "public, max-age=31536000, immutable";
// index.html names the current hashes, and sw.js must be re-checked for a
// new service worker to install, so neither may be served stale.
const REVALIDATE = "no-cache";

export function webApp(dir: string): Fallback {
  const root = resolve(dir);
  const index = resolve(root, "index.html");

  return async (request, url) => {
    if (request.method !== "GET" && request.method !== "HEAD") return null;
    const path = url.pathname;
    if (isApiPath(path)) return null;

    let decoded: string;
    try {
      decoded = decodeURIComponent(path);
    } catch {
      return badRequest();
    }
    // The URL parser already folds literal "..", but an encoded "%2e%2e/"
    // or "..%2f" only becomes one here.
    if (decoded.includes("\0") || decoded.includes("\\") || decoded.split("/").includes("..")) {
      return badRequest();
    }
    const file = resolve(root, "." + decoded);
    if (file !== root && !file.startsWith(root + sep)) return badRequest();

    if (await isFile(file)) {
      const hashed = path.startsWith(HASHED_PREFIX);
      return send(request, file, hashed ? IMMUTABLE : REVALIDATE);
    }
    // A missing hashed asset is a stale page asking for an old build:
    // index.html in its place would be parsed as script and fail obscurely.
    if (path.startsWith(HASHED_PREFIX)) return null;
    return send(request, index, REVALIDATE);
  };
}

function isApiPath(path: string): boolean {
  return path === "/v1" || path.startsWith("/v1/") || path === "/health" || path.startsWith("/health/");
}

async function isFile(path: string): Promise<boolean> {
  try {
    return (await stat(path)).isFile();
  } catch {
    return false;
  }
}

function send(request: Request, path: string, cacheControl: string): Response {
  const file = Bun.file(path);
  const headers = { "content-type": file.type, "cache-control": cacheControl };
  if (request.method === "HEAD") {
    return new Response(null, { headers: { ...headers, "content-length": String(file.size) } });
  }
  return new Response(file, { headers });
}

function badRequest(): Response {
  return new Response("bad path\n", { status: 400, headers: { "content-type": "text/plain; charset=utf-8" } });
}
