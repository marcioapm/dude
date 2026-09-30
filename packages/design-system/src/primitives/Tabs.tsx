import * as RadixTabs from "@radix-ui/react-tabs";
import { forwardRef, type ComponentPropsWithoutRef, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { compact } from "../util/compact.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import { Tooltip } from "./Tooltip.tsx";
import styles from "./Tabs.module.css";

export interface TabsProps {
  readonly value?: string | undefined;
  readonly defaultValue?: string | undefined;
  readonly onValueChange?: ((value: string) => void) | undefined;
  /** Fill the parent's height; content becomes a flex column. */
  readonly fill?: boolean | undefined;
  readonly className?: string | undefined;
  readonly children?: ReactNode;
}

/** Tabs. Compose: Tabs > TabList > Tab, then TabPanel per value. */
export function Tabs({ value, defaultValue, onValueChange, fill, className, children }: TabsProps) {
  return (
    <RadixTabs.Root
      className={cx(styles["root"], fill && styles["rootFill"], className)}
      {...compact({ value, defaultValue, onValueChange })}
    >
      {children}
    </RadixTabs.Root>
  );
}

export interface TabListProps {
  readonly variant?: "underline" | "segmented" | undefined;
  readonly "aria-label"?: string | undefined;
  readonly className?: string | undefined;
  readonly children?: ReactNode;
}

export function TabList({ variant = "underline", className, children, "aria-label": ariaLabel }: TabListProps) {
  return (
    <RadixTabs.List
      className={cx(styles["list"], variant === "segmented" && styles["segmented"], className)}
      {...compact({ "aria-label": ariaLabel })}
    >
      {children}
    </RadixTabs.List>
  );
}

export interface TabProps {
  readonly value: string;
  readonly icon?: IconName | undefined;
  /** Small trailing count, e.g. number of findings. */
  readonly count?: number | undefined;
  /** After the count: a mark that says more than a number (a state's dot). */
  readonly trailing?: ReactNode;
  /**
   * What the tab's count stands for, told on hover and on keyboard focus
   * (a `Tooltip`). Supplementary: the tab's name and count stay its label.
   */
  readonly tooltip?: ReactNode;
  readonly disabled?: boolean | undefined;
  readonly children?: ReactNode;
}

/**
 * Radix's tab trigger, keeping its own `data-state`: a tooltip around it
 * passes the tooltip's ("delayed-open", "closed"), which would take the
 * selected tab's underline and colour away.
 */
const TabTrigger = forwardRef<HTMLButtonElement, ComponentPropsWithoutRef<typeof RadixTabs.Trigger> & { "data-state"?: string }>(
  function TabTrigger({ "data-state": _tooltipState, ...props }, ref) {
    return <RadixTabs.Trigger ref={ref} {...props} />;
  },
);

export function Tab({ value, icon, count, trailing, tooltip, disabled, children }: TabProps) {
  const trigger = (
    <TabTrigger value={value} className={styles["trigger"]} disabled={disabled ?? false}>
      {icon ? <Icon name={icon} size={13} /> : null}
      {children}
      {count !== undefined ? <span className={cx(styles["count"], "ds-cap")}>{count}</span> : null}
      {trailing}
    </TabTrigger>
  );
  // Always the Tooltip wrapper: swapping it in and out would remount the
  // button and drop keyboard focus when the tooltip arrives or leaves.
  return (
    <Tooltip content={tooltip} side="bottom" disabled={!tooltip}>
      {trigger}
    </Tooltip>
  );
}

/** A tab's count, drawn apart: for a `trailing` that carries a count of its own. */
export function TabCount({ children }: { readonly children: ReactNode }) {
  return <span className={cx(styles["count"], "ds-cap")}>{children}</span>;
}

export interface TabPanelProps {
  readonly value: string;
  readonly fill?: boolean | undefined;
  readonly className?: string | undefined;
  readonly children?: ReactNode;
}

export function TabPanel({ value, fill, className, children }: TabPanelProps) {
  return (
    <RadixTabs.Content value={value} className={cx(styles["content"], fill && styles["contentFill"], className)}>
      {children}
    </RadixTabs.Content>
  );
}
