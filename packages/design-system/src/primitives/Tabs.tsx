import * as RadixTabs from "@radix-ui/react-tabs";
import type { ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { compact } from "../util/compact.ts";
import { Icon, type IconName } from "../icons/index.tsx";
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
  /**
   * `underline` for a page's tabs; `pills` for a second level under them
   * (a session's Conversation / Changes on its task's page); `segmented`
   * for a view switch inside a toolbar.
   */
  readonly variant?: "underline" | "pills" | "segmented" | undefined;
  readonly "aria-label"?: string | undefined;
  readonly className?: string | undefined;
  readonly children?: ReactNode;
}

export function TabList({ variant = "underline", className, children, "aria-label": ariaLabel }: TabListProps) {
  return (
    <RadixTabs.List
      className={cx(styles["list"], variant === "segmented" && styles["segmented"], variant === "pills" && styles["pills"], className)}
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
  /** What it shows is changing now: a breathing dot after the count. */
  readonly live?: boolean | undefined;
  readonly disabled?: boolean | undefined;
  readonly children?: ReactNode;
}

export function Tab({ value, icon, count, live, disabled, children }: TabProps) {
  return (
    <RadixTabs.Trigger value={value} className={styles["trigger"]} disabled={disabled ?? false}>
      {icon ? <Icon name={icon} size={13} /> : null}
      {children}
      {count !== undefined ? <span className={cx(styles["count"], "ds-cap")}>{count}</span> : null}
      {live ? <span className={styles["live"]} aria-label="live" data-testid="tab-live" /> : null}
    </RadixTabs.Trigger>
  );
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
