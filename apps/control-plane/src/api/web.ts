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
// Scripts other origins load (a preview page's overlay, apps/web/public/feedback):
// a missing one is a 404, never index.html parsed as script on someone's page.
const SCRIPT_PREFIX = "/feedback/";
// Loadable by a preview page that sends Cross-Origin-Embedder-Policy: require-corp.
const CROSS_ORIGIN = { "cross-origin-resource-policy": "cross-origin" };
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
      if (path.startsWith(SCRIPT_PREFIX)) return send(request, file, REVALIDATE, CROSS_ORIGIN);
      const hashed = path.startsWith(HASHED_PREFIX);
      return send(request, file, hashed ? IMMUTABLE : REVALIDATE);
    }
    // A missing hashed asset is a stale page asking for an old build:
    // index.html in its place would be parsed as script and fail obscurely.
    if (path.startsWith(HASHED_PREFIX) || path.startsWith(SCRIPT_PREFIX)) return null;
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

async function send(request: Request, path: string, cacheControl: string, extra: Record<string, string> = {}): Promise<Response> {
  const file = Bun.file(path);
  const { size, mtimeMs } = await stat(path);
  // HTTP dates have whole seconds; If-Modified-Since is compared at that grain.
  const modified = Math.floor(mtimeMs / 1000) * 1000;
  const etag = `W/"${size.toString(16)}-${Math.floor(mtimeMs).toString(16)}"`;
  const headers = {
    "content-type": file.type,
    "cache-control": cacheControl,
    "last-modified": new Date(modified).toUTCString(),
    etag,
    ...extra,
  };
  if (notModified(request, etag, modified)) return new Response(null, { status: 304, headers });
  if (request.method === "HEAD") {
    return new Response(null, { headers: { ...headers, "content-length": String(size) } });
  }
  return new Response(file, { headers });
}

// RFC 9110 13.1.2/13.1.3: If-None-Match (weak comparison) wins over If-Modified-Since.
function notModified(request: Request, etag: string, modified: number): boolean {
  const inm = request.headers.get("if-none-match");
  if (inm !== null) {
    const weak = (t: string) => t.trim().replace(/^W\//, "");
    return inm.split(",").some((t) => t.trim() === "*" || weak(t) === weak(etag));
  }
  const ims = request.headers.get("if-modified-since");
  if (ims === null) return false;
  const since = Date.parse(ims);
  return !Number.isNaN(since) && modified <= since;
}

function badRequest(): Response {
  return new Response("bad path\n", { status: 400, headers: { "content-type": "text/plain; charset=utf-8" } });
}
