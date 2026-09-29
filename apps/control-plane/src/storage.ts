/**
 * Object storage: the S3 bucket people's and projects' photos live in.
 *
 * The database keeps only an object's key; the bytes are here. Any
 * S3-compatible store works (AWS, MinIO, versitygw); a custom endpoint is
 * addressed path-style. Unconfigured, uploads answer 503 and nothing else
 * changes: faces fall back to initials.
 */

import { S3Client } from "bun";
import { HttpError } from "./api/http.ts";

type Credentials = { accessKeyId: string; secretAccessKey: string; sessionToken: string; expires: number };
let client: S3Client | null | undefined;
let roleCredentials: Credentials | undefined;
let roleS3: S3Client | undefined;
let refresh: Promise<Credentials> | undefined;

type Operation = "put" | "get" | "delete";

/**
 * The only error storage lets out besides HttpError. Its fields are the
 * operation, the metadata HTTP status and an S3 or metadata error code: raw
 * S3 and metadata errors can carry response bodies, header values or URLs,
 * and those can hold credentials.
 */
export class StorageError extends Error {
  constructor(
    readonly operation: Operation,
    readonly status?: number,
    readonly code?: string,
  ) {
    super(`photo storage ${operation} failed${status ? ` (HTTP ${status})` : ""}${code ? ` (${code})` : ""}`);
    this.name = "StorageError";
  }
}

// A failure inside storage, with a fixed reason and the metadata HTTP status only.
class Failure extends Error {
  constructor(readonly reason: string, readonly status?: number) {
    super(reason);
  }
}

const S3_CODE = /^[A-Za-z0-9.]{1,64}$/;

function safeError(operation: Operation, error: unknown): Error {
  if (error instanceof HttpError) return error;
  if (error instanceof Failure) return new StorageError(operation, error.status, error.reason);
  const code = (error as { code?: unknown } | null)?.code;
  if ((error as { name?: unknown } | null)?.name === "S3Error" && typeof code === "string" && S3_CODE.test(code)) {
    return new StorageError(operation, undefined, code);
  }
  return new StorageError(operation);
}

// Visible ASCII only: no CR, LF or other characters invalid in a header value.
const IMDS_TOKEN = /^[\x21-\x7e]{1,1024}$/;

async function metadata(path: string, token?: string, method = "GET"): Promise<Response> {
  const endpoint = process.env.AWS_EC2_METADATA_SERVICE_ENDPOINT || "http://169.254.169.254";
  for (let attempt = 0; attempt < 2; attempt++) {
    let response: Response;
    try {
      response = await fetch(`${endpoint}${path}`, {
        method,
        headers: token ? { "X-aws-ec2-metadata-token": token } : { "X-aws-ec2-metadata-token-ttl-seconds": "21600" },
        signal: AbortSignal.timeout(1500),
      });
    } catch {
      if (attempt === 1) throw new Failure("MetadataUnreachable");
      continue;
    }
    if (response.ok) return response;
    if (response.status < 500 || attempt === 1) throw new Failure("MetadataRequestFailed", response.status);
  }
  throw new Failure("MetadataUnreachable");
}

async function metadataText(response: Response): Promise<string> {
  try { return await response.text(); } catch { throw new Failure("MetadataUnreachable"); }
}

async function loadRoleCredentials(): Promise<Credentials> {
  // IMDSv2 requires a token before either role or credential endpoint is accessible.
  const token = await metadataText(await metadata("/latest/api/token", undefined, "PUT"));
  if (!IMDS_TOKEN.test(token)) throw new Failure("MetadataTokenInvalid");
  const role = (await metadataText(await metadata("/latest/meta-data/iam/security-credentials/", token))).trim();
  if (!role || role.includes("/") || role.includes("\n")) throw new Failure("MetadataRoleInvalid");
  let data: unknown;
  try {
    data = JSON.parse(await metadataText(await metadata(`/latest/meta-data/iam/security-credentials/${encodeURIComponent(role)}`, token)));
  } catch (error) {
    if (error instanceof Failure) throw error;
    throw new Failure("MetadataCredentialsInvalid");
  }
  if (!data || typeof data !== "object") throw new Failure("MetadataCredentialsInvalid");
  const value = data as Record<string, unknown>;
  const expires = Date.parse(String(value.Expiration));
  if (value.Code !== "Success" || typeof value.AccessKeyId !== "string" || !value.AccessKeyId ||
      typeof value.SecretAccessKey !== "string" || !value.SecretAccessKey ||
      typeof value.Token !== "string" || !value.Token || !Number.isFinite(expires) || expires <= Date.now()) {
    throw new Failure("MetadataCredentialsInvalid");
  }
  return { accessKeyId: value.AccessKeyId, secretAccessKey: value.SecretAccessKey, sessionToken: value.Token, expires };
}

