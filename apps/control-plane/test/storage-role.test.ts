import { afterAll, expect, spyOn, test } from "bun:test";
import { createHmac, createHash } from "node:crypto";

type Storage = typeof import("../src/storage.ts");

const keys = ["FAKEROLEKEYONE", "FAKEROLEKEYTWO"];
const secrets = ["fakeRoleSecretOne", "fakeRoleSecretTwo"];
const tokens = ["fakeRoleSessionOne", "fakeRoleSessionTwo"];
const requests: { method: string; url: string; headers: Headers }[] = [];
const objects = new Map<string, Uint8Array>();
// What an S3 gateway could reflect from a signed request: a key, secret and session token.
const LEAKED = "FAKELEAKEDKEY fakeLeakedSecret fakeLeakedSessionToken";
let generation = 0;
let denied = false;
let malformed = false;
let imdsToken = "fake-imds-token";
// When set, the role credential endpoint answers with exactly this body.
let credentialBody: string | undefined;
let metadataCalls = 0;
let expiry = Date.now() + 360_000;
const meta = Bun.serve({ hostname: "127.0.0.1", port: 0, fetch(req) {
  const path = new URL(req.url).pathname;
  metadataCalls++;
  if (denied) return new Response("denied", { status: 403 });
  if (path === "/latest/api/token") {
    if (req.method !== "PUT" || !req.headers.get("x-aws-ec2-metadata-token-ttl-seconds")) return new Response(null, { status: 400 });
    return new Response(imdsToken);
  }
  if (req.headers.get("x-aws-ec2-metadata-token") !== "fake-imds-token") return new Response(null, { status: 401 });
  if (path === "/latest/meta-data/iam/security-credentials/") return new Response("fake-role\n");
  if (path === "/latest/meta-data/iam/security-credentials/fake-role" && credentialBody !== undefined) return new Response(credentialBody);
  if (path === "/latest/meta-data/iam/security-credentials/fake-role") return Response.json(malformed ? { Code: "Failure" } : {
    Code: "Success", AccessKeyId: keys[generation], SecretAccessKey: secrets[generation],
    Token: tokens[generation], Expiration: new Date(expiry).toISOString(),
  });
  return new Response(null, { status: 404 });
}});
const s3 = Bun.serve({ hostname: "127.0.0.1", port: 0, async fetch(req) {
  requests.push({ method: req.method, url: req.url, headers: new Headers(req.headers) });
  const key = new URL(req.url).pathname;
  if (key.includes("leak-plain")) return new Response(LEAKED, { status: 403 });
  if (key.includes("leak-code")) {
    return new Response(`<?xml version="1.0"?><Error><Code>FAKECODESECRET123</Code><Message>denied</Message></Error>`, { status: 403 });
  }
  if (key.includes("leak-xml")) {
    return new Response(`<?xml version="1.0"?><Error><Code>AccessDenied</Code><Message>${LEAKED}</Message></Error>`, { status: 403 });
  }
  if (req.method === "PUT") { objects.set(key, new Uint8Array(await req.arrayBuffer())); return new Response(null, { status: 200 }); }
  if (req.method === "DELETE") { objects.delete(key); return new Response(null, { status: 204 }); }
  const bytes = objects.get(key);
  return bytes ? new Response(bytes) : new Response("missing", { status: 404 });
}});

// Counts S3Client constructions; storage.ts resolves `S3Client` from Bun at construction time.
const RealS3Client = Bun.S3Client;
let clientsBuilt = 0;
(Bun as { S3Client: unknown }).S3Client = new Proxy(RealS3Client, {
  construct(target, args) { clientsBuilt++; return Reflect.construct(target, args); },
});

for (const key of Object.keys(process.env)) if (key.startsWith("AWS_") || key.startsWith("S3_")) delete process.env[key];
process.env.AWS_EC2_METADATA_SERVICE_ENDPOINT = `http://127.0.0.1:${meta.port}`;
process.env.DUDE_S3_BUCKET = "fake-bucket";
process.env.DUDE_S3_REGION = "us-east-1";
process.env.DUDE_S3_ENDPOINT = `http://127.0.0.1:${s3.port}`;
delete process.env.DUDE_S3_ACCESS_KEY;
delete process.env.DUDE_S3_SECRET_KEY;
const { putObject, getObject, deleteObject } = await import("../src/storage.ts");
const { errorResponse } = await import("../src/api/http.ts");

// A storage module with none of the cached credentials or clients of earlier imports.
let freshCount = 0;
const fresh = (): Promise<Storage> => import(`../src/storage.ts?fresh=${++freshCount}`);

