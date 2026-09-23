/**
 * GitHub webhook signatures: the only thing standing between the public
 * internet and a signal that wakes an agent.
 */

import { describe, expect, test } from "bun:test";
import { createHmac } from "node:crypto";
import { verifySignature } from "../src/api/routes/pullRequests.ts";

const body = JSON.stringify({ action: "opened" });
const sign = (secret: string, payload = body) =>
  `sha256=${createHmac("sha256", secret).update(payload).digest("hex")}`;

describe("verifySignature", () => {
  test("accepts GitHub's signature of the body", () => {
    expect(verifySignature("s3cret", body, sign("s3cret"))).toBe(true);
  });

  test("refuses a signature made with another secret", () => {
    expect(verifySignature("s3cret", body, sign("other"))).toBe(false);
  });

  test("refuses a body changed after signing", () => {
    expect(verifySignature("s3cret", JSON.stringify({ action: "closed" }), sign("s3cret"))).toBe(false);
  });

  test("refuses a missing secret, header or prefix", () => {
    expect(verifySignature("", body, sign(""))).toBe(false);
    expect(verifySignature("s3cret", body, null)).toBe(false);
    expect(verifySignature("s3cret", body, sign("s3cret").slice("sha256=".length))).toBe(false);
  });

  test("refuses a truncated signature rather than throwing", () => {
    expect(verifySignature("s3cret", body, sign("s3cret").slice(0, 20))).toBe(false);
  });
});
