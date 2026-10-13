import type { HTMLAttributes } from "react";
import { cx } from "../util/cx.ts";
import { Button } from "../primitives/Button.tsx";
import styles from "./InvitationLinkList.module.css";

export interface InvitationLink {
  readonly email: string;
  readonly name?: string | undefined;
  /** "Member", "Admin". */
  readonly role: string;
  readonly url: string;
  /** An expired invitation renewed: the same link as before. */
  readonly renewed?: boolean | undefined;
}

export interface InvitationLinkListProps extends Omit<HTMLAttributes<HTMLUListElement>, "children" | "onCopy"> {
  readonly links: ReadonlyArray<InvitationLink>;
  readonly onCopy: (link: InvitationLink) => void;
}

/** "Name, email, link" per line: what Copy all puts on the clipboard. */
export function invitationLinesText(links: ReadonlyArray<InvitationLink>): string {
  return links.map((l) => [l.name, l.email, l.url].filter(Boolean).join(", ")).join("\n");
}

/**
 * Each invitee and their link, to send them yourself: who (name, address,
 * role), the link in mono, and Copy. On a phone the link drops under the
 * name. Used by an invite's result and by Create organisation.
 */
export function InvitationLinkList({ links, onCopy, className, ...rest }: InvitationLinkListProps) {
  return (
    <ul className={cx(styles["list"], className)} role="list" aria-label="Invitation links" {...rest}>
      {links.map((l) => {
        const who = l.name ?? l.email;
        return (
          <li key={l.email} className={styles["row"]} data-email={l.email}>
            <span className={styles["who"]}>
              <b className={styles["name"]}>{who}</b>
              <span className={styles["detail"]}>{[l.name ? l.email : null, l.role, l.renewed ? "renewed, same link" : null].filter(Boolean).join(" · ")}</span>
            </span>
            <code className={styles["url"]} data-testid="invitation-url">{l.url}</code>
            <Button size="sm" variant="secondary" leadingIcon="copy" aria-label={`Copy ${who}'s link`} onClick={() => onCopy(l)}>Copy</Button>
          </li>
        );
      })}
    </ul>
  );
}
