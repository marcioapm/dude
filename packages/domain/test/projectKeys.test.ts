import { describe, expect, test } from "bun:test";
import { deriveProjectKey, PROJECT_KEY, projectKeyCandidates } from "../src/projectKeys.ts";

describe("a project's key, when none is chosen", () => {
  test("is the slug's first four letters while that is free", () => {
    expect(deriveProjectKey("billing-api", [])).toBe("BILL");
    expect(deriveProjectKey("web", [])).toBe("WEB");
    expect(deriveProjectKey("3d-printer", [])).toBe("DPRI");
  });

  test("then the words' initials padded with the letters after the last, then the first with 2 to 9, then 10 to 99", () => {
    expect(projectKeyCandidates("billing-worker").slice(0, 4)).toEqual(["BILL", "BWOR", "BILL2", "BILL3"]);
    expect(projectKeyCandidates("billing-api").slice(0, 3)).toEqual(["BILL", "BAPI", "BILL2"]);
    expect(projectKeyCandidates("customer-web-portal-v2").slice(0, 2)).toEqual(["CUST", "CWPV"]);
    expect(projectKeyCandidates("api-2-gateway").slice(0, 2)).toEqual(["APIG", "AGAT"]);
    expect(projectKeyCandidates("a-b-c-d-e").slice(0, 2)).toEqual(["ABCD", "ABCD2"]);
    expect(projectKeyCandidates("billing").slice(0, 3)).toEqual(["BILL", "BILL2", "BILL3"]);
    const all = projectKeyCandidates("billing-worker");
    expect(all.slice(-3)).toEqual(["BILL97", "BILL98", "BILL99"]);
    expect(all.length).toBe(1 + 1 + 98);
  });

  test("billing-api then billing-worker get two distinct keys: BILL, then BWOR", () => {
    const api = deriveProjectKey("billing-api", [])!;
    const worker = deriveProjectKey("billing-worker", [api])!;
    expect([api, worker]).toEqual(["BILL", "BWOR"]);
    expect(deriveProjectKey("billing-ledger", [api, worker])).toBe("BLED");
    expect(deriveProjectKey("billing", ["bill", "Bill2"])).toBe("BILL3");
  });

  test("a slug with fewer than two letters starts at its first letter, or is WI", () => {
    expect(deriveProjectKey("a1", [])).toBe("A1");
    expect(deriveProjectKey("x", [])).toBe("WI");
    expect(deriveProjectKey("2024", [])).toBe("WI");
    expect(deriveProjectKey("2024", ["WI"])).toBe("WI2");
  });

  test("every candidate is a key a person could choose, and none when all are taken", () => {
    for (const slug of ["billing-api", "billing-worker", "a1", "x", "2024", "a-b-c-d-e", "z9-zz", "q-1", "customer-web-portal-v2"]) {
      for (const k of projectKeyCandidates(slug)) expect(k).toMatch(PROJECT_KEY);
    }
    expect(deriveProjectKey("billing", projectKeyCandidates("billing"))).toBeNull();
  });

  test("a slug as long as a slug may be, 100 letters or 100 digits, still gives only keys a person could choose", () => {
    const letters = "abcdefghijklmnopqrstuvwxyz".repeat(4).slice(0, 100);
    const digits = "1234567890".repeat(10);
    expect([letters.length, digits.length]).toEqual([100, 100]);

    const fromLetters = projectKeyCandidates(letters);
    expect(fromLetters.slice(0, 2)).toEqual(["ABCD", "ABCD2"]);
    expect(fromLetters.at(-1)).toBe("ABCD99");
    expect(fromLetters.length).toBe(99);
    for (const k of fromLetters) expect(k).toMatch(PROJECT_KEY);

    const fromDigits = projectKeyCandidates(digits);
    expect(fromDigits.slice(0, 2)).toEqual(["WI", "WI2"]);
    expect(fromDigits.at(-1)).toBe("WI99");
    expect(fromDigits.length).toBe(99);
    for (const k of fromDigits) expect(k).toMatch(PROJECT_KEY);
  });
});

describe("a chosen key", () => {
  test("is 2 to 6 letters or digits, the first a letter", () => {
    for (const k of ["BL", "BILL", "B2", "WEB3", "ABCDEF", "A1B2C3"]) expect(PROJECT_KEY.test(k)).toBe(true);
    for (const k of ["B", "2B", "ABCDEFG", "BI-LL", "BI LL", "", "bill", "BÍLL"]) expect(PROJECT_KEY.test(k)).toBe(false);
  });
});
