import * as RadixCheckbox from "@radix-ui/react-checkbox";
import { useId, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { compact } from "../util/compact.ts";
import { Icon } from "../icons/index.tsx";
import styles from "./Checkbox.module.css";

export type CheckedState = boolean | "indeterminate";

export interface CheckboxProps {
  readonly checked?: CheckedState | undefined;
  readonly defaultChecked?: CheckedState | undefined;
  readonly onCheckedChange?: ((checked: CheckedState) => void) | undefined;
  readonly label?: ReactNode;
  readonly description?: ReactNode;
  readonly disabled?: boolean | undefined;
  readonly name?: string | undefined;
  readonly value?: string | undefined;
  readonly className?: string | undefined;
  readonly "aria-label"?: string | undefined;
}

/** Checkbox with optional label/description. 14px box, sized for dense rows. */
export function Checkbox({
  checked,
  defaultChecked,
  onCheckedChange,
  label,
  description,
  disabled,
  name,
  value,
  className,
  "aria-label": ariaLabel,
}: CheckboxProps) {
  const id = useId();
  return (
    <div className={cx(styles["root"], className)} data-disabled={disabled ? "true" : undefined}>
      <RadixCheckbox.Root
        id={id}
        className={styles["box"]}
        disabled={disabled ?? false}
        {...compact({ checked, defaultChecked, onCheckedChange, name, value, "aria-label": ariaLabel })}
      >
        <RadixCheckbox.Indicator className={styles["indicator"]}>
          {checked === "indeterminate" ? (
            <Icon name="minus" size={10} strokeWidth={2.5} />
          ) : (
            <Icon name="check" size={10} strokeWidth={2.5} />
          )}
        </RadixCheckbox.Indicator>
      </RadixCheckbox.Root>
      {label || description ? (
        <label className={styles["text"]} htmlFor={id}>
          {label ? <span className={styles["label"]}>{label}</span> : null}
          {description ? <span className={styles["description"]}>{description}</span> : null}
        </label>
      ) : null}
    </div>
  );
}
