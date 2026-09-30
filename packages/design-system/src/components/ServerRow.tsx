import type { HTMLAttributes, ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { shortId } from "../util/format.ts";
import { bareUrl, canStop, safeServerUrl } from "../util/servers.ts";
import { Icon } from "../icons/index.tsx";
import { Button, IconButton, LinkButton } from "../primitives/Button.tsx";
import { LogStream, type LogLine } from "./LogStream.tsx";
import { ServerStateMark } from "./ServerStateMark.tsx";
import type { ServerDisplayState } from "../tokens/servers.ts";
import styles from "./ServerRow.module.css";

export interface ServerListProps extends HTMLAttributes<HTMLUListElement> {
  readonly children?: ReactNode;
}

/** Servers as rows on the page, in StepList's grammar: no frame, told apart by space. */
export function ServerList({ className, children, ...rest }: ServerListProps) {
  return (
    <ul className={cx(styles["list"], className)} {...rest}>
      {children}
    </ul>
  );
}

export interface ServerLogs {
  readonly open: boolean;
  readonly onToggle: () => void;
  readonly lines: ReadonlyArray<LogLine>;
  /** Still receiving lines. */
  readonly live?: boolean | undefined;
  /** The whole log, elsewhere. */
  readonly onFull?: (() => void) | undefined;
  readonly maxHeight?: number | undefined;
  readonly loading?: boolean | undefined;
}

export interface ServerRowProps extends Omit<HTMLAttributes<HTMLLIElement>, "title"> {
  readonly name: string;
  readonly port: number;
  readonly state: ServerDisplayState;
  /** The mark's word, when not the state's own ("Exited 1"). */
  readonly stateLabel?: string | undefined;
  /** In place of the state mark and detail: what a recipe says of itself. */
  readonly mark?: ReactNode;
  /** Beside the mark, muted: "ready for 12m". */
  readonly detail?: ReactNode;
  /** The last stderr line on exit, in danger ink. */
  readonly error?: string | null | undefined;
  /** Its URL; null when previews are not configured. Clickable only while ready, and only if https. */
  readonly url?: string | null | undefined;
  /** In place of the URL, in the same muted mono: what a recipe would run. */
  readonly urlSlot?: ReactNode;
  readonly onStart?: (() => void) | undefined;
  readonly onStop?: (() => void) | undefined;
  readonly onRestart?: (() => void) | undefined;
  /** The row's overflow menu (the app's RowMenu). */
  readonly menu?: ReactNode;
  readonly logs?: ServerLogs | undefined;
  /** An action is in flight: the buttons wait. */
  readonly busy?: boolean | undefined;
}

/**
 * One server: its name and port in mono, its state and what that means,
 * its URL to copy or open, and what can be done to it — Preview (the URL,
 * in a new tab) while it is ready, Logs always, Stop or Start by its state, a menu for the rest.
 * Its log folds open under the row, titled `server:<name>`.
 */
export function ServerRow({
  name, port, state, stateLabel, mark, detail, error, url, urlSlot, onStart, onStop, onRestart, menu, logs, busy, className, ...rest
}: ServerRowProps) {
  const live = state === "ready";
  const href = safeServerUrl(url);
  return (
    <li className={className} {...rest}>
      <div className={cx(styles["row"], logs?.open && styles["open"])} data-server={name} data-state={state}>
        <div className={styles["name"]}>
          <span className={styles["nameText"]}>{name}</span>
          <span className={styles["port"]}>:{port}</span>
        </div>
        <div className={styles["mid"]}>
          <div className={styles["state"]}>
            {mark ?? <ServerStateMark state={state} size="sm" label={stateLabel} />}
            {detail ? <span className={styles["since"]}>{detail}</span> : null}
            {error ? <span className={styles["err"]} title={error}>{error}</span> : null}
          </div>
          {urlSlot !== undefined ? (
            <div className={cx(styles["url"], styles["off"])}>{urlSlot}</div>
          ) : url ? (
            <div className={cx(styles["url"], !live && styles["off"])}>
              {href ? <a href={href} target="_blank" rel="noopener noreferrer" title={url} tabIndex={live ? undefined : -1}>{bareUrl(url)}</a> : <span title={url}>{url}</span>}
              <IconButton size="sm" icon="copy" label="Copy URL" onClick={() => void navigator.clipboard?.writeText(url)} />
              {live && href ? <a className={styles["openLink"]} href={href} target="_blank" rel="noopener noreferrer" aria-label="Open in a new tab" title="Open in a new tab"><Icon name="external" size={16} /></a> : null}
            </div>
          ) : null}
        </div>
        <div className={styles["actions"]}>
          {live && href ? (
            <LinkButton href={href} size="sm" variant="secondary" leadingIcon="eye" external={false} target="_blank" rel="noopener noreferrer" title="Opens in a new tab" data-testid="server-preview">Preview</LinkButton>
          ) : null}
          {logs ? (
            <Button size="sm" variant="quiet" trailingIcon={logs.open ? "chevron-up" : "chevron-down"} aria-expanded={logs.open} onClick={logs.onToggle} data-testid="server-logs">
              Logs
            </Button>
          ) : null}
          {canStop({ state }) ? (
            <>
              {/* Restart is the way back for a server that went unreachable; Start would be lux's no-op. */}
              {state !== "starting" && onRestart ? <IconButton size="sm" icon="retry" label={`Restart ${name}`} disabled={busy} onClick={onRestart} /> : null}
              {onStop ? <Button size="sm" variant="quiet" leadingIcon="stop" disabled={busy} onClick={onStop} data-testid="server-stop">Stop</Button> : null}
            </>
          ) : state === "waiting" ? (
            <Button size="sm" variant="quiet" leadingIcon="play" disabled>Start now</Button>
          ) : onStart ? (
            <Button size="sm" variant="secondary" leadingIcon="play" disabled={busy} onClick={onStart} data-testid="server-start">Start</Button>
          ) : null}
          {menu}
        </div>
      </div>
      {logs?.open ? (
        <div className={styles["logs"]}>
          <LogStream
            lines={logs.lines}
            title={`server:${name}`}
            live={logs.live}
            maxHeight={logs.maxHeight ?? 240}
            emptyMessage={logs.loading ? "Reading the log…" : "No output yet."}
            toolbar={logs.onFull ? <Button size="sm" variant="quiet" trailingIcon="arrow-right" onClick={logs.onFull}>Full log</Button> : undefined}
          />
        </div>
      ) : null}
    </li>
  );
}

export interface AutostartMarkProps extends Omit<HTMLAttributes<HTMLSpanElement>, "children"> {
  readonly autostart: boolean;
  /** Longer words: "Starts automatically" / "Manual". Short: "Auto" / "Manual". */
  readonly short?: boolean | undefined;
}

/** Whether a branch preview starts a recipe: a check or a dash, and the word. */
export function AutostartMark({ autostart, short, className, ...rest }: AutostartMarkProps) {
  return (
    <span className={cx(styles["previewIn"], !autostart && styles["previewOff"], className)} data-autostart={autostart} {...rest}>
      <span className={styles["previewGlyph"]}><Icon name={autostart ? "check" : "minus"} size={12} strokeWidth={2} /></span>
      {autostart ? (short ? "Auto" : "Starts automatically") : "Manual"}
    </span>
  );
}

export interface ServerRecipeRowProps extends Omit<HTMLAttributes<HTMLLIElement>, "title"> {
  readonly name: string;
  readonly port: number;
  readonly command: string;
  readonly autostart: boolean;
}

/** A recipe as a row in the servers' grammar: what a preview would start. */
export function ServerRecipeRow({ name, port, command, autostart, ...rest }: ServerRecipeRowProps) {
  return <ServerRow name={name} port={port} state="stopped" mark={<AutostartMark autostart={autostart} />} urlSlot={<span className="ds-mono">{command}</span>} {...rest} />;
}

export interface ServersRecipesPreviewProps extends HTMLAttributes<HTMLElement> {
  /** What a preview would start, from the project's definitions. */
  readonly recipes: ReadonlyArray<{ name: string; port: number; command: string; autostartInPreviews: boolean }>;
  /** Under the list, muted: where they are defined. */
  readonly note?: ReactNode;
}

/** "What a preview starts": the project's recipes as rows, for a task nothing is serving. */
export function ServersRecipesPreview({ recipes, note, className, ...rest }: ServersRecipesPreviewProps) {
  return (
    <section className={cx(styles["recipes"], className)} aria-label="Project servers" {...rest}>
      <h2 className="ds-label">What a preview starts</h2>
      <ServerList>
        {recipes.map((r) => (
          <ServerRecipeRow key={r.name} name={r.name} port={r.port} command={r.command} autostart={r.autostartInPreviews} />
        ))}
      </ServerList>
      {note ? <p className={styles["note"]}>{note}</p> : null}
    </section>
  );
}

export interface ServersRunLineProps extends Omit<HTMLAttributes<HTMLDivElement>, "title"> {
  /** Who the run is: the owner's face with their agent on it, or the system's for a preview. */
  readonly avatar: ReactNode;
  /** "Implementer run", "Branch preview". */
  readonly title: ReactNode;
  /** Its status: a StatusMark. */
  readonly status?: ReactNode;
  /** One muted line: id, host, when it started, who for. */
  readonly detail?: ReactNode;
  readonly actions?: ReactNode;
}

/** The run the servers live on: a line, not a card. */
export function ServersRunLine({ avatar, title, status, detail, actions, className, ...rest }: ServersRunLineProps) {
  return (
    <div className={cx(styles["host"], className)} {...rest}>
      {avatar}
      <div className={styles["hostText"]}>
        <span className={styles["hostTitle"]}>{title}{status}</span>
        {detail ? <span className={styles["hostDetail"]}>{detail}</span> : null}
      </div>
      {actions ? <div className={styles["hostActions"]}>{actions}</div> : null}
    </div>
  );
}

/** "Open terminal in lux ↗": a link among buttons, to the run's terminal. */
export function TerminalLink({ href, size = "sm", children = "Open terminal in lux" }: { readonly href: string; readonly size?: "sm" | "md" | undefined; readonly children?: ReactNode }) {
  return (
    <LinkButton href={href} size={size} leadingIcon="terminal" data-testid="terminal-link">
      {children}
    </LinkButton>
  );
}

export interface ServersPanelProps extends HTMLAttributes<HTMLDivElement> {
  readonly children?: ReactNode;
  /** Under the list, muted: what stops servers, where their output goes. */
  readonly note?: ReactNode;
}

/**
 * The servers panel: the run line, a notice when the run moved, a preview's
 * stages, the list, a note. A container: on a narrow page the rows stack.
 */
export function ServersPanel({ note, className, children, ...rest }: ServersPanelProps) {
  return (
    <div className={cx(styles["panel"], className)} {...rest}>
      {children}
      {note ? <p className={styles["note"]}>{note}</p> : null}
    </div>
  );
}

/** The first characters of an id with an ellipsis, the whole in its title. */
export function ShortId({ id, length = 12 }: { readonly id: string; readonly length?: number | undefined }) {
  const short = shortId(id, length);
  return <code title={id}>{short === id ? id : `${short}…`}</code>;
}
