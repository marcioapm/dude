import { describe, expect, test } from "bun:test";
import { parseAnsi, type AnsiColor, type AnsiSegment, type ParseAnsiOptions } from "../src/util/ansi.ts";
import { ansiColorCss } from "../src/util/ansiCss.ts";
import { ANSI_COLOR_NAMES, ansiColors, neutral, white, type AnsiColorName } from "../src/tokens/palette.ts";

const E = "\u001b";
const plain = { bold: false, dim: false, italic: false, underline: false, inverse: false, fg: null, bg: null };

const strip = (text: string, o?: ParseAnsiOptions) =>
  parseAnsi(text, o)
    .map((s) => s.text)
    .join("");

/** `[text, style patch]` pairs, so a test reads as what it expects. */
function shape(segments: ReadonlyArray<AnsiSegment>): Array<[string, Record<string, unknown>]> {
  return segments.map((s) => {
    const patch: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(s.style)) if (v !== plain[k as keyof typeof plain]) patch[k] = v;
    return [s.text, patch];
  });
}

const named = (name: AnsiColorName): AnsiColor => ({ kind: "named", name });
const rgb = (r: number, g: number, b: number): AnsiColor => ({ kind: "rgb", r, g, b });

describe("parseAnsi", () => {
  test("plain text passes through as one plain segment", () => {
    expect(parseAnsi("hello\nworld")).toEqual([{ text: "hello\nworld", style: plain }]);
  });

  test("empty input is no segments", () => {
    expect(parseAnsi("")).toEqual([]);
    expect(parseAnsi("", { cutStart: true })).toEqual([]);
  });

  test("basic colours and reset", () => {
    expect(shape(parseAnsi(`${E}[31mFAILED${E}[0m tests/a.py ${E}[32mPASSED${E}[m`))).toEqual([
      ["FAILED", { fg: named("red") }],
      [" tests/a.py ", {}],
      ["PASSED", { fg: named("green") }],
    ]);
  });

  test("bright and background colours", () => {
    expect(shape(parseAnsi(`${E}[90mdim${E}[39m ${E}[44mon blue${E}[49m ${E}[101mon bright red${E}[0m`))).toEqual([
      ["dim", { fg: named("bright-black") }],
      [" ", {}],
      ["on blue", { bg: named("blue") }],
      [" ", {}],
      ["on bright red", { bg: named("bright-red") }],
    ]);
  });

  test("bold combined with colour, in one sequence and in two", () => {
    expect(shape(parseAnsi(`${E}[1;31mE${E}[0m`))).toEqual([["E", { bold: true, fg: named("red") }]]);
    expect(shape(parseAnsi(`${E}[1m${E}[32mok${E}[22m still green${E}[0m`))).toEqual([
      ["ok", { bold: true, fg: named("green") }],
      [" still green", { fg: named("green") }],
    ]);
  });

  test("dim, italic, underline, inverse and their offs", () => {
    expect(shape(parseAnsi(`${E}[2;3;4;7mx${E}[22;23;24;27my`))).toEqual([
      ["x", { dim: true, italic: true, underline: true, inverse: true }],
      ["y", {}],
    ]);
  });

  test("256-colour: named, cube and greys", () => {
    expect(shape(parseAnsi(`${E}[38;5;1ma${E}[38;5;196mb${E}[48;5;240mc`))).toEqual([
      ["a", { fg: named("red") }],
      ["b", { fg: rgb(255, 0, 0) }],
      ["c", { fg: rgb(255, 0, 0), bg: rgb(88, 88, 88) }],
    ]);
    expect(shape(parseAnsi(`${E}[38;5;16ma${E}[38;5;231mb${E}[38;5;232mc${E}[38;5;255md${E}[38;5;256me`))).toEqual([
      ["a", { fg: rgb(0, 0, 0) }],
      ["b", { fg: rgb(255, 255, 255) }],
      ["c", { fg: rgb(8, 8, 8) }],
      ["de", { fg: rgb(238, 238, 238) }],
    ]);
  });

  test("truecolor, semicolon and colon forms", () => {
    const fg = rgb(255, 128, 0);
    expect(shape(parseAnsi(`${E}[38;2;255;128;0ma${E}[0m`))).toEqual([["a", { fg }]]);
    expect(shape(parseAnsi(`${E}[38:2:255:128:0ma${E}[0m`))).toEqual([["a", { fg }]]);
    expect(shape(parseAnsi(`${E}[38:2::255:128:0ma${E}[0m`))).toEqual([["a", { fg }]]);
    expect(shape(parseAnsi(`${E}[1;38;2;255;128;0;4ma${E}[0m`))).toEqual([["a", { bold: true, underline: true, fg }]]);
  });

  test("malformed extended colours are ignored, not applied", () => {
    expect(shape(parseAnsi(`${E}[38;2;300;0;0mx`))).toEqual([["x", {}]]);
    expect(shape(parseAnsi(`${E}[38;5mx`))).toEqual([["x", {}]]);
    expect(shape(parseAnsi(`${E}[38mx`))).toEqual([["x", {}]]);
  });

  test("non-SGR escapes are stripped: cursor, erase, modes, charset", () => {
    expect(strip(`${E}[2K${E}[1Gprogress 50%${E}[?25l${E}[3A${E}[0J${E}(B${E}=done${E}7${E}8`)).toBe("progress 50%done");
    expect(strip(`${E}[1;31;40Hnot an SGR${E}[0m`)).toBe("not an SGR");
    // A private-parameter "m" is not SGR either.
    expect(shape(parseAnsi(`${E}[?31mx`))).toEqual([["x", {}]]);
  });

  test("OSC hyperlinks and titles are stripped, with BEL and with ST", () => {
    expect(strip(`${E}]8;;https://example.com\u0007link${E}]8;;\u0007 end`)).toBe("link end");
    expect(strip(`${E}]8;;https://example.com${E}\\link${E}]8;;${E}\\ end`)).toBe("link end");
    expect(strip(`${E}]0;window title\u0007text`)).toBe("text");
    expect(strip(`${E}Pq#0;2;0;0;0${E}\\after dcs`)).toBe("after dcs");
  });

  test("an unterminated OSC loses only its introducer, never the lines after it", () => {
    // Review item 1: one open `ESC ]` must not blank the rest of the head.
    expect(strip(`hello${E}]0;title\nmore\nsecret`)).toBe("hello0;title\nmore\nsecret");
    expect(strip(`a${E}]0;x\nb\u0007c`)).toBe("a0;x\nb\u0007c");
    expect(strip(`a${E}]0;x\nb\u0007c`).split("\n")).toHaveLength(2);
    // Past the bound on one line, the same: only the introducer goes.
    const long = "x".repeat(600);
    expect(strip(`${E}]8;;${long}\u0007link`)).toBe(`8;;${long}\u0007link`);
    // Within the bound and the input simply ends: that is the cap's cut, dropped whole.
    expect(strip(`ok ${E}]8;;https://exa`)).toBe("ok ");
  });

  test("carriage returns collapse a line to its last frame, as a terminal shows it", () => {
    // Review item 4: a progress bar's frames must not all render on one line.
    expect(strip("10%\r20%\r100%\ndone")).toBe("100%\ndone");
    expect(strip(`${E}[2K\r${E}[36m⠋${E}[0m Resolving 10%\r${E}[2K${E}[32m✓${E}[0m Resolved\n`)).toBe("✓ Resolved\n");
    expect(shape(parseAnsi(`${E}[31mred\r${E}[32mgreen${E}[0m\n`))).toEqual([
      ["green", { fg: named("green") }],
      ["\n", {}],
    ]);
    // \r\n is a line ending; a trailing \r (a bar cut mid-frame) is just removed.
    expect(strip("one\r\ntwo\r\n")).toBe("one\ntwo\n");
    expect(strip("frame 1\rframe 2\r")).toBe("frame 2");
    // The style in force survives the \r, as it does on a terminal.
    expect(shape(parseAnsi(`${E}[33mabc\rxyz`))).toEqual([["xyz", { fg: named("yellow") }]]);
    // Lines and line counts are preserved.
    expect(strip("a\rb\nc\rd\ne").split("\n")).toEqual(["b", "d", "e"]);
  });

  test("a sequence cut at the end of the input is dropped whole", () => {
    expect(parseAnsi(`ok ${E}[01;3`)).toEqual([{ text: "ok ", style: plain }]);
    expect(parseAnsi(`ok ${E}[`)).toEqual([{ text: "ok ", style: plain }]);
    expect(parseAnsi(`ok ${E}`)).toEqual([{ text: "ok ", style: plain }]);
    expect(shape(parseAnsi(`${E}[31mred ${E}[0`))).toEqual([["red ", { fg: named("red") }]]);
  });

  test("a sequence cut at the start of a tail is dropped when cutStart is set", () => {
    expect(strip("01;34mbin/", { cutStart: true })).toBe("bin/");
    expect(strip("[01;34mbin/", { cutStart: true })).toBe("bin/");
    expect(strip("[mbin/", { cutStart: true })).toBe("bin/");
    expect(strip(";;https://example.com\u0007link", { cutStart: true })).toBe("link");
    expect(strip(`;;https://example.com${E}\\link`, { cutStart: true })).toBe("link");
    // A short code whose m runs straight into a glyph or an escape.
    expect(strip(`2m✓ routes`, { cutStart: true })).toBe("✓ routes");
    expect(shape(parseAnsi(`4m${E}[32my`, { cutStart: true }))).toEqual([["y", { fg: named("green") }]]);
    // A cut SGR longer than 32 chars (a truecolor pair).
    expect(strip("38;2;255;128;0;48;2;10;20;30;1;4mX", { cutStart: true })).toBe("X");
  });

  test("cutStart never eats real text", () => {
    // Review item 2: durations and words that end in m are text.
    expect(strip("10ms elapsed", { cutStart: true })).toBe("10ms elapsed");
    expect(strip("5m ago", { cutStart: true })).toBe("5m ago");
    expect(strip("12m", { cutStart: true })).toBe("12m");
    expect(strip("main.rs\nmod.rs", { cutStart: true })).toBe("main.rs\nmod.rs");
    expect(strip("mbin/", { cutStart: true })).toBe("mbin/");
    expect(strip("12 passed in 0.31s", { cutStart: true })).toBe("12 passed in 0.31s");
    // Without cutStart, a cut-looking start is text: the head begins at byte 0.
    expect(strip("[01;34mbin/")).toBe("[01;34mbin/");
    // A BEL on a later line is not a cut OSC.
    expect(strip("line one\nb\u0007", { cutStart: true })).toBe("line one\nb\u0007");
  });

  test("a literal [ that is not an escape is text", () => {
    const s = "arr[0] = [1, 2]; ESC[31m is not red; [01;34m either";
    expect(parseAnsi(s)).toEqual([{ text: s, style: plain }]);
  });

  test("ESC before a newline or control drops only the ESC", () => {
    expect(strip(`a${E}\nb`)).toBe("a\nb");
    expect(strip(`a${E}[31\nb`)).toBe("a\nb");
  });

  test("styles do not carry across head and tail: each parse starts plain", () => {
    expect(shape(parseAnsi(`${E}[31mred `))).toEqual([["red ", { fg: named("red") }]]);
    expect(shape(parseAnsi("still?", { cutStart: true }))).toEqual([["still?", {}]]);
  });

  test("adjacent runs of one style merge; empty runs vanish", () => {
    expect(parseAnsi(`${E}[31m${E}[31ma${E}[1m${E}[22mb${E}[0m${E}[0m`)).toEqual([{ text: "ab", style: { ...plain, fg: named("red") } }]);
  });

  test("newlines are preserved through styled runs (line counts survive)", () => {
    const text = `${E}[32m✓ one${E}[0m\n${E}[31m✗ two${E}[0m\n${E}[2K\rthree\n`;
    expect(strip(text).split("\n")).toHaveLength(4);
    expect(text.split("\n")).toHaveLength(4);
  });
});

