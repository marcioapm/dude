/**
 * WCAG 2 contrast validation for the palette against the *surfaces that
 * actually use it*, executed against the live token values rather than
 * inspected by eye. This is the automated half of the README's "How the
 * colours were chosen" contract: the four chromatic tone foregrounds, the
 * six role colour slots, the text ladder and the accent all clear their
 * documented floors on the surfaces they are drawn on, in both themes.
 *
 * The other half, CVD (protan/deutan) simulation and ΔE pair distance, is
 * `paletteDistance.test.ts`.
 */

import { describe, expect, test } from "bun:test";
import { AGENT_ROLE_NAMES, TONE_NAMES, accent, roleColors, tones } from "../src/tokens/palette.ts";
import { themeColors, type ThemeMode } from "../src/tokens/themes.ts";
import { tints } from "../src/tokens/tints.ts";

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

const MODES: readonly ThemeMode[] = ["dark", "light"];

/** `color-mix(in srgb, top p, bottom)` flattened to a hex, as the browser composites a tint. */
function mix(top: string, bottom: string, p: number): string {
  const ch = (h: string, i: number) => Number.parseInt(h.slice(i, i + 2), 16);
  return `#${[1, 3, 5].map((i) => Math.round(ch(top, i) * p + ch(bottom, i) * (1 - p)).toString(16).padStart(2, "0")).join("")}`;
}

describe("text ladder contrast (Discord/Obsidian-soft, not a bright-white spike)", () => {
  test("primary text lands close to 10-12:1 on the surface, not >=14:1", () => {
    for (const mode of MODES) {
      const c = themeColors[mode];
      const ratio = contrast(c.textPrimary, c.surface);
      expect(ratio, `${mode} primary-on-surface`).toBeGreaterThanOrEqual(9.5);
      expect(ratio, `${mode} primary-on-surface`).toBeLessThan(13.5);
    }
  });

  test("secondary text lands around 6-7:1 on the surface", () => {
    for (const mode of MODES) {
      const c = themeColors[mode];
      const ratio = contrast(c.textSecondary, c.surface);
      expect(ratio, `${mode} secondary-on-surface`).toBeGreaterThanOrEqual(5.5);
      expect(ratio, `${mode} secondary-on-surface`).toBeLessThan(8.5);
    }
  });

  test("primary/secondary/muted sit in even, close steps rather than one big jump", () => {
    for (const mode of MODES) {
      const c = themeColors[mode];
      const primary = contrast(c.textPrimary, c.surface);
      const secondary = contrast(c.textSecondary, c.surface);
      const muted = contrast(c.textMuted, c.surface);
      // Neither gap dominates the other by more than 3x — a "spike" would
      // show up as one step much larger than its neighbour. Contrast ratio
      // is nonlinear near the low end, so this is a loose sanity check,
      // not a precision target.
      const gap1 = primary - secondary;
      const gap2 = secondary - muted;
      expect(gap1 / gap2, `${mode} step evenness`).toBeLessThan(3);
      expect(gap2 / gap1, `${mode} step evenness`).toBeLessThan(3);
    }
  });

  test("every text slot that carries read content clears 4.5:1 on canvas, surface, raised and chrome", () => {
    // Sidebar text sits on canvas, the transcript on surface, cards on
    // raised, panel headers on chrome. `overlay` is for menus and carries
    // primary text.
    for (const mode of MODES) {
      const c = themeColors[mode];
      for (const bg of ["canvas", "surface", "raised", "chrome"] as const) {
        expect(contrast(c.textPrimary, c[bg]), `${mode} primary on ${bg}`).toBeGreaterThanOrEqual(4.5);
        expect(contrast(c.textSecondary, c[bg]), `${mode} secondary on ${bg}`).toBeGreaterThanOrEqual(4.5);
        expect(contrast(c.textMuted, c[bg]), `${mode} muted on ${bg}`).toBeGreaterThanOrEqual(4.5);
      }
    }
  });
});

describe("surface ladder", () => {
  test("dark surfaces are charcoal, not near-black", () => {
    // Obsidian's darkest is #1e1e1e (~1.23:1 against pure black).
    expect(contrast(themeColors.dark.canvas, "#000000")).toBeGreaterThanOrEqual(1.2);
  });

  test("dark elevation steps (canvas -> surface -> raised -> overlay) are small, even shades, not hairline-vs-void", () => {
    const c = themeColors.dark;
    const steps = [
      contrast(c.canvas, c.surface),
      contrast(c.surface, c.raised),
      contrast(c.raised, c.overlay),
    ];
    for (const s of steps) {
      expect(s).toBeGreaterThan(1.02);
      expect(s).toBeLessThan(1.35);
    }
  });
});

