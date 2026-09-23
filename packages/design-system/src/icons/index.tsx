import type { SVGProps } from "react";

/**
 * A small, self-contained icon set. 16px grid, 1.5px strokes, round caps.
 * Icons are the *shape channel* for status — they must read without color.
 */

export type IconName =
  // status glyphs
  | "circle"
  | "circle-dotted"
  | "clock"
  | "circle-half"
  | "spinner"
  | "pause"
  | "check"
  | "cross"
  | "stop"
  | "inbox"
  | "question"
  | "hand"
  | "eye"
  | "merge"
  | "list"
  // actors
  | "system"
  | "human"
  | "agent"
  | "integration"
  // agent roles
  | "orchestrator"
  | "investigator"
  | "implementer"
  | "reviewer"
  | "simplifier"
  | "qa_browser"
  // ui
  | "chevron-down"
  | "chevron-right"
  | "chevron-up"
  | "close"
  | "search"
  | "sort"
  | "sort-asc"
  | "sort-desc"
  | "info"
  | "warning"
  | "alert"
  | "plus"
  | "minus"
  | "more"
  | "external"
  | "copy"
  | "arrow-up"
  | "arrow-down"
  | "arrow-right"
  | "arrow-down-to-line"
  | "file"
  | "folder"
  | "terminal"
  | "dollar"
  | "zap"
  | "git-branch"
  | "git-pr"
  | "layers"
  // chat / activity
  | "caret"
  | "retry"
  | "send"
  | "edit"
  | "globe"
  | "list-check"
  | "message"
  | "brain";

