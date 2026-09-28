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
  /** After the count: a mark that says more than a number (a state's dot, "1 ready"). */
  readonly trailing?: ReactNode;
  readonly disabled?: boolean | undefined;
  readonly children?: ReactNode;
}

export function Tab({ value, icon, count, trailing, disabled, children }: TabProps) {
  return (
    <RadixTabs.Trigger value={value} className={styles["trigger"]} disabled={disabled ?? false}>
      {icon ? <Icon name={icon} size={13} /> : null}
      {children}
      {count !== undefined ? <span className={cx(styles["count"], "ds-cap")}>{count}</span> : null}
      {trailing}
    </RadixTabs.Trigger>
  );
}

/** A tab's count, for something in its row that is not a tab: "1 ready" on a toggle. */
export function TabCount({ children }: { readonly children: ReactNode }) {
  return <span className={cx(styles["count"], "ds-cap")}>{children}</span>;
}

export interface TabToggleProps {
  readonly pressed: boolean;
  readonly onPressedChange: (pressed: boolean) => void;
  readonly icon?: IconName | undefined;
  readonly title?: string | undefined;
  readonly trailing?: ReactNode;
  readonly children?: ReactNode;
  readonly "data-testid"?: string | undefined;
}

/**
 * A toggle drawn as a tab, for a panel that opens beside the tabs' content
 * rather than in its place (the run screen's Servers drawer). Not a tab
 * to the keyboard: it is a pressed button in the tab row, and sits at
 * its end after a spacer.
 */
export function TabToggle({ pressed, onPressedChange, icon, title, trailing, children, "data-testid": testId }: TabToggleProps) {
  return (
    <>
      <span className={styles["spacer"]} />
      <button
        type="button"
        className={styles["trigger"]}
        data-state={pressed ? "active" : "inactive"}
        aria-pressed={pressed}
        title={title}
        onClick={() => onPressedChange(!pressed)}
        data-testid={testId}
      >
        {icon ? <Icon name={icon} size={13} /> : null}
        {children}
        {trailing}
      </button>
    </>
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