// WCAG 2 relative luminance and contrast, for checking the palette.
function luminance(hex: string): number {
  const ch = (i: number) => {
    const v = Number.parseInt(hex.slice(i, i + 2), 16) / 255;
    return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4);
  };
  return 0.2126 * ch(1) + 0.7152 * ch(3) + 0.0722 * ch(5);
}
function contrast(a: string, b: string): number {
  const la = luminance(a);
  const lb = luminance(b);
  return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05);
}
const lightDark = (css: string) => css.slice("light-dark(".length, -1).split(", ") as [string, string];

describe("ansi palette", () => {
  test("every named colour reads on the field background in both modes", () => {
    const fieldBg = { dark: neutral[1], light: white };
    for (const mode of ["dark", "light"] as const) {
      for (const name of ANSI_COLOR_NAMES) {
        const c = contrast(ansiColors[mode][name], fieldBg[mode]);
        expect(c, `${mode} ${name} ${ansiColors[mode][name]}`).toBeGreaterThanOrEqual(4.5);
      }
    }
  });

  test("the sixteen slots are sixteen colours in each mode", () => {
    // Review item 8: light "white" and "bright-black" had collapsed to one value.
    for (const mode of ["dark", "light"] as const) {
      const values = ANSI_COLOR_NAMES.map((n) => ansiColors[mode][n]);
      expect(new Set(values).size, mode).toBe(ANSI_COLOR_NAMES.length);
    }
  });

  test("named colours map to tokens; rgb text on our field is clamped per mode", () => {
    expect(ansiColorCss(named("red"), "fg")).toBe("var(--ds-ansi-red)");
    const [light, dark] = lightDark(ansiColorCss(rgb(0, 0, 0), "fg"));
    expect(contrast(dark, neutral[1])).toBeGreaterThanOrEqual(4.5);
    expect(contrast(light, white)).toBeGreaterThanOrEqual(4.5);
    const [bl, bd] = lightDark(ansiColorCss(rgb(255, 255, 200), "fg"));
    expect(contrast(bd, neutral[1])).toBeGreaterThanOrEqual(4.5);
    expect(contrast(bl, white)).toBeGreaterThanOrEqual(4.5);
  });

  test("rgb ink on the tool's own fill, and the fill, pass through unclamped", () => {
    // Review item 3: dark text on a white fill must stay dark.
    expect(ansiColorCss(rgb(20, 20, 20), "ink")).toBe("#141414");
    expect(ansiColorCss(rgb(255, 255, 255), "bg")).toBe("#ffffff");
    // The cache hands back the same string for the same colour and use.
    expect(ansiColorCss(rgb(20, 20, 20), "ink")).toBe(ansiColorCss(rgb(20, 20, 20), "ink"));
    expect(ansiColorCss(rgb(20, 20, 20), "fg")).not.toBe("#141414");
  });
});
