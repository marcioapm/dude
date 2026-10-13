import type { HTMLAttributes } from "react";
import { ProjectAvatar } from "./ProjectAvatar.tsx";

/** An organisation as faces name it: its id picks the identity colour. */
export interface Organisation {
  readonly id: string;
  readonly name: string;
  readonly slug?: string | undefined;
}

export interface OrgFaceProps extends Omit<HTMLAttributes<HTMLSpanElement>, "children"> {
  readonly org: Organisation;
  /** Pixels, 14–56. */
  readonly size?: number | undefined;
}

/**
 * An organisation's face. Organisations have no image or colour of their
 * own, so this is a project's shape (a rounded square) with the
 * organisation's initials on the identity colour its id hashes to. Named by
 * the organisation's name; pass `aria-hidden` where the name is beside it.
 */
export function OrgFace({ org, size = 24, ...rest }: OrgFaceProps) {
  return <ProjectAvatar project={{ id: org.id, name: org.name }} size={size} data-org-face={org.id} {...rest} />;
}