/** Path data on a 16x16 grid. `fill` marks icons that are filled shapes. */
const PATHS: Record<IconName, { d: string; fill?: true; dashed?: true }> = {
  circle: { d: "M8 2.75a5.25 5.25 0 1 0 0 10.5a5.25 5.25 0 0 0 0-10.5Z" },
  "circle-dotted": { d: "M8 2.75a5.25 5.25 0 1 0 0 10.5a5.25 5.25 0 0 0 0-10.5Z", dashed: true },
  clock: { d: "M8 2.75a5.25 5.25 0 1 0 0 10.5a5.25 5.25 0 0 0 0-10.5ZM8 5v3.2l2 1.3" },
  "circle-half": {
    d: "M8 2.75a5.25 5.25 0 1 0 0 10.5a5.25 5.25 0 0 0 0-10.5ZM8 2.75v10.5a5.25 5.25 0 0 0 0-10.5Z",
  },
  spinner: { d: "M8 2.75a5.25 5.25 0 1 1-3.7 1.55" },
  pause: { d: "M5.5 3.5v9M10.5 3.5v9" },
  check: { d: "M3.25 8.5l3 3 6.5-7" },
  cross: { d: "M4 4l8 8M12 4l-8 8" },
  stop: { d: "M4 4h8v8H4z", fill: true },
  inbox: { d: "M2.5 9h3.2l.8 1.5h3l.8-1.5h3.2M2.5 9v3.5h11V9M2.5 9l1.8-5.5h7.4L13.5 9" },
  question: { d: "M8 2.75a5.25 5.25 0 1 0 0 10.5a5.25 5.25 0 0 0 0-10.5ZM6.3 6.5a1.7 1.7 0 1 1 2.4 1.55C8.2 8.3 8 8.7 8 9.2M8 11.2h.01" },
  hand: {
    d: "M6 8.5V3.6a1 1 0 0 1 2 0V7.5M8 7V2.8a1 1 0 0 1 2 0V7.5M10 7.5V4.1a1 1 0 0 1 2 0v5.4c0 2.5-1.7 4.2-4.2 4.2-1.9 0-3-1-3.7-2.2L2.7 9a1 1 0 0 1 1.7-1L6 9.7",
  },
  eye: { d: "M1.75 8s2.25-4.25 6.25-4.25S14.25 8 14.25 8s-2.25 4.25-6.25 4.25S1.75 8 1.75 8ZM8 9.75a1.75 1.75 0 1 0 0-3.5a1.75 1.75 0 0 0 0 3.5Z" },
  merge: { d: "M4.5 3.5v9M4.5 5a1.5 1.5 0 1 0 0-3a1.5 1.5 0 0 0 0 3ZM4.5 14a1.5 1.5 0 1 0 0-3a1.5 1.5 0 0 0 0 3ZM11.5 9.5a1.5 1.5 0 1 0 0-3a1.5 1.5 0 0 0 0 3ZM4.5 6c0 2 1.5 3 5.5 3" },
  list: { d: "M3 4.5h10M3 8h10M3 11.5h6" },

  system: { d: "M8 1.5l1.2 3.4 3.6.1-2.9 2.2 1.1 3.5L8 8.6l-3 2.1 1.1-3.5-2.9-2.2 3.6-.1Z" },
  human: { d: "M8 8a2.75 2.75 0 1 0 0-5.5A2.75 2.75 0 0 0 8 8ZM2.75 13.5c.5-2.5 2.5-4 5.25-4s4.75 1.5 5.25 4" },
  agent: { d: "M4 6.5h8a1.5 1.5 0 0 1 1.5 1.5v3A1.5 1.5 0 0 1 12 12.5H4A1.5 1.5 0 0 1 2.5 11V8A1.5 1.5 0 0 1 4 6.5ZM8 6.5V4M8 4a1 1 0 1 0 0-2a1 1 0 0 0 0 2ZM5.75 9.5h.01M10.25 9.5h.01" },
  integration: { d: "M6 2.5v3M10 2.5v3M4.5 5.5h7v2.5a3.5 3.5 0 0 1-7 0V5.5ZM8 11.5v2" },

  orchestrator: { d: "M8 2.5v3M8 5.5l-4 3M8 5.5l4 3M8 5.5v0M2.5 10.5h3v3h-3zM10.5 10.5h3v3h-3zM6.5 2.5h3v3h-3z" },
  investigator: { d: "M7 11.5A4.5 4.5 0 1 0 7 2.5a4.5 4.5 0 0 0 0 9ZM10.3 10.3l3.2 3.2" },
  implementer: { d: "M9.6 2.7a3.2 3.2 0 0 0-3.9 4L2.5 9.9l1.6 1.6 3.2-3.2a3.2 3.2 0 0 0 4-3.9L9.4 6.3 7.7 4.6Z" },
  reviewer: { d: "M4.5 2.5h7a1 1 0 0 1 1 1v9a1 1 0 0 1-1 1h-7a1 1 0 0 1-1-1v-9a1 1 0 0 1 1-1ZM5.75 8l1.5 1.5 3-3" },
  simplifier: { d: "M4.5 5.5a1.75 1.75 0 1 0 0-3.5a1.75 1.75 0 0 0 0 3.5ZM4.5 14a1.75 1.75 0 1 0 0-3.5a1.75 1.75 0 0 0 0 3.5ZM5.9 4.9l7.6 5.1M5.9 11.1l7.6-5.1" },
  qa_browser: { d: "M2.5 4a1 1 0 0 1 1-1h9a1 1 0 0 1 1 1v8a1 1 0 0 1-1 1h-9a1 1 0 0 1-1-1V4ZM2.5 6h11M4.5 4.5h.01M6.25 4.5h.01M7.5 8l3.5 1.4-1.5.6-.6 1.5Z" },

  "chevron-down": { d: "M4 6l4 4 4-4" },
  "chevron-right": { d: "M6 4l4 4-4 4" },
  "chevron-up": { d: "M4 10l4-4 4 4" },
  close: { d: "M4 4l8 8M12 4l-8 8" },
  search: { d: "M7 11.5A4.5 4.5 0 1 0 7 2.5a4.5 4.5 0 0 0 0 9ZM10.3 10.3l3.2 3.2" },
  sort: { d: "M5 6.5l3-3 3 3M5 9.5l3 3 3-3" },
  "sort-asc": { d: "M5 6.5l3-3 3 3M8 3.5v9" },
  "sort-desc": { d: "M5 9.5l3 3 3-3M8 3.5v9" },
  info: { d: "M8 2.75a5.25 5.25 0 1 0 0 10.5a5.25 5.25 0 0 0 0-10.5ZM8 7.25v3.5M8 5.25h.01" },
  warning: { d: "M8 2.5l6 10.5H2L8 2.5ZM8 6.5v3M8 11.25h.01" },
  alert: { d: "M8 2.75a5.25 5.25 0 1 0 0 10.5a5.25 5.25 0 0 0 0-10.5ZM8 5.25v3.5M8 10.75h.01" },
  plus: { d: "M8 3v10M3 8h10" },
  minus: { d: "M3 8h10" },
  more: { d: "M3.5 8h.01M8 8h.01M12.5 8h.01" },
  external: { d: "M7 3.5H4a1 1 0 0 0-1 1V12a1 1 0 0 0 1 1h7.5a1 1 0 0 0 1-1V9M9.5 2.5h4v4M13.5 2.5L7.5 8.5" },
  copy: { d: "M5.5 5.5V3.5a1 1 0 0 1 1-1h6a1 1 0 0 1 1 1v6a1 1 0 0 1-1 1h-2M3.5 5.5h6a1 1 0 0 1 1 1v6a1 1 0 0 1-1 1h-6a1 1 0 0 1-1-1v-6a1 1 0 0 1 1-1Z" },
  "arrow-up": { d: "M8 13V3M4 7l4-4 4 4" },
  "arrow-down": { d: "M8 3v10M4 9l4 4 4-4" },
  "arrow-right": { d: "M3 8h10M9 4l4 4-4 4" },
  "arrow-down-to-line": { d: "M8 2.5v8M4.5 7l3.5 3.5L11.5 7M3 13.5h10" },
  file: { d: "M4 2.5h5l3 3v8a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1v-10a1 1 0 0 1 1-1ZM9 2.5v3h3" },
  folder: { d: "M2.5 4.5a1 1 0 0 1 1-1h3l1.5 1.5h4.5a1 1 0 0 1 1 1v6a1 1 0 0 1-1 1h-9a1 1 0 0 1-1-1v-7.5Z" },
  terminal: { d: "M3.5 4.5l3.5 3.5-3.5 3.5M8.5 11.5h4" },
  dollar: { d: "M8 2v12M10.75 5.25c0-1.1-1.2-1.75-2.75-1.75S5.25 4.15 5.25 5.25 6.5 7 8 7s2.75.65 2.75 1.75S9.55 10.5 8 10.5s-2.75-.65-2.75-1.75" },
  zap: { d: "M9 1.5L3.5 9h4l-.5 5.5L12.5 7h-4L9 1.5Z" },
  "git-branch": { d: "M4.5 3.5v9M4.5 5a1.5 1.5 0 1 0 0-3a1.5 1.5 0 0 0 0 3ZM4.5 14a1.5 1.5 0 1 0 0-3a1.5 1.5 0 0 0 0 3ZM11.5 6.5a1.5 1.5 0 1 0 0-3a1.5 1.5 0 0 0 0 3ZM11.5 6.5c0 2-1.5 3-7 3.5" },
  "git-pr": { d: "M4.5 5v9M4.5 5a1.5 1.5 0 1 0 0-3a1.5 1.5 0 0 0 0 3ZM11.5 14a1.5 1.5 0 1 0 0-3a1.5 1.5 0 0 0 0 3ZM11.5 11V6a2 2 0 0 0-2-2H7.5M9.5 2L7.5 4l2 2" },
  layers: { d: "M8 2.5l6 3-6 3-6-3 6-3ZM2 8.5l6 3 6-3M2 11.5l6 3 6-3" },

  /* A text caret: the streaming glyph. Filled so it reads as a block cursor. */
  caret: { d: "M6 2.5h4v11H6z", fill: true },
  /* Circular arrow: an attempt about to be made again. */
  retry: { d: "M13 8a5 5 0 1 1-1.6-3.7M13 2.5v2.5h-2.5" },
  send: { d: "M13.5 2.5L2.5 7l5 1.5 1.5 5 4.5-11ZM7.5 8.5l6-6" },
  edit: { d: "M10.5 3l2.5 2.5-7 7H3.5V10l7-7ZM9.25 4.25l2.5 2.5" },
  globe: { d: "M8 2.75a5.25 5.25 0 1 0 0 10.5a5.25 5.25 0 0 0 0-10.5ZM2.75 8h10.5M8 2.75c1.6 1.6 2.4 3.4 2.4 5.25S9.6 11.65 8 13.25M8 2.75C6.4 4.35 5.6 6.15 5.6 8s.8 3.65 2.4 5.25" },
  "list-check": { d: "M2.75 4.5l1 1 2-2M2.75 8.5l1 1 2-2M2.75 12.5l1 1 2-2M8 4.5h5.25M8 8.5h5.25M8 12.5h5.25" },
  message: { d: "M3 3.5h10a.5.5 0 0 1 .5.5v6a.5.5 0 0 1-.5.5H7l-3 2.5V10.5H3a.5.5 0 0 1-.5-.5V4a.5.5 0 0 1 .5-.5Z" },
  /* Two lobes and a midline: the model's reasoning, as opposed to its output. */
  brain: {
    d: "M8 3.5C7.1 2.4 5 2.7 5 4.5C3.5 4.5 2.7 6.1 3.5 7.4C2.5 8.6 3.2 10.3 4.8 10.3C4.8 11.9 6.5 12.8 8 11.7C9.5 12.8 11.2 11.9 11.2 10.3C12.8 10.3 13.5 8.6 12.5 7.4C13.3 6.1 12.5 4.5 11 4.5C11 2.7 8.9 2.4 8 3.5ZM8 3.5v8.2",
  },
};

