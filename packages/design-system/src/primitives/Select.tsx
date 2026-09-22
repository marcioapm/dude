import * as RadixSelect from "@radix-ui/react-select";
import { forwardRef, useId, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { compact } from "../util/compact.ts";
import { Icon } from "../icons/index.tsx";
import styles from "./Select.module.css";

export interface SelectOption<T extends string = string> {
  readonly value: T;
  readonly label: ReactNode;
  readonly disabled?: boolean | undefined;
}

export interface SelectGroup<T extends string = string> {
  readonly label: string;
  readonly options: ReadonlyArray<SelectOption<T>>;
}

export interface SelectProps<T extends string = string> {
  readonly value?: T | undefined;
  readonly defaultValue?: T | undefined;
  readonly onValueChange?: ((value: T) => void) | undefined;
  readonly options: ReadonlyArray<SelectOption<T>> | ReadonlyArray<SelectGroup<T>>;
  readonly placeholder?: string | undefined;
  readonly label?: string | undefined;
  readonly size?: "sm" | "md" | undefined;
  readonly disabled?: boolean | undefined;
  readonly name?: string | undefined;
  readonly className?: string | undefined;
  readonly "aria-label"?: string | undefined;
}

function isGrouped<T extends string>(
  options: ReadonlyArray<SelectOption<T>> | ReadonlyArray<SelectGroup<T>>,
): options is ReadonlyArray<SelectGroup<T>> {
  const first = options[0];
  return first !== undefined && "options" in first;
}

/**
 * Select, built on Radix for keyboard/typeahead/ARIA. Use for <= ~30
 * options. For larger sets, build a combobox with `Input` and a listbox.
 */
export function Select<T extends string = string>({
  value,
  defaultValue,
  onValueChange,
  options,
  placeholder = "Select…",
  label,
  size = "md",
  disabled,
  name,
  className,
  "aria-label": ariaLabel,
}: SelectProps<T>) {
  const id = useId();
  const renderItem = (o: SelectOption<T>) => (
    <RadixSelect.Item
      key={o.value}
      value={o.value}
      disabled={o.disabled ?? false}
      className={styles["item"]}
    >
      <RadixSelect.ItemIndicator className={styles["indicator"]}>
        <Icon name="check" size={12} />
      </RadixSelect.ItemIndicator>
      <RadixSelect.ItemText>{o.label}</RadixSelect.ItemText>
    </RadixSelect.Item>
  );

  return (
    <div className={cx(styles["field"], className)}>
      {label ? (
        <label className={styles["label"]} htmlFor={id}>
          {label}
        </label>
      ) : null}
      <RadixSelect.Root
        disabled={disabled ?? false}
        {...compact({
          value,
          defaultValue,
          onValueChange: onValueChange as ((v: string) => void) | undefined,
          name,
        })}
      >
        <SelectTrigger id={id} size={size} ariaLabel={ariaLabel ?? label}>
          <RadixSelect.Value placeholder={placeholder} />
        </SelectTrigger>
        <RadixSelect.Portal>
          <RadixSelect.Content className={styles["content"]} position="popper" sideOffset={4}>
            <RadixSelect.ScrollUpButton className={styles["scrollButton"]}>
              <Icon name="chevron-up" />
            </RadixSelect.ScrollUpButton>
            <RadixSelect.Viewport className={styles["viewport"]}>
              {isGrouped(options)
                ? options.map((g, i) => (
                    <RadixSelect.Group key={g.label} className={styles["group"]}>
                      {i > 0 ? <RadixSelect.Separator className={styles["separator"]} /> : null}
                      <RadixSelect.Label className={styles["groupLabel"]}>{g.label}</RadixSelect.Label>
                      {g.options.map(renderItem)}
                    </RadixSelect.Group>
                  ))
                : options.map(renderItem)}
            </RadixSelect.Viewport>
            <RadixSelect.ScrollDownButton className={styles["scrollButton"]}>
              <Icon name="chevron-down" />
            </RadixSelect.ScrollDownButton>
          </RadixSelect.Content>
        </RadixSelect.Portal>
      </RadixSelect.Root>
    </div>
  );
}

interface TriggerProps {
  readonly id: string;
  readonly size: "sm" | "md";
  readonly ariaLabel: string | undefined;
  readonly children: ReactNode;
}

const SelectTrigger = forwardRef<HTMLButtonElement, TriggerProps>(function SelectTrigger(
  { id, size, ariaLabel, children },
  ref,
) {
  return (
    <RadixSelect.Trigger
      ref={ref}
      id={id}
      className={cx(styles["trigger"], size === "sm" && styles["sm"])}
      aria-label={ariaLabel}
    >
      {children}
      <RadixSelect.Icon className={styles["chevron"]}>
        <Icon name="chevron-down" size={12} />
      </RadixSelect.Icon>
    </RadixSelect.Trigger>
  );
});