// Refresh starts this long before expiry; inside that window, IMDS is asked at
// most once per REFRESH_INTERVAL_MS, since it may keep returning the same credential.
const REFRESH_WINDOW_MS = 300_000;
const REFRESH_INTERVAL_MS = 30_000;
// A credential is not signed with inside this margin of its expiry, so a request
// in flight cannot outlive it.
const SAFETY_MARGIN_MS = 60_000;
// Failed refreshes back off 5 s, 10 s, 20 s, 40 s, then every 60 s; shared by all requests.
const FAILURE_BACKOFF_MS = 5_000;
const FAILURE_BACKOFF_MAX_MS = 60_000;
let nextRefresh = 0;
let failures = 0;

function sameCredentials(a: Credentials, b: Credentials): boolean {
  return a.accessKeyId === b.accessKeyId && a.secretAccessKey === b.secretAccessKey && a.sessionToken === b.sessionToken;
}

const usable = (credentials: Credentials | undefined, now: number) =>
  credentials !== undefined && credentials.expires - now > SAFETY_MARGIN_MS;

async function roleClient(bucket: string): Promise<S3Client> {
  const now = Date.now();
  const due = !roleCredentials || roleCredentials.expires - now < REFRESH_WINDOW_MS;
  if (due && now >= nextRefresh) {
    refresh ??= loadRoleCredentials().then((credentials) => {
      failures = 0;
      nextRefresh = Date.now() + REFRESH_INTERVAL_MS;
      if (!roleS3 || !roleCredentials || !sameCredentials(roleCredentials, credentials)) {
        roleS3 = new S3Client({
          bucket,
          region: process.env.DUDE_S3_REGION || "us-east-1",
          ...(process.env.DUDE_S3_ENDPOINT ? { endpoint: process.env.DUDE_S3_ENDPOINT } : {}),
          accessKeyId: credentials.accessKeyId,
          secretAccessKey: credentials.secretAccessKey,
          sessionToken: credentials.sessionToken,
        });
      }
      roleCredentials = credentials;
      return credentials;
    }, (error: unknown) => {
      nextRefresh = Date.now() + Math.min(FAILURE_BACKOFF_MS * 2 ** failures, FAILURE_BACKOFF_MAX_MS);
      failures++;
      throw error;
    }).finally(() => { refresh = undefined; });
    try {
      await refresh;
    } catch (error) {
      if (!usable(roleCredentials, Date.now())) throw error;
    }
  }
  if (!usable(roleCredentials, Date.now())) throw new Failure("CredentialsUnavailable");
  return roleS3!;
}

function configured(): S3Client | null {
  if (client !== undefined) return client;
  const bucket = process.env.DUDE_S3_BUCKET;
  client = bucket && process.env.DUDE_S3_ACCESS_KEY && process.env.DUDE_S3_SECRET_KEY
    ? new S3Client({
        bucket,
        region: process.env.DUDE_S3_REGION || "us-east-1",
        ...(process.env.DUDE_S3_ENDPOINT ? { endpoint: process.env.DUDE_S3_ENDPOINT } : {}),
        accessKeyId: process.env.DUDE_S3_ACCESS_KEY,
        secretAccessKey: process.env.DUDE_S3_SECRET_KEY,
      })
    : null;
  return client;
}

async function required(): Promise<S3Client> {
  const bucket = process.env.DUDE_S3_BUCKET;
  if (!bucket) throw new HttpError(503, "photo storage is not configured (DUDE_S3_BUCKET)", "storage_unconfigured");
  if (process.env.DUDE_S3_ACCESS_KEY || process.env.DUDE_S3_SECRET_KEY) {
    if (!process.env.DUDE_S3_ACCESS_KEY || !process.env.DUDE_S3_SECRET_KEY) throw new Failure("CredentialsIncomplete");
    return configured()!;
  }
  return roleClient(bucket);
}

/** Store an object; it is never rewritten: a new photo is a new key. */
export async function putObject(key: string, bytes: Uint8Array, type: string): Promise<void> {
  try {
    await (await required()).write(key, bytes, { type });
  } catch (err) {
    throw safeError("put", err);
  }
}

/** An object's bytes, or null when there is no such object. */
export async function getObject(key: string): Promise<ArrayBuffer | null> {
  try {
    return await (await required()).file(key).arrayBuffer();
  } catch (err) {
    const error = safeError("get", err);
    if (error instanceof StorageError && error.code === "NoSuchKey") return null;
    throw error;
  }
}

/** Remove an object, best effort: one left behind costs a few KB, never a wrong face. */
export async function deleteObject(key: string): Promise<void> {
  if (!process.env.DUDE_S3_BUCKET) return;
  try {
    await (await required()).delete(key);
  } catch (err) {
    console.error(`storage: could not delete ${key}: ${safeError("delete", err).message}`);
  }
}
