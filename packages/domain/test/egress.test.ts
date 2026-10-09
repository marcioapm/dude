import { describe, expect, test } from "bun:test";
import { agentEgressSchema, egressAllows, egressProblem } from "../src/index.ts";

describe("an egress entry", () => {
  test("takes what lux takes: anywhere, an address, a range, a hostname, and a wildcard over a domain of two labels or more", () => {
    for (const ok of ["*", "github.com", "10.0.0.5", "10.0.0.0/8", "2001:db8::/32", "::1", "*.github.com", "*.a.b.example.com"]) {
      expect([ok, egressProblem(ok)]).toEqual([ok, null]);
    }
  });

  test("refuses a wildcard lux would, saying why in lux's words", () => {
    expect(egressProblem("*.com")).toBe(`*.com: a wildcard is "*." then a domain of at least two labels, e.g. *.example.com`);
    expect(egressProblem("*.example")).toContain("at least two labels");
    expect(egressProblem("a.*.com")).toContain("at least two labels");
    expect(egressProblem("**.example.com")).toContain("at least two labels");
    expect(egressProblem("*.example.com:443")).toBe("*.example.com:443: a wildcard is a domain only, without a port or path");
    expect(egressProblem("*.example.com/x")).toContain("without a port or path");
    expect(egressProblem("10.0.0.0/33")).toContain("CIDR");
    expect(egressProblem("not a host")).toContain("not a hostname");
  });
});

describe("an agent egress list", () => {
  test("allows a name as lux does: itself, under a wildcard but not its apex, or anywhere", () => {
    const list = ["pypi.org", "*.github.com", "10.0.0.0/8"];
    expect(egressAllows(list, "pypi.org")).toBe(true);
    expect(egressAllows(list, "PyPI.org.")).toBe(true);
    expect(egressAllows(list, "files.pythonhosted.org")).toBe(false);
    expect(egressAllows(list, "api.github.com")).toBe(true);
    expect(egressAllows(list, "a.b.github.com")).toBe(true);
    expect(egressAllows(list, "github.com")).toBe(false);
    expect(egressAllows(list, "evilgithub.com")).toBe(false);
    expect(egressAllows(["*"], "anything.example")).toBe(true);
    expect(egressAllows(["*.com"], "a.com")).toBe(false);
  });
  test("is lowercased, trimmed and each entry once", () => {
    expect(agentEgressSchema.parse([" GitHub.com", "github.com", "*.GitHub.com", "10.0.0.0/8"])).toEqual(["github.com", "*.github.com", "10.0.0.0/8"]);
  });

  test("refuses an entry lux would, and more than 200", () => {
    const bad = agentEgressSchema.safeParse(["github.com", "*.com"]);
    expect(bad.success).toBe(false);
    expect(bad.error?.issues[0]?.message).toContain("at least two labels");
    expect(agentEgressSchema.safeParse(Array.from({ length: 201 }, (_, i) => `h${i}.example.com`)).success).toBe(false);
    expect(agentEgressSchema.safeParse(Array.from({ length: 200 }, (_, i) => `h${i}.example.com`)).success).toBe(true);
  });
});
