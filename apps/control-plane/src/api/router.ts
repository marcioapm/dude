/**
 * Minimal pattern-matching router.
 *
 * Bun's built-in `routes` option would also work, but an explicit router keeps
 * route definitions testable in isolation and independent of the server.
 */

import { type Principal, authenticate, personPrincipal } from "./auth.ts";
import { touch } from "./presence.ts";
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

/** Per-route authentication options. */
export interface RouteOptions {
  allowKeyInQuery?: boolean;
}
type PublicHandler = (ctx: PublicContext) => Promise<Response> | Response;

interface Route {
  method: string;
  segments: string[];
  handler: Handler | PublicHandler;
  public: boolean;
  /**
   * Accept the API key as a `key` query parameter.
   *
   * Only for endpoints a browser must reach with `EventSource`, which cannot
   * set headers. Keys in URLs can leak into logs and proxies, so this is
   * opt-in per route and limited to read-only streams.
   */
  allowKeyInQuery?: boolean | undefined;
}

/** Answers a request no route matched, or returns null to leave it a 404. */
export type Fallback = (request: Request, url: URL) => Promise<Response | null> | Response | null;

export class Router {
  readonly #routes: Route[] = [];
  #fallback: Fallback | null = null;

  constructor(private readonly authenticateRequest: (credential: string | null) => Promise<Principal | null> = authenticate) {}

  #add(
    method: string,
    pattern: string,
    handler: Handler | PublicHandler,
    opts: RouteOptions & { public?: boolean } = {},
  ): this {
    this.#routes.push({
      method,
      segments: pattern.split("/").filter(Boolean),
      handler,
      public: opts.public ?? false,
      allowKeyInQuery: opts.allowKeyInQuery,
    });
    return this;
  }

  get(pattern: string, handler: Handler, opts?: RouteOptions): this {
    return this.#add("GET", pattern, handler, opts);
  }
  post(pattern: string, handler: Handler, opts?: RouteOptions): this {
    return this.#add("POST", pattern, handler, opts);
  }
  patch(pattern: string, handler: Handler, opts?: RouteOptions): this {
    return this.#add("PATCH", pattern, handler, opts);
  }
  put(pattern: string, handler: Handler, opts?: RouteOptions): this {
    return this.#add("PUT", pattern, handler, opts);
  }
  delete(pattern: string, handler: Handler, opts?: RouteOptions): this {
    return this.#add("DELETE", pattern, handler, opts);
  }

  /** Register a route that runs without authentication. */
  publicRoute(method: string, pattern: string, handler: PublicHandler): this {
    return this.#add(method, pattern, handler, { public: true });
  }

  /** Runs, unauthenticated, only when no route matched. */
  fallback(handler: Fallback): this {
    this.#fallback = handler;
    return this;
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

        /*
         * Credentials come from the Authorization header, except on routes
         * that opt into a query parameter.
         *
         * `EventSource` cannot set headers, so an SSE stream has no other way
         * to authenticate from a browser. That is a real trade-off — a key in
         * a URL can reach access logs, proxies and referrers — so it is
         * enabled per route rather than globally, and only for the read-only
         * stream endpoint.
         */
        let principal = await this.authenticateRequest(
          request.headers.get("authorization") ??
            (route.allowKeyInQuery ? url.searchParams.get("key") : null),
        );
        if (principal?.credentialKind === "person") {
          principal = await personPrincipal(principal.organizationId, principal.personId);
        }
        if (!principal) throw unauthorized();
        await touch(principal, request.headers.get("x-dude-where"));

        return await (route.handler as Handler)({ request, url, params, principal });
      }
      const fallback = await this.#fallback?.(request, url);
      if (fallback) return fallback;
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
