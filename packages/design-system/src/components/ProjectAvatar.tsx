import type { HTMLAttributes } from "react";
import { cx } from "../util/cx.ts";
import { identitySlot, initialsOf } from "./PersonAvatar.tsx";
import styles from "./ProjectAvatar.module.css";

export interface ProjectFace {
  readonly id?: string | undefined;
  readonly name: string;
  /** An uploaded image. Without one, initials on the project's identity colour. */
  readonly imageUrl?: string | null | undefined;
  /** An identity slot the project chose (0–7); by default a hash of its id. */
  readonly colorSlot?: number | undefined;
}

export interface ProjectAvatarProps extends Omit<HTMLAttributes<HTMLSpanElement>, "children"> {
  readonly project: ProjectFace;
  /** Pixels, 16–56. */
  readonly size?: number | undefined;
}

/**
 * A project: a rounded square whose corner is a share of its size
 * (`--ds-radius-face-project`), an image or initials on an identity colour.
 * Squarer than an agent's tile and never a glyph, rounder than nothing —
 * so a person, a project and an agent are three shapes in grayscale.
 */
export function ProjectAvatar({ project, size = 20, className, style, ...rest }: ProjectAvatarProps) {
  const slot = project.colorSlot ?? identitySlot(project);
  return (
    <span
      className={cx(styles["root"], className)}
      data-identity={slot}
      role="img"
      aria-label={project.name}
      title={project.name}
      style={{ ["--pv-size" as string]: `${size}px`, ...style }}
      {...rest}
    >
      {project.imageUrl ? <img className={styles["image"]} src={project.imageUrl} alt="" /> : initialsOf(project.name)}
    </span>
  );
}
