import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import styles from "./Button.module.css";

export type ButtonVariant =
  | "primary"
  | "secondary"
  | "ghost"
  | "destructive"
  | "destructive-outline";
export type ButtonSize = "sm" | "md" | "lg";

export interface ButtonProps extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, "type"> {
  readonly variant?: ButtonVariant | undefined;
  readonly size?: ButtonSize | undefined;
  readonly type?: "button" | "submit" | "reset" | undefined;
  readonly leadingIcon?: IconName | undefined;
  readonly trailingIcon?: IconName | undefined;
  readonly loading?: boolean | undefined;
  readonly block?: boolean | undefined;
  readonly children?: ReactNode;
}

const VARIANT_CLASS: Record<ButtonVariant, string | undefined> = {
  primary: styles["primary"],
  secondary: styles["secondary"],
  ghost: styles["ghost"],
  destructive: styles["destructive"],
  "destructive-outline": styles["destructiveOutline"],
};

/**
 * Button. Default is `secondary` — in a dense console, most actions are
 * quiet. Exactly one `primary` per view; `destructive` only for actions that
 * lose work (abort a run, delete), and always behind a confirm dialog.
 */
export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  {
    variant = "secondary",
    size = "md",
    type = "button",
    leadingIcon,
    trailingIcon,
    loading = false,
    block = false,
    className,
    children,
    disabled,
    ...rest
  },
  ref,
) {
  return (
    <button
      ref={ref}
      type={type}
      className={cx(
        styles["root"],
        VARIANT_CLASS[variant],
        (variant === "primary" || variant === "destructive") && styles["filled"],
        size !== "md" && styles[size],
        loading && styles["loading"],
        block && styles["block"],
        className,
      )}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      {...rest}
    >
      {leadingIcon ? <Icon name={leadingIcon} /> : null}
      {children}
      {trailingIcon ? <Icon name={trailingIcon} /> : null}
      {loading ? (
        <span className={styles["spinner"]} aria-hidden>
          <Icon name="spinner" />
        </span>
      ) : null}
    </button>
  );
});

export interface IconButtonProps extends Omit<ButtonProps, "leadingIcon" | "trailingIcon" | "children"> {
  readonly icon: IconName;
  /** Required: an icon-only button needs an accessible name. */
  readonly label: string;
}

/** Square icon-only button. `label` becomes the aria-label and tooltip text. */
export const IconButton = forwardRef<HTMLButtonElement, IconButtonProps>(function IconButton(
  { icon, label, variant = "ghost", className, ...rest },
  ref,
) {
  return (
    <Button
      ref={ref}
      variant={variant}
      className={cx(styles["iconOnly"], className)}
      aria-label={label}
      title={label}
      {...rest}
    >
      <Icon name={icon} size={16} />
    </Button>
  );
});
