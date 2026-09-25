import { createContext, useContext, type CSSProperties, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import type { ThemeMode } from "../tokens/themes.ts";
import { DENSITIES, type Density } from "../tokens/density.ts";
import styles from "./gallery.module.css";

export type PaneMode = "both" | "dark" | "light";
export type PaneDensity = "both" | Density;

/** Which densities every `Panes` draws; set once by the gallery's density control. */
export const PaneDensityContext = createContext<PaneDensity>("comfortable");

/** The densities a gallery frame draws for the density control's choice. */
export function densitiesFor(paneDensity: PaneDensity): readonly Density[] {
  return paneDensity === "both" ? DENSITIES : [paneDensity];
}

/**
 * Renders the same children once per theme (columns) and density (rows),
 * each pane scoped with its own `data-theme` and `data-density`. Children
 * are rendered per pane, so local state is independent per pane.
 */
export function Panes({
  mode,
  surface,
  children,
  style,
}: {
  readonly mode: PaneMode;
  /** Pane background = surface instead of canvas (for things that sit on cards). */
  readonly surface?: boolean | undefined;
  readonly children: ReactNode | ((theme: ThemeMode, density: Density) => ReactNode);
  readonly style?: CSSProperties | undefined;
}) {
  const paneDensity = useContext(PaneDensityContext);
  const modes: ThemeMode[] = mode === "both" ? ["dark", "light"] : [mode];
  const densities = densitiesFor(paneDensity);
  return (
    <div className={cx(styles["panes"], modes.length === 1 && styles["panesSingle"])}>
      {densities.flatMap((d) =>
        modes.map((m) => (
          <div
            key={`${d}-${m}`}
            className={cx(styles["pane"], surface && styles["paneSurface"])}
            data-theme={m}
            data-density={d}
            style={{ colorScheme: m, ...style }}
          >
            <span className={styles["paneLabel"]}>{densities.length > 1 ? `${m} · ${d}` : m}</span>
            {typeof children === "function" ? children(m, d) : children}
          </div>
        )),
      )}
    </div>
  );
}

export function Section({ id, title, intro, children }: { readonly id: string; readonly title: string; readonly intro?: ReactNode; readonly children: ReactNode }) {
  return (
    <section id={id} className={styles["section"]}>
      <h2 className={styles["sectionTitle"]}>{title}</h2>
      {intro ? <p className={styles["sectionIntro"]}>{intro}</p> : null}
      {children}
    </section>
  );
}

export function Block({ id, title, note, children }: { readonly id?: string | undefined; readonly title: string; readonly note?: ReactNode; readonly children: ReactNode }) {
  return (
    <div id={id} className={styles["block"]}>
      <h3 className={styles["blockTitle"]}>{title}</h3>
      {note ? <p className={styles["blockNote"]}>{note}</p> : null}
      {children}
    </div>
  );
}

export function Row({ top, children, style }: { readonly top?: boolean | undefined; readonly children: ReactNode; readonly style?: CSSProperties | undefined }) {
  return (
    <div className={cx(styles["row"], top && styles["rowTop"])} style={style}>
      {children}
    </div>
  );
}

export function Col({ children, style }: { readonly children: ReactNode; readonly style?: CSSProperties | undefined }) {
  return (
    <div className={styles["col"]} style={style}>
      {children}
    </div>
  );
}

export function Caption({ children }: { readonly children: ReactNode }) {
  return <span className={styles["caption"]}>{children}</span>;
}

export function Label({ children }: { readonly children: ReactNode }) {
  return <div className={styles["label"]}>{children}</div>;
}

/** Rows of `caption | example` for enumerating states. */
export function States({ items }: { readonly items: ReadonlyArray<readonly [string, ReactNode]> }) {
  return (
    <div className={styles["stateGrid"]}>
      {items.map(([k, v]) => (
        <StateRow key={k} k={k} v={v} />
      ))}
    </div>
  );
}
function StateRow({ k, v }: { readonly k: string; readonly v: ReactNode }) {
  return (
    <>
      <Caption>{k}</Caption>
      <div className={styles["row"]}>{v}</div>
    </>
  );
}
