import { memo, useMemo, type CSSProperties, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { isPlainStyle, parseAnsi, type AnsiStyle, type ParseAnsiOptions } from "../util/ansi.ts";
import { ansiColorCss, isLightFill } from "../util/ansiCss.ts";
import { neutral, white } from "../tokens/palette.ts";
import styles from "./AnsiString.module.css";

export interface AnsiStringProps extends ParseAnsiOptions {
  readonly text: string;
}

/**
 * Tool output with its escape codes rendered. The text is the tool's own —
 * its colours are the content (pytest's red, git's green), so they are
 * shown as the terminal would have shown them, mapped onto the theme's
 * `--ds-ansi-*` palette so they read in both modes. Nothing here is an
 * HTML string: each styled run is a React span with a class and, for a
 * colour, an inline `color`.
 *
 * Memoised on the text: a running card re-renders every tick for its
 * clock, and the parse and the spans must not be rebuilt each time.
 */
export const AnsiString = memo(function AnsiString({ text, cutStart }: AnsiStringProps): ReactNode {
  return useMemo(() => {
    const segments = parseAnsi(text, { cutStart });
    return segments.map((s, i) =>
      isPlainStyle(s.style) ? (
        s.text
      ) : (
        <span key={i} className={cx(s.style.bold && styles["bold"], s.style.dim && styles["dim"], s.style.italic && styles["italic"], s.style.underline && styles["underline"])} style={colorStyle(s.style)}>
          {s.text}
        </span>
      ),
    );
  }, [text, cutStart]);
});

/**
 * Inline colour for a run. A foreground on our field is clamped for
 * contrast; one on the tool's own fill is the tool's choice and passes
 * through. Inverse swaps the two, with the surrounding ink and field
 * standing in for whichever was unset. A fill with no ink chosen gets
 * ink that reads on it.
 */
function colorStyle(s: AnsiStyle): CSSProperties | undefined {
  let fg = s.fg === null ? null : ansiColorCss(s.fg, s.bg === null ? "fg" : "ink");
  let bg = s.bg === null ? null : ansiColorCss(s.bg, "bg");
  if (s.inverse) {
    const fill = fg ?? "var(--ds-color-text-primary)";
    const ink = bg ?? "var(--ds-color-field-bg)";
    fg = ink;
    bg = fill;
  } else if (bg !== null && fg === null) {
    fg = s.bg?.kind === "rgb" ? (isLightFill(s.bg) ? neutral[1] : white) : "var(--ds-color-text-inverse)";
  }
  if (fg === null && bg === null) return undefined;
  const out: CSSProperties = {};
  if (fg !== null) out.color = fg;
  if (bg !== null) out.backgroundColor = bg;
  return out;
}