describe("tones on the new surfaces", () => {
  test("every chromatic tone fg clears 4.5:1 on its theme's surface", () => {
    for (const mode of MODES) {
      const surface = themeColors[mode].surface;
      for (const t of TONE_NAMES) {
        const fg = tones[mode][t].fg;
        expect(contrast(fg, surface), `${mode} ${t}`).toBeGreaterThanOrEqual(4.5);
      }
    }
  });
});

describe("role colours on the new surfaces", () => {
  test("the brainstorm wears the conductor's colour in both modes", () => {
    for (const mode of MODES) expect(roleColors[mode].brainstorm, mode).toEqual(roleColors[mode].conductor);
  });
  test("every role fg clears 4.5:1 on its theme's surface", () => {
    for (const mode of MODES) {
      const surface = themeColors[mode].surface;
      for (const r of AGENT_ROLE_NAMES) {
        expect(contrast(roleColors[mode][r].fg, surface), `${mode} ${r}`).toBeGreaterThanOrEqual(4.5);
      }
    }
  });
});

describe("accent", () => {
  test("accent text clears 4.5:1 on the surface (link contrast)", () => {
    for (const mode of MODES) {
      expect(contrast(accent[mode].text, themeColors[mode].surface), mode).toBeGreaterThanOrEqual(4.5);
    }
  });

  test("focus ring clears 3:1 against the surface it appears on", () => {
    for (const mode of MODES) {
      expect(contrast(accent[mode].ring, themeColors[mode].surface), mode).toBeGreaterThanOrEqual(3);
    }
  });

  test("onAccent (button label) clears 4.5:1 on the accent fill at rest, hover and active", () => {
    for (const mode of MODES) {
      const c = themeColors[mode];
      for (const fill of [c.accent, c.accentHover, c.accentActive]) {
        expect(contrast(c.onAccent, fill), `${mode} ${fill}`).toBeGreaterThanOrEqual(4.5);
      }
    }
  });

  test("the accent fill stands 3:1 off the surface, as a UI component must", () => {
    for (const mode of MODES) {
      expect(contrast(themeColors[mode].accent, themeColors[mode].surface), mode).toBeGreaterThanOrEqual(3);
    }
  });
});

describe("filled tone buttons and emphasis ink", () => {
  // Attention fills the Answer button, danger the destructive button; these
  // carry text labels. Success/info solids fill glyph-only marks.
  test("labelled tone buttons clear 4.5:1 for their on-solid label", () => {
    for (const mode of MODES) {
      for (const t of ["attention", "danger"] as const) {
        const tone = tones[mode][t];
        expect(contrast(tone.onSolid, tone.solid), `${mode} ${t} on-solid`).toBeGreaterThanOrEqual(4.5);
      }
    }
  });

  test("strong text (names, headings) sits a step past primary", () => {
    for (const mode of MODES) {
      const c = themeColors[mode];
      expect(contrast(c.textStrong, c.surface), `${mode} strong`).toBeGreaterThan(contrast(c.textPrimary, c.surface));
    }
  });
});

describe("text on tints", () => {
  // Tints as the components composite them, from the same `tints` values
  // the CSS reads: QuestionCard's waiting wash is attention-solid over the
  // surface, the sidebar needs-you block attention over the canvas, a
  // failed tool card danger-bg over the surface.
  test("the waiting question's muted clock and primary body clear 4.5:1 on its wash", () => {
    for (const mode of MODES) {
      const c = themeColors[mode];
      const wash = mix(tones[mode].attention.solid, c.surface, tints.highlight);
      expect(contrast(c.textMuted, wash), `${mode} muted`).toBeGreaterThanOrEqual(4.5);
      expect(contrast(c.textPrimary, wash), `${mode} primary`).toBeGreaterThanOrEqual(4.5);
    }
  });

  test("the needs-you block's secondary ink (ask line, key, +N) clears 4.5:1 on its tint", () => {
    // Muted drops to ~4.3:1 on the light tint, so nothing inside the block uses it.
    for (const mode of MODES) {
      const c = themeColors[mode];
      const tint = mix(tones[mode].attention.solid, c.canvas, tints.needsYou);
      expect(contrast(c.textSecondary, tint), `${mode} secondary`).toBeGreaterThanOrEqual(4.5);
    }
  });

  test("a failed tool call's error summary (primary) and meta (muted) clear 4.5:1 on the danger fill", () => {
    for (const mode of MODES) {
      const c = themeColors[mode];
      const fill = mix(tones[mode].danger.bg, c.surface, tints.failedFill);
      expect(contrast(c.textPrimary, fill), `${mode} primary`).toBeGreaterThanOrEqual(4.5);
      expect(contrast(c.textMuted, fill), `${mode} muted`).toBeGreaterThanOrEqual(4.5);
    }
  });

  test("the composer's muted placeholder clears 4.5:1 on the raised field", () => {
    for (const mode of MODES) {
      const c = themeColors[mode];
      expect(contrast(c.textMuted, c.fieldRaised), `${mode} placeholder`).toBeGreaterThanOrEqual(4.5);
    }
  });
});

