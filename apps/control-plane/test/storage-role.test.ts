import { afterAll, expect, test } from "bun:test";
import { createHmac, createHash } from "node:crypto";

type Storage = typeof import("../src/storage.ts");

const keys = ["FAKEROLEKEYONE", "FAKEROLEKEYTWO"];
const secrets = ["fakeRoleSecretOne", "fakeRoleSecretTwo"];
const tokens = ["fakeRoleSessionOne", "fakeRoleSessionTwo"];
const requests: { method: string; url: string; headers: Headers }[] = [];
const objects = new Map<string, Uint8Array>();
let generation = 0;
let denied = false;
let malformed = false;
let metadataCalls = 0;
let expiry = Date.now() + 360_000;
const meta = Bun.serve({ hostname: "127.0.0.1", port: 0, fetch(req) {
  const path = new URL(req.url).pathname;
  metadataCalls++;
  if (denied) return new Response("denied", { status: 403 });
  if (path === "/latest/api/token") {
    if (req.method !== "PUT" || !req.headers.get("x-aws-ec2-metadata-token-ttl-seconds")) return new Response(null, { status: 400 });
    return new Response("fake-imds-token");
  }
  if (req.headers.get("x-aws-ec2-metadata-token") !== "fake-imds-token") return new Response(null, { status: 401 });
  if (path === "/latest/meta-data/iam/security-credentials/") return new Response("fake-role\n");
  if (path === "/latest/meta-data/iam/security-credentials/fake-role") return Response.json(malformed ? { Code: "Failure" } : {
    Code: "Success", AccessKeyId: keys[generation], SecretAccessKey: secrets[generation],
    Token: tokens[generation], Expiration: new Date(expiry).toISOString(),
  });
  return new Response(null, { status: 404 });
}});
const s3 = Bun.serve({ hostname: "127.0.0.1", port: 0, async fetch(req) {
  requests.push({ method: req.method, url: req.url, headers: new Headers(req.headers) });
  const key = new URL(req.url).pathname;
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
  const auth = req.headers.get("authorization") ?? "";
  expect(auth).toContain(`Credential=${keys[generation]}/`);
  expect(req.headers.get("x-amz-security-token")).toBe(tokens[generation]!);
  const match = auth.match(/Credential=([^, ]+), SignedHeaders=([^, ]+), Signature=([0-9a-f]+)/);
  expect(match).not.toBeNull();
  const url = new URL(req.url);
  const headers = match![2]!.split(";");
  const canonical = `${req.method}\n${url.pathname}\n${url.searchParams.toString()}\n${headers.map((h) => `${h}:${req.headers.get(h)?.trim()}\n`).join("")}\n${match![2]}\n${req.headers.get("x-amz-content-sha256")}`;
  const scope = match![1]!.slice(keys[generation]!.length + 1);
  const hash = (v: string) => createHash("sha256").update(v).digest("hex");
  const hmac = (key: Buffer | string, value: string) => createHmac("sha256", key).update(value).digest();
  const [date, region, service] = scope.split("/");
  const signature = createHmac("sha256", hmac(hmac(hmac(hmac(`AWS4${secrets[generation]!}`, date!), region!), service!), "aws4_request"))
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
  Date.now = () => now() + 3_400_000;
  try {
    await expect(putObject("denied", new Uint8Array([1]), "image/png")).rejects.toThrow();
    denied = false;
    malformed = true;
    await expect(getObject("malformed")).rejects.toThrow();
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