const realNow = Date.now;
function setClock(offsetMs: number) { Date.now = () => realNow() + offsetMs; }

afterAll(() => {
  Date.now = realNow;
  (Bun as { S3Client: unknown }).S3Client = RealS3Client;
  meta.stop(true);
  s3.stop(true);
});

function signed(req: { method: string; url: string; headers: Headers }, generation: number) {
  signedWith(req, keys[generation]!, secrets[generation]!, tokens[generation]!);
}

// Recomputes the SigV4 signature with the given secret; a null session token must be absent.
function signedWith(req: { method: string; url: string; headers: Headers }, key: string, secret: string, session: string | null) {
  const auth = req.headers.get("authorization") ?? "";
  expect(auth).toContain(`Credential=${key}/`);
  expect(req.headers.get("x-amz-security-token")).toBe(session);
  const match = auth.match(/Credential=([^, ]+), SignedHeaders=([^, ]+), Signature=([0-9a-f]+)/);
  expect(match).not.toBeNull();
  const url = new URL(req.url);
  const headers = match![2]!.split(";");
  const canonical = `${req.method}\n${url.pathname}\n${url.searchParams.toString()}\n${headers.map((h) => `${h}:${req.headers.get(h)?.trim()}\n`).join("")}\n${match![2]}\n${req.headers.get("x-amz-content-sha256")}`;
  const scope = match![1]!.slice(key.length + 1);
  const hash = (v: string) => createHash("sha256").update(v).digest("hex");
  const hmac = (key: Buffer | string, value: string) => createHmac("sha256", key).update(value).digest();
  const [date, region, service] = scope.split("/");
  const signature = createHmac("sha256", hmac(hmac(hmac(hmac(`AWS4${secret}`, date!), region!), service!), "aws4_request"))
    .update(`AWS4-HMAC-SHA256\n${req.headers.get("x-amz-date")}\n${scope}\n${hash(canonical)}`).digest("hex");
  expect(match![3]).toBe(signature);
}

test("IMDSv2 credentials sign put, read, delete and refresh before expiry", async () => {
  const bytes = new Uint8Array([1, 2, 3]);
  await putObject("image", bytes, "image/png");
  expect(new Uint8Array((await getObject("image"))!)).toEqual(bytes);
  await deleteObject("image");
  expect(objects.size).toBe(0);
  expect(metadataCalls).toBe(3);
  for (const request of requests) signed(request, 0);
  requests.length = 0;
  generation = 1;
  const now = Date.now;
  Date.now = () => now() + 70_000;
  try {
    expiry = Date.now() + 3_600_000;
    await putObject("new-image", bytes, "image/png");
  } finally { Date.now = now; }
  expect(metadataCalls).toBe(6);
  signed(requests[0]!, 1);
});

test("denied and malformed refresh fail closed without S3 requests", async () => {
  const count = requests.length;
  denied = true;
  const now = Date.now;
  // About 40 s before expiry: inside the safety margin, so the cached credential is not used.
  Date.now = () => now() + 3_630_000;
  try {
    await expect(putObject("denied", new Uint8Array([1]), "image/png")).rejects.toThrow();
    denied = false;
    malformed = true;
    const calls = metadataCalls;
    // Past the 5 s backoff of the denied attempt.
    Date.now = () => now() + 3_636_000;
    await expect(getObject("malformed")).rejects.toThrow();
    expect(metadataCalls).toBe(calls + 3);
  } finally {
    Date.now = now;
    denied = false;
    malformed = false;
  }
  expect(requests.length).toBe(count);
});

test("an unchanged near-expiry credential is rechecked on a throttle and keeps one client", async () => {
  const storage = await fresh();
  generation = 0;
  expiry = Date.now() + 240_000;
  metadataCalls = 0;
  requests.length = 0;
  clientsBuilt = 0;
  try {
    // One photo request per second for a minute, all inside the refresh window.
    for (let second = 0; second < 60; second++) {
      setClock(second * 1000);
      await storage.putObject(`flood-${second}`, new Uint8Array([1]), "image/png");
    }
  } finally { Date.now = realNow; }
  // The initial load plus one recheck at 30 s: three metadata calls each.
  expect(metadataCalls).toBe(6);
  expect(clientsBuilt).toBe(1);
  expect(requests.length).toBe(60);
  for (const request of requests) signed(request, 0);
});

