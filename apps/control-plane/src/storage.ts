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
import { config } from "./config.ts";

type Credentials = { accessKeyId: string; secretAccessKey: string; sessionToken: string; expires: number };
let client: S3Client | undefined;
let roleCredentials: Credentials | undefined;
let roleS3: S3Client | undefined;
let refresh: Promise<void> | undefined;

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

// S3 error codes a log may name. An endpoint controls the code it returns,
// so only these known ones pass; any other is reported without a code.
const S3_CODES = new Set([
  "AccessDenied", "AllAccessDisabled", "ExpiredToken", "InternalError", "InvalidAccessKeyId",
  "InvalidBucketName", "InvalidToken", "NoSuchBucket", "NoSuchKey", "RequestTimeTooSkewed",
  "ServiceUnavailable", "SignatureDoesNotMatch", "SlowDown", "TokenRefreshRequired",
]);

function safeError(operation: Operation, error: unknown): Error {
  if (error instanceof HttpError) return error;
  if (error instanceof Failure) return new StorageError(operation, error.status, error.reason);
  const code = (error as { code?: unknown } | null)?.code;
  if ((error as { name?: unknown } | null)?.name === "S3Error" && typeof code === "string" && S3_CODES.has(code)) {
    return new StorageError(operation, undefined, code);
  }
  return new StorageError(operation);
}

// Bun's S3Client before 1.4.0 fails every PUT to a store that answers
// `Connection: close` (versitygw does) with ConnectionClosed, though the 200
// arrived and the object is stored.
export const MIN_BUN_FOR_S3 = "1.4.0";

// major.minor.patch, then a pre-release or build suffix (1.4.1-canary.3+abc).
const BUN_VERSION = /^(\d+)\.(\d+)\.(\d+)(-[^+]*)?(\+.*)?$/;

/** Why `version` of Bun cannot store photos, or undefined when it can. */
export function s3RuntimeProblem(version: string): string | undefined {
  const refuse = `photo storage (s3.bucket) needs Bun >= ${MIN_BUN_FOR_S3}, this is Bun ${version}: ` +
    "earlier Bun fails every upload to a store that answers Connection: close, such as versitygw";
  const have = BUN_VERSION.exec(version);
  if (!have) return refuse;
  const floor = BUN_VERSION.exec(MIN_BUN_FOR_S3)!;
  for (let i = 1; i <= 3; i++) {
    const diff = Number(have[i]) - Number(floor[i]);
    if (diff !== 0) return diff > 0 ? undefined : refuse;
  }
  // A pre-release of the floor comes before it (semver §11).
  return have[4] ? refuse : undefined;
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
          ...location(),
          accessKeyId: credentials.accessKeyId,
          secretAccessKey: credentials.secretAccessKey,
          sessionToken: credentials.sessionToken,
        });
      }
      roleCredentials = credentials;
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

function location(): { region: string; endpoint?: string } {
  const endpoint = config().string("DUDE_S3_ENDPOINT");
  return { region: config().string("DUDE_S3_REGION")!, ...(endpoint ? { endpoint } : {}) };
}

function configured(bucket: string, accessKeyId: string, secretAccessKey: string): S3Client {
  return client ??= new S3Client({
    bucket,
    ...location(),
    accessKeyId,
    secretAccessKey,
  });
}

async function required(): Promise<S3Client> {
  const bucket = config().string("DUDE_S3_BUCKET");
  if (!bucket) throw new HttpError(503, "photo storage is not configured (DUDE_S3_BUCKET)", "storage_unconfigured");
  const accessKeyId = config().string("DUDE_S3_ACCESS_KEY");
  const secretAccessKey = config().string("DUDE_S3_SECRET_KEY");
  if (accessKeyId || secretAccessKey) {
    if (!accessKeyId || !secretAccessKey) throw new Failure("CredentialsIncomplete");
    return configured(bucket, accessKeyId, secretAccessKey);
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
  if (!config().string("DUDE_S3_BUCKET")) return;
  try {
    await (await required()).delete(key);
  } catch (err) {
    // Not the key: it holds the random token that authorizes serving the image.
    console.error(`storage: could not delete an object: ${safeError("delete", err).message}`);
  }
}
