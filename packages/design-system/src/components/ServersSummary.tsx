import type { HTMLAttributes, ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { openLinkWith } from "../util/openLink.ts";
import { bareUrl, canStart, safeServerUrl } from "../util/servers.ts";
import { Icon } from "../icons/index.tsx";
import { IconButton, LinkButton } from "../primitives/Button.tsx";
import { ServerStateMark } from "./ServerStateMark.tsx";
import type { ServerDisplayState } from "../tokens/servers.ts";
import styles from "./ServersSummary.module.css";

export interface ServersSummaryProps extends HTMLAttributes<HTMLElement> {
  /** After the title, muted: "on the implementer run". */
  readonly where?: ReactNode;
  /** A line under the head, in attention ink: "Stopped when the run moved host at 14:32." */
  readonly notice?: ReactNode;
  /** In place of the rows: "No run is serving this branch." */
  readonly empty?: ReactNode;
  readonly actions?: ReactNode;
  readonly children?: ReactNode;
}

/**
 * The task overview's servers, beside the pull request: one row per
 * server with its state as a glyph and its URL or a word, so a ready URL
 * is one click away. PullRequestPanel's grammar, on the chrome shade.
 */
export function ServersSummary({ where, notice, empty, actions, className, children, ...rest }: ServersSummaryProps) {
  return (
    <aside className={cx(styles["root"], className)} aria-label="Servers" {...rest}>
      <div className={styles["head"]}>
        <Icon name="globe" size={16} className={styles["glyph"]} />
        <span className={styles["title"]}>Servers</span>
        {where ? <span className={styles["where"]}>{where}</span> : null}
      </div>
      {notice ? <p className={styles["notice"]}>{notice}</p> : null}
      {empty ? <p className={styles["empty"]}>{empty}</p> : <ul className={styles["rows"]}>{children}</ul>}
      {actions ? <div className={styles["actions"]}>{actions}</div> : null}
    </aside>
  );
}

export interface ServersSummaryRowProps extends Omit<HTMLAttributes<HTMLLIElement>, "title"> {
  readonly name: string;
  readonly state: ServerDisplayState;
  readonly stateLabel?: string | undefined;
  /** Its URL while ready (a link, and Preview opens it in a new tab); else the state's words. */
  readonly url?: string | null | undefined;
  readonly detail?: ReactNode;
  readonly onStart?: (() => void) | undefined;
  /** For a server that went unreachable: Start would be lux's no-op. */
  readonly onRestart?: (() => void) | undefined;
  /** The app's own way to open the URL, as ServerRow's: a plain left click on the link or Preview; false leaves it to the link. */
  readonly onOpen?: ((url: string) => boolean) | undefined;
  /** The eye Preview's accessible name while `onOpen` handles it; else "Preview <name> (opens in a new tab)". */
  readonly openLabel?: string | undefined;
}

export function ServersSummaryRow({ name, state, stateLabel, url, detail, onStart, onRestart, onOpen, openLabel, className, ...rest }: ServersSummaryRowProps) {
  const ready = state === "ready";
  const href = safeServerUrl(url);
  const open = ready && href ? openLinkWith(onOpen, href) : undefined;
  return (
    <li className={cx(styles["row"], className)} data-server={name} data-state={state} {...rest}>
      <span className={styles["name"]}>{name}</span>
      <ServerStateMark state={state} size="sm" iconOnly label={stateLabel} />
      <span className={styles["url"]}>
        {ready && href ? <a href={href} target="_blank" rel="noopener noreferrer" title={href} onClick={open} data-testid="server-url">{bareUrl(href)}</a> : detail}
      </span>
      {ready && href ? (
        <LinkButton href={href} size="sm" iconOnly leadingIcon="eye" label={open && openLabel ? openLabel : `Preview ${name} (opens in a new tab)`} external={false} target="_blank" rel="noopener noreferrer"
          onClick={open} data-testid="server-preview" />
      ) : state === "unreachable" && onRestart ? (
        <IconButton size="sm" icon="retry" label={`Restart ${name}`} onClick={onRestart} />
      ) : canStart({ state }) && onStart ? (
        <IconButton size="sm" icon="play" label={`Start ${name}`} onClick={onStart} />
      ) : null}
    </li>
  );
}