test("an IMDS outage backs off, serves while the cached credential is valid, then fails closed", async () => {
  const storage = await fresh();
  generation = 0;
  expiry = Date.now() + 240_000;
  await storage.putObject("outage-start", new Uint8Array([1]), "image/png");
  metadataCalls = 0;
  requests.length = 0;
  denied = true;
  const outcomes: boolean[] = [];
  try {
    // One request per second from the start of the outage until past expiry.
    for (let second = 0; second < 240; second++) {
      setClock(second * 1000);
      outcomes.push(await storage.putObject(`outage-${second}`, new Uint8Array([1]), "image/png").then(() => true, () => false));
    }
  } finally {
    Date.now = realNow;
    denied = false;
  }
  // Credential expires at 240 s; the 60 s margin ends usable service at 180 s.
  expect(outcomes.slice(0, 180).every(Boolean)).toBe(true);
  expect(outcomes.slice(180).some(Boolean)).toBe(false);
  expect(requests.length).toBe(180);
  for (const request of requests) signed(request, 0);
  // Attempts at 30, 35, 45, 65, 105, 165, 225 s; a denied token PUT is one call each.
  expect(metadataCalls).toBe(7);
});

// Everything console.error would print for these operations, rendered as the console renders it.
async function logged(run: () => Promise<void>): Promise<string> {
  const spy = spyOn(console, "error").mockImplementation(() => {});
  try {
    await run();
    return spy.mock.calls.map((args) => args.map((arg) => (typeof arg === "string" ? arg : Bun.inspect(arg))).join(" ")).join("\n");
  } finally { spy.mockRestore(); }
}

test("S3 error bodies holding credentials never reach the logs", async () => {
  const storage = await fresh();
  generation = 0;
  expiry = Date.now() + 3_600_000;
  const statuses: number[] = [];
  const output = await logged(async () => {
    for (const key of ["leak-plain", "leak-xml"]) {
      for (const run of [() => storage.putObject(key, new Uint8Array([1]), "image/png"), () => storage.getObject(key)]) {
        const error = await run().then(() => undefined, (err: unknown) => err);
        expect(error).toBeInstanceOf(storage.StorageError);
        statuses.push(errorResponse(error).status);
      }
      await storage.deleteObject(key);
    }
  });
  expect(statuses).toEqual([500, 500, 500, 500]);
  expect(output).toContain("photo storage put failed (AccessDenied)");
  expect(output).toContain("photo storage delete failed (AccessDenied)");
  for (const secret of LEAKED.split(" ")) expect(output).not.toContain(secret);
});

test("an S3 error code outside the known set, and the object key, never reach the logs", async () => {
  const storage = await fresh();
  generation = 0;
  expiry = Date.now() + 3_600_000;
  const key = "org/person/leak-code-SECRETIMAGETOKEN.png";
  const output = await logged(async () => {
    const error = await storage.putObject(key, new Uint8Array([1]), "image/png").then(() => undefined, (err: unknown) => err);
    expect(error).toBeInstanceOf(storage.StorageError);
    expect((error as { code?: string }).code).toBeUndefined();
    errorResponse(error);
    await storage.deleteObject(key);
  });
  expect(output).toContain("photo storage put failed");
  expect(output).toContain("could not delete an object");
  expect(output).not.toContain("FAKECODESECRET123");
  expect(output).not.toContain("SECRETIMAGETOKEN");
});

test("an IMDS token with CR/LF is rejected without its value reaching the logs", async () => {
  const storage = await fresh();
  imdsToken = "FAKE_IMDS_TOKEN_VALUE\r\nX-Injected: fakeInjectedValue";
  metadataCalls = 0;
  requests.length = 0;
  let error: unknown;
  const output = await logged(async () => {
    error = await storage.putObject("token", new Uint8Array([1]), "image/png").then(() => undefined, (err: unknown) => err);
    errorResponse(error);
    await storage.deleteObject("token");
  }).finally(() => { imdsToken = "fake-imds-token"; });
  expect((error as { code?: string }).code).toBe("MetadataTokenInvalid");
  // Only the token PUT: the token is never sent on a role or credential request.
  expect(metadataCalls).toBe(1);
  expect(requests.length).toBe(0);
  expect(output).toContain("photo storage put failed (MetadataTokenInvalid)");
  expect(output).not.toContain("FAKE_IMDS_TOKEN_VALUE");
  expect(output).not.toContain("fakeInjectedValue");
});

// Runs with the given DUDE_S3_*KEY values and IMDS denying everything, then restores both.
async function withExplicitKeys(access: string | undefined, secret: string | undefined, run: () => Promise<void>) {
  const set = (name: string, value: string | undefined) => { if (value === undefined) delete process.env[name]; else process.env[name] = value; };
  set("DUDE_S3_ACCESS_KEY", access);
  set("DUDE_S3_SECRET_KEY", secret);
  denied = true;
  try { await run(); } finally {
    delete process.env.DUDE_S3_ACCESS_KEY;
    delete process.env.DUDE_S3_SECRET_KEY;
    denied = false;
  }
}

