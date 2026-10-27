import type { HTMLAttributes } from "react";
import { cx } from "../util/cx.ts";
import styles from "./CodeWell.module.css";

/**
 * Text to copy as it is, in mono: a square sunken well with the density's
 * code padding and no frame (a curl, a CLI line). `inline` is the same well
 * around one value in a line of text (a URL), wrapping anywhere.
 */
export function CodeWell({ inline, className, children, ...rest }: HTMLAttributes<HTMLElement> & { readonly inline?: boolean | undefined }) {
  if (inline) return <code className={cx(styles["inline"], className)} {...rest}>{children}</code>;
  return <pre className={cx(styles["block"], className)} {...rest}><code>{children}</code></pre>;
}
