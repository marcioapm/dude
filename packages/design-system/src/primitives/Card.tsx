import type { HTMLAttributes, ReactNode } from "react";
import { cx } from "../util/cx.ts";
import styles from "./Card.module.css";

export interface CardProps extends HTMLAttributes<HTMLDivElement> {
  readonly variant?: "default" | "raised" | "flat" | undefined;
  readonly interactive?: boolean | undefined;
  readonly selected?: boolean | undefined;
  readonly children?: ReactNode;
}

/**
 * Card. A surface one step above its parent. Nesting cards is a smell —
 * prefer dividers inside one card. Use `flat` inside another surface.
 */
export function Card({ variant = "default", interactive, selected, className, children, ...rest }: CardProps) {
  return (
    <div
      className={cx(
        styles["root"],
        variant === "raised" && styles["raised"],
        variant === "flat" && styles["flat"],
        interactive && styles["interactive"],
        selected && styles["selected"],
        className,
      )}
      data-selected={selected ? "true" : undefined}
      {...rest}
    >
      {children}
    </div>
  );
}

export interface CardHeaderProps extends Omit<HTMLAttributes<HTMLDivElement>, "title"> {
  readonly title?: ReactNode;
  readonly actions?: ReactNode;
  readonly children?: ReactNode;
}

export function CardHeader({ title, actions, className, children, ...rest }: CardHeaderProps) {
  return (
    <div className={cx(styles["header"], className)} {...rest}>
      {title !== undefined ? <div className={styles["title"]}>{title}</div> : null}
      {children}
      {actions ? <div className={styles["actions"]}>{actions}</div> : null}
    </div>
  );
}

export interface CardBodyProps extends HTMLAttributes<HTMLDivElement> {
  readonly padding?: "default" | "dense" | "flush" | undefined;
  readonly children?: ReactNode;
}

export function CardBody({ padding = "default", className, children, ...rest }: CardBodyProps) {
  return (
    <div
      className={cx(
        styles["body"],
        padding === "flush" && styles["bodyFlush"],
        padding === "dense" && styles["bodyDense"],
        className,
      )}
      {...rest}
    >
      {children}
    </div>
  );
}

export function CardFooter({ className, children, ...rest }: HTMLAttributes<HTMLDivElement>) {
  return (
    <div className={cx(styles["footer"], className)} {...rest}>
      {children}
    </div>
  );
}
