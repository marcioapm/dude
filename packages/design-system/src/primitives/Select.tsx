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
  /**
   * Muted words after the label, on its line: a machine size's spec
   * ("8 CPUs · 16 GiB · 80 GiB"). Shown in the closed trigger too, after
   * the label, and cut with an ellipsis before the label is.
   */
  readonly meta?: ReactNode;
  /** A muted line under the label, in the list only: what the option means. */
  readonly description?: ReactNode;
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
  /** Under the field, muted, as an Input's hint: what the choice does. */
  readonly hint?: ReactNode;
  /** Under the list, on the chrome shade: a note, or a link to where the options are managed. */
  readonly footer?: ReactNode;
  /** The trigger's id, so an outside label names it. */
  readonly id?: string | undefined;
  readonly "data-testid"?: string | undefined;
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
  hint,
  footer,
  id: givenId,
  "data-testid": testId,
}: SelectProps<T>) {
  const autoId = useId();
  const id = givenId ?? autoId;
  const renderItem = (o: SelectOption<T>) => (
    <RadixSelect.Item
      key={o.value}
      value={o.value}
      disabled={o.disabled ?? false}
      className={cx(styles["item"], o.description ? styles["itemTall"] : undefined)}
      data-value={o.value}
    >
      <RadixSelect.ItemIndicator className={styles["indicator"]}>
        <Icon name="check" size={12} />
      </RadixSelect.ItemIndicator>
      <span className={styles["itemBody"]}>
        <span className={styles["itemLine"]}>
          {/* Label and meta are the item's text: the trigger shows both. */}
          <RadixSelect.ItemText className={styles["itemText"]}>
            <span className={styles["itemLabel"]}>{o.label}</span>
            {o.meta ? <span className={styles["meta"]}>{o.meta}</span> : null}
          </RadixSelect.ItemText>
        </span>
        {o.description ? <span className={styles["description"]}>{o.description}</span> : null}
      </span>
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
        <SelectTrigger id={id} size={size} ariaLabel={ariaLabel ?? label} testId={testId}>
          <span className={styles["value"]}>
            <RadixSelect.Value placeholder={placeholder} />
          </span>
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
            {footer ? <div className={styles["footer"]}>{footer}</div> : null}
          </RadixSelect.Content>
        </RadixSelect.Portal>
      </RadixSelect.Root>
      {hint ? <div className={styles["hint"]}>{hint}</div> : null}
    </div>
  );
}

interface TriggerProps {
  readonly id: string;
  readonly size: "sm" | "md";
  readonly ariaLabel: string | undefined;
  readonly testId?: string | undefined;
  readonly children: ReactNode;
}

const SelectTrigger = forwardRef<HTMLButtonElement, TriggerProps>(function SelectTrigger(
  { id, size, ariaLabel, testId, children },
  ref,
) {
  return (
    <RadixSelect.Trigger
      ref={ref}
      id={id}
      className={cx(styles["trigger"], size === "sm" && styles["sm"])}
      aria-label={ariaLabel}
      data-testid={testId}
    >
      {children}
      <RadixSelect.Icon className={styles["chevron"]}>
        <Icon name="chevron-down" size={12} />
      </RadixSelect.Icon>
    </RadixSelect.Trigger>
  );
});
