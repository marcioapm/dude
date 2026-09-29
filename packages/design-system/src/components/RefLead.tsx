import type { HTMLAttributes } from "react";
import type { TaskStatus } from "@dude/domain";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import { ProjectAvatar } from "./ProjectAvatar.tsx";
import { StatusMark } from "./StatusMark.tsx";
import styles from "./RefLead.module.css";

/*
 * What a thing is, in the sidebar's grammar, where a list names tasks,
 * epics, projects and memories side by side (search results, what a memory
 * is about): a task is its status mark and its key in mono; an epic the
 * `layers` glyph; a project its face; a memory the `memory` glyph. The
 * sizes are the design system's, so every list draws them alike.
 */

export type RefType = "task" | "epic" | "project" | "memory";

export interface RefLeadSpec {
  readonly type: RefType;
  /** A task's key (TEXT-12). (`key` is React's.) */
  readonly taskKey?: string | undefined;
  readonly status?: TaskStatus | undefined;
  /** A project's name, for its face; an epic's or a memory's, shown only with `named`. */
  readonly name?: string | undefined;
  /** For a project's face colour. */
  readonly id?: string | undefined;
}

export interface RefLeadProps extends RefLeadSpec, Omit<HTMLAttributes<HTMLSpanElement>, "children" | "id"> {
  /** Say its name after the lead (an epic's title, a project's name): for a list of what something is about. */
  readonly named?: boolean | undefined;
}

export function RefLead({ type, taskKey, status, name, id, named, className, ...rest }: RefLeadProps) {
  return (
    <span className={cx(styles["root"], className)} data-ref={type} {...rest}>
      {type === "task" ? (
        <>
          {status ? <StatusMark status={status} iconOnly size="sm" /> : null}
          <span className={styles["key"]}>{taskKey ?? name}</span>
        </>
      ) : type === "project" ? (
        <ProjectAvatar project={{ id: id ?? name ?? "", name: name ?? "" }} size={16} />
      ) : (
        <span className={styles["glyph"]}>
          <Icon name={type === "epic" ? "layers" : "memory"} size={14} />
        </span>
      )}
      {named && type !== "task" && name ? <span className={styles["name"]}>{name}</span> : null}
    </span>
  );
}