export interface IconProps extends Omit<SVGProps<SVGSVGElement>, "name"> {
  readonly name: IconName;
  /** Pixel size; defaults to 1em so it scales with the surrounding text. */
  readonly size?: number | string | undefined;
  /** Decorative by default. Provide a title to make it announced. */
  readonly title?: string | undefined;
}

export function Icon({ name, size = "1em", title, className, style, ...rest }: IconProps) {
  const spec = PATHS[name];
  const spin = name === "spinner";
  return (
    <svg
      viewBox="0 0 16 16"
      width={size}
      height={size}
      fill={spec.fill ? "currentColor" : "none"}
      stroke="currentColor"
      strokeWidth={spec.fill ? 0 : 1.5}
      strokeLinecap="round"
      strokeLinejoin="round"
      strokeDasharray={spec.dashed ? "2 2.2" : undefined}
      aria-hidden={title ? undefined : true}
      role={title ? "img" : undefined}
      className={className}
      data-icon={name}
      style={{
        flexShrink: 0,
        ...(spin
          ? {
              animation:
                "ds-spin calc(1.1s / max(var(--ds-motion-live), 0.0001)) linear infinite",
            }
          : null),
        ...style,
      }}
      {...rest}
    >
      {title ? <title>{title}</title> : null}
      <path d={spec.d} />
    </svg>
  );
}

export const ICON_NAMES = Object.keys(PATHS) as IconName[];
