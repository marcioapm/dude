/**
 * HTTP helpers: typed responses, error mapping, body validation.
 */

import type { ZodType } from "zod";

export class HttpError extends Error {
  constructor(
    readonly status: number,
    override readonly message: string,
    readonly code: string = "error",
    readonly details?: unknown,
  ) {
    super(message);
    this.name = "HttpError";
  }
}

export const badRequest = (message: string, details?: unknown) =>
  new HttpError(400, message, "bad_request", details);
export const unauthorized = (message = "missing or invalid credentials") =>
  new HttpError(401, message, "unauthorized");
export const forbidden = (message = "not permitted") => new HttpError(403, message, "forbidden");
export const notFound = (message = "not found") => new HttpError(404, message, "not_found");
export const conflict = (message: string) => new HttpError(409, message, "conflict");

const JSON_HEADERS = { "content-type": "application/json; charset=utf-8" } as const;

export function json(body: unknown, status = 200, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), { status, headers: { ...JSON_HEADERS, ...headers } });
}

export function noContent(): Response {
  return new Response(null, { status: 204 });
}

/** Convert any thrown value into a client-safe response. */
export function errorResponse(err: unknown): Response {
  if (err instanceof HttpError) {
    return json(
      { error: { code: err.code, message: err.message, ...(err.details ? { details: err.details } : {}) } },
      err.status,
    );
  }
  // Unexpected failures must not leak internals to the client, but must be
  // visible to the operator.
  console.error("unhandled error:", err);
  return json({ error: { code: "internal", message: "internal server error" } }, 500);
}

/**
 * Parse and validate a JSON body, raising a 400 with field details.
 *
 * An absent body is treated as `{}` rather than as malformed. Several
 * endpoints — pause, resume, abort — take only optional fields, and
 * `POST`ing them with nothing should mean "use the defaults", not "your
 * request was invalid". The schema still decides whether empty is acceptable.
 */
export async function parseBody<T>(request: Request, schema: ZodType<T>): Promise<T> {
  const raw = await request.text();

  let payload: unknown = {};
  if (raw.trim() !== "") {
    try {
      payload = JSON.parse(raw);
    } catch {
      throw badRequest("request body must be valid JSON");
    }
  }

  const result = schema.safeParse(payload);
  if (!result.success) {
    throw badRequest("request body failed validation", result.error.flatten());
  }
  return result.data;
}

/** Read a bounded integer query parameter. */
export function intParam(
  url: URL,
  name: string,
  opts: { min?: number; max?: number } = {},
): number | undefined {
  const value = url.searchParams.get(name);
  if (value === null || value === "") return undefined;

  const parsed = Number(value);
  if (!Number.isInteger(parsed)) throw badRequest(`${name} must be an integer`);
  if (opts.min !== undefined && parsed < opts.min) throw badRequest(`${name} must be >= ${opts.min}`);
  if (opts.max !== undefined && parsed > opts.max) throw badRequest(`${name} must be <= ${opts.max}`);
  return parsed;
}
