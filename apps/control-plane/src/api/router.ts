/**
 * Minimal pattern-matching router.
 *
 * Bun's built-in `routes` option would also work, but an explicit router keeps
 * route definitions testable in isolation and independent of the server.
 */

import { type Principal, authenticate } from "./auth.ts";
import { errorResponse, notFound, unauthorized } from "./http.ts";

export interface RequestContext {
  request: Request;
  url: URL;
  params: Record<string, string>;
  principal: Principal;
}

/** Context for routes that run before authentication (health, webhooks). */
export interface PublicContext {
  request: Request;
  url: URL;
  params: Record<string, string>;
}

type Handler = (ctx: RequestContext) => Promise<Response> | Response;
type PublicHandler = (ctx: PublicContext) => Promise<Response> | Response;

interface Route {
  method: string;
  segments: string[];
  handler: Handler | PublicHandler;
  public: boolean;
  /** Restricts a route to one principal kind, e.g. runner-only endpoints. */
  requireKind?: Principal["kind"] | undefined;
}

export class Router {
  readonly #routes: Route[] = [];

  #add(
    method: string,
    pattern: string,
    handler: Handler | PublicHandler,
    opts: { public?: boolean; requireKind?: Principal["kind"] } = {},
  ): this {
    this.#routes.push({
      method,
      segments: pattern.split("/").filter(Boolean),
      handler,
      public: opts.public ?? false,
      requireKind: opts.requireKind,
    });
    return this;
  }

  get(pattern: string, handler: Handler, opts?: { requireKind?: Principal["kind"] }): this {
    return this.#add("GET", pattern, handler, opts);
  }
  post(pattern: string, handler: Handler, opts?: { requireKind?: Principal["kind"] }): this {
    return this.#add("POST", pattern, handler, opts);
  }
  patch(pattern: string, handler: Handler, opts?: { requireKind?: Principal["kind"] }): this {
    return this.#add("PATCH", pattern, handler, opts);
  }
  delete(pattern: string, handler: Handler, opts?: { requireKind?: Principal["kind"] }): this {
    return this.#add("DELETE", pattern, handler, opts);
  }

  /** Register a route that runs without authentication. */
  publicRoute(method: string, pattern: string, handler: PublicHandler): this {
    return this.#add(method, pattern, handler, { public: true });
  }

  async handle(request: Request): Promise<Response> {
    const url = new URL(request.url);
    const segments = url.pathname.split("/").filter(Boolean);

    try {
      for (const route of this.#routes) {
        if (route.method !== request.method) continue;
        const params = match(route.segments, segments);
        if (!params) continue;

        if (route.public) {
          return await (route.handler as PublicHandler)({ request, url, params });
        }

        const principal = await authenticate(request.headers.get("authorization"));
        if (!principal) throw unauthorized();
        if (route.requireKind && principal.kind !== route.requireKind) {
          throw unauthorized(`this endpoint requires a ${route.requireKind} key`);
        }

        return await (route.handler as Handler)({ request, url, params, principal });
      }
      throw notFound(`no route for ${request.method} ${url.pathname}`);
    } catch (err) {
      return errorResponse(err);
    }
  }
}

/** Match concrete path segments against a pattern, extracting `:name` params. */
function match(pattern: string[], actual: string[]): Record<string, string> | null {
  if (pattern.length !== actual.length) return null;

  const params: Record<string, string> = {};
  for (let i = 0; i < pattern.length; i++) {
    const p = pattern[i];
    const a = actual[i];
    if (p === undefined || a === undefined) return null;

    if (p.startsWith(":")) {
      params[p.slice(1)] = decodeURIComponent(a);
    } else if (p !== a) {
      return null;
    }
  }
  return params;
}
