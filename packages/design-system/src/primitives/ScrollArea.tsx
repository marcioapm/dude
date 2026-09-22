import * as RadixScrollArea from "@radix-ui/react-scroll-area";
import { forwardRef, type CSSProperties, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import styles from "./ScrollArea.module.css";

export interface ScrollAreaProps {
  readonly orientation?: "vertical" | "horizontal" | "both" | undefined;
  /** Fill the parent (flex: 1, height: 100%). */
  readonly fill?: boolean | undefined;
  readonly className?: string | undefined;
  readonly viewportClassName?: string | undefined;
  readonly style?: CSSProperties | undefined;
  /** Forwarded to the viewport so consumers can control scrollTop. */
  readonly onScroll?: ((e: React.UIEvent<HTMLDivElement>) => void) | undefined;
  readonly children?: ReactNode;
}

/**
 * Scroll container with an overlay scrollbar that is consistent across
 * platforms (Tauri's WebKit and Chromium draw very different native bars).
 * The ref points at the *viewport*, which is the element that scrolls.
 */
export const ScrollArea = forwardRef<HTMLDivElement, ScrollAreaProps>(function ScrollArea(
  { orientation = "vertical", fill, className, viewportClassName, style, onScroll, children },
  ref,
) {
  return (
    <RadixScrollArea.Root
      className={cx(styles["root"], fill && styles["fill"], className)}
      style={style}
      scrollHideDelay={600}
    >
      <RadixScrollArea.Viewport
        ref={ref}
        className={cx(styles["viewport"], viewportClassName)}
        {...(onScroll ? { onScroll } : {})}
      >
        {children}
      </RadixScrollArea.Viewport>
      {orientation !== "horizontal" ? (
        <RadixScrollArea.Scrollbar orientation="vertical" className={styles["scrollbar"]}>
          <RadixScrollArea.Thumb className={styles["thumb"]} />
        </RadixScrollArea.Scrollbar>
      ) : null}
      {orientation !== "vertical" ? (
        <RadixScrollArea.Scrollbar orientation="horizontal" className={styles["scrollbar"]}>
          <RadixScrollArea.Thumb className={styles["thumb"]} />
        </RadixScrollArea.Scrollbar>
      ) : null}
      <RadixScrollArea.Corner className={styles["corner"]} />
    </RadixScrollArea.Root>
  );
});