test("explicit DUDE_S3 keys sign put, read and delete without any metadata request", async () => {
  const storage = await fresh();
  metadataCalls = 0;
  requests.length = 0;
  objects.clear();
  const bytes = new Uint8Array([4, 5, 6]);
  await withExplicitKeys("FAKEEXPLICITKEY", "fakeExplicitSecret", async () => {
    await storage.putObject("explicit", bytes, "image/png");
    expect(new Uint8Array((await storage.getObject("explicit"))!)).toEqual(bytes);
    const output = await logged(() => storage.deleteObject("explicit"));
    expect(output).toBe("");
  });
  expect(objects.size).toBe(0);
  expect(metadataCalls).toBe(0);
  expect(requests.map((r) => r.method)).toEqual(["PUT", "GET", "DELETE"]);
  for (const request of requests) signedWith(request, "FAKEEXPLICITKEY", "fakeExplicitSecret", null);
});

test("one explicit DUDE_S3 key alone fails without falling back to IMDS", async () => {
  metadataCalls = 0;
  requests.length = 0;
  for (const [access, secret] of [["FAKEEXPLICITKEY", undefined], [undefined, "fakeExplicitSecret"]] as const) {
    const storage = await fresh();
    await withExplicitKeys(access, secret, async () => {
      // Metadata would answer here, so a fallback would reach S3.
      denied = false;
      const error = await storage.putObject("one-key", new Uint8Array([1]), "image/png").then(() => undefined, (err: unknown) => err);
      expect(error).toBeInstanceOf(storage.StorageError);
      expect((error as { code?: string }).code).toBe("CredentialsIncomplete");
      await expect(storage.getObject("one-key")).rejects.toThrow("CredentialsIncomplete");
    });
  }
  expect(metadataCalls).toBe(0);
  expect(requests.length).toBe(0);
});

test("a first request fails with no S3 request when metadata denies or returns unusable credentials", async () => {
  const credential = { Code: "Success", AccessKeyId: keys[0], SecretAccessKey: secrets[0], Token: tokens[0] };
  const cases: [string, string, () => void][] = [
    ["denied", "MetadataRequestFailed", () => { denied = true; }],
    ["malformed JSON", "MetadataCredentialsInvalid", () => { credentialBody = "{not json"; }],
    ["failure code", "MetadataCredentialsInvalid", () => { credentialBody = JSON.stringify({ ...credential, Code: "Failure", Expiration: new Date(Date.now() + 3_600_000).toISOString() }); }],
    ["empty body", "MetadataCredentialsInvalid", () => { credentialBody = ""; }],
    ["empty credentials", "MetadataCredentialsInvalid", () => { credentialBody = JSON.stringify({ Code: "Success", AccessKeyId: "", SecretAccessKey: "", Token: "", Expiration: new Date(Date.now() + 3_600_000).toISOString() }); }],
    ["expired", "MetadataCredentialsInvalid", () => { credentialBody = JSON.stringify({ ...credential, Expiration: new Date(Date.now() - 1_000).toISOString() }); }],
    ["inside the safety margin", "CredentialsUnavailable", () => { credentialBody = JSON.stringify({ ...credential, Expiration: new Date(Date.now() + 30_000).toISOString() }); }],
  ];
  requests.length = 0;
  const operations = [
    (storage: Storage) => storage.putObject("first", new Uint8Array([1]), "image/png"),
    (storage: Storage) => storage.getObject("first"),
  ];
  for (const [name, code, arrange] of cases) {
    for (const run of operations) {
      const storage = await fresh();
      metadataCalls = 0;
      arrange();
      try {
        const error = await run(storage).then(() => undefined, (err: unknown) => err);
        expect({ name, error: error instanceof storage.StorageError, code: (error as { code?: string }).code }).toEqual({ name, error: true, code });
        // Inside the failure backoff the next request fails without asking metadata again.
        const calls = metadataCalls;
        await expect(run(storage)).rejects.toThrow("CredentialsUnavailable");
        expect(metadataCalls).toBe(calls);
      } finally {
        denied = false;
        credentialBody = undefined;
      }
      expect({ name, metadataCalls: metadataCalls > 0 }).toEqual({ name, metadataCalls: true });
    }
  }
  expect(requests.length).toBe(0);
});
