import { expect, test } from "bun:test";
import pkg from "../../../package.json";
import { MIN_BUN_FOR_S3, s3RuntimeProblem } from "../src/storage.ts";

test("Bun below 1.4.0 is refused for photo storage, naming the floor and the reason", () => {
  for (const version of ["1.3.9", "1.3.14", "1.2.99", "0.9.0", "1.4.0-canary.1", "1.4.0-canary.1+abc"]) {
    const problem = s3RuntimeProblem(version);
    expect(problem).toBe(
      `photo storage (s3.bucket) needs Bun >= 1.4.0, this is Bun ${version}: ` +
      "earlier Bun fails every upload to a store that answers Connection: close, such as versitygw",
    );
  }
});

test("Bun at or above 1.4.0 may store photos", () => {
  expect(MIN_BUN_FOR_S3).toBe("1.4.0");
  for (const version of ["1.4.0", "1.4.2", "1.4.10", "1.10.0", "2.0.0", "1.4.1-canary.3+deadbeef", "1.4.0+build"]) {
    expect(s3RuntimeProblem(version)).toBeUndefined();
  }
});

test("a version that is not major.minor.patch is refused rather than guessed at", () => {
  for (const version of ["", "1.4", "v1.4.0", "1.4.x", "latest"]) {
    expect(s3RuntimeProblem(version)).toContain("needs Bun >= 1.4.0");
  }
});

test("the backend's floor is the one package.json asks for", () => {
  expect(pkg.engines.bun).toBe(`>=${MIN_BUN_FOR_S3}`);
});