/** An `rgba()` wash token composited over an opaque background. */
function over(wash: string, bg: string): string {
  const [r, g, b, a] = wash.match(/[\d.]+/g)!.map(Number) as [number, number, number, number];
  return mix(`#${[r, g, b].map((v) => v.toString(16).padStart(2, "0")).join("")}`, bg, a);
}

describe("text on row states", () => {
  // Hovered and selected rows (NavTree on the canvas, a selected row on the
  // surface) and a steer turn at rest and hovered. Muted falls under 4.5:1
  // on several of these in light mode, so metadata on them (NavTree key,
  // "+N" and epic count; a steer's time and "delivered") uses secondary.
  const states = (mode: ThemeMode) => {
    const c = themeColors[mode];
    return {
      "canvas + hover wash": over(c.hoverWash, c.canvas),
      "canvas + active wash": over(c.activeWash, c.canvas),
      "surface + active wash": over(c.activeWash, c.surface),
      "surface + steer wash": mix(c.accent, c.surface, tints.highlight),
      "surface + steer hover": mix(c.accent, c.surface, tints.highlightHover),
    };
  };

  test("secondary metadata clears 4.5:1", () => {
    for (const mode of MODES) {
      for (const [state, bg] of Object.entries(states(mode))) {
        expect(contrast(themeColors[mode].textSecondary, bg), `${mode} secondary on ${state}`).toBeGreaterThanOrEqual(4.5);
      }
    }
  });
});

describe("sidebar muted ink on its washes (light was 3.95:1)", () => {
  test("muted clears 4.5:1 on the hover and active washes over the canvas", () => {
    // The project name, a finished session's title and the search field's
    // "/" hint are muted, on a hovered or selected row or the search wash.
    for (const mode of MODES) {
      const c = themeColors[mode];
      for (const wash of [c.hoverWash, c.activeWash]) expect(contrast(c.textMuted, over(wash, c.canvas)), `${mode} on ${wash}`).toBeGreaterThanOrEqual(4.5);
    }
  });
});

describe("badge labels on their own tint", () => {
  test("every tone fg clears 4.5:1 on its tone bg (a Completed or Answered badge)", () => {
    for (const mode of MODES) {
      for (const t of TONE_NAMES) expect(contrast(tones[mode][t].fg, tones[mode][t].bg), `${mode} ${t}`).toBeGreaterThanOrEqual(4.5);
    }
  });
});

describe("disabled filled button", () => {
  test("its secondary label clears 4.5:1 on the active wash over surface and chrome", () => {
    for (const mode of MODES) {
      const c = themeColors[mode];
      for (const bg of [c.surface, c.chrome]) expect(contrast(c.textSecondary, over(c.activeWash, bg)), `${mode} on ${bg}`).toBeGreaterThanOrEqual(4.5);
    }
  });
});

describe("status marks", () => {
  test("every tone's mark clears 3:1 (non-text) on the canvas and the surface", () => {
    for (const mode of MODES) {
      const c = themeColors[mode];
      for (const t of TONE_NAMES) {
        expect(contrast(tones[mode][t].mark, c.canvas), `${mode} ${t} on canvas`).toBeGreaterThanOrEqual(3);
        expect(contrast(tones[mode][t].mark, c.surface), `${mode} ${t} on surface`).toBeGreaterThanOrEqual(3);
      }
    }
  });

  test("light marks are lighter than the text fg, so an 8px mark keeps its hue", () => {
    for (const t of TONE_NAMES) {
      expect(luminance(tones.light[t].mark), t).toBeGreaterThan(luminance(tones.light[t].fg));
    }
  });
});
