import { useEffect, useRef, useState, type HTMLAttributes, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { bareUrl } from "../util/servers.ts";
import { Icon } from "../icons/index.tsx";
import { Button, IconButton, LinkButton } from "../primitives/Button.tsx";
import { Segmented } from "./ScreenHeader.tsx";
import { ServerStateMark } from "./ServerStateMark.tsx";
import type { ServerDisplayState } from "../tokens/servers.ts";
import styles from "./PreviewFrame.module.css";

export type PreviewViewport = "desktop" | "mobile";

export interface PreviewFrameProps extends Omit<HTMLAttributes<HTMLElement>, "title"> {
  /** The server previewed. */
  readonly name: string;
  readonly state: ServerDisplayState;
  readonly stateLabel?: string | undefined;
  /** Where the frame starts: the server's URL. */
  readonly url: string;
  /** Beside the URL's host, muted: who the visitor is to the preview ("Cloudflare Access · ana@acme.com"). */
  readonly access?: ReactNode;
  /** Docked beside a conversation rather than over the page: no scrim, no slide, narrower head. */
  readonly docked?: boolean | undefined;
  readonly onClose: () => void;
  readonly onLogs?: (() => void) | undefined;
  readonly onRestart?: (() => void) | undefined;
  /** In the foot: what the server is doing ("web · ready for 12m"), and anything else known. */
  readonly foot?: ReactNode;
  /** Right in the foot, muted: who can see this. */
  readonly footNote?: ReactNode;
  /** For the gallery: what the frame shows instead of the URL. */
  readonly srcDoc?: string | undefined;
  readonly defaultViewport?: PreviewViewport | undefined;
}

/**
 * A server's page, in a frame with a browser's chrome over it: back,
 * forward, reload, the URL with its host and path, who is signed in, and
 * Desktop or Mobile width. As a side sheet over the page (`PreviewScrim`
 * beside it), or docked next to a conversation.
 *
 * The frame is cross-origin: what it shows is the server's, so its path
 * is known only as far as this component navigated it.
 */
export function PreviewFrame({ name, state, stateLabel, url, access, docked, onClose, onLogs, onRestart, foot, footNote, srcDoc, defaultViewport = "desktop", className, ...rest }: PreviewFrameProps) {
  const frame = useRef<HTMLIFrameElement>(null);
  const [viewport, setViewport] = useState<PreviewViewport>(defaultViewport);
  // What the chrome bar says: the path is ours to know only when we set it.
  const [location, setLocation] = useState(url);
  const [history, setHistory] = useState<{ back: number; forward: number }>({ back: 0, forward: 0 });
  useEffect(() => setLocation(url), [url]);
  const parsed = safeParse(location);

  const reload = () => {
    const el = frame.current;
    if (!el) return;
    // Re-assigning the same src is a reload without touching the cross-origin document.
    if (srcDoc === undefined) el.src = location;
    else el.srcdoc = srcDoc;
  };
  const go = (delta: number) => {
    // The frame's own history is the window's; stepping it moves the frame
    // when the frame made the last entries, which the counters track.
    if (delta < 0 && history.back > 0) {
      window.history.back();
      setHistory((h) => ({ back: h.back - 1, forward: h.forward + 1 }));
    } else if (delta > 0 && history.forward > 0) {
      window.history.forward();
      setHistory((h) => ({ back: h.back + 1, forward: h.forward - 1 }));
    }
  };

  return (
    <aside
      className={cx(styles["panel"], docked && styles["docked"], className)}
      role={docked ? "complementary" : "dialog"}
      aria-label={`Preview of ${name}`}
      data-testid="preview-frame"
      data-server={name}
      {...rest}
    >
      <div className={styles["head"]}>
        <span className={styles["headTitle"]}>
          <Icon name="eye" size={16} />
          Preview <code>{name}</code>
          <ServerStateMark state={state} size="sm" label={stateLabel} />
        </span>
        <span className={styles["headSpacer"]} />
        {onLogs ? <Button size="sm" variant="quiet" onClick={onLogs} className={styles["headAction"]}>Logs</Button> : null}
        {onRestart ? <Button size="sm" variant="quiet" leadingIcon="retry" onClick={onRestart} className={styles["headAction"]}>Restart</Button> : null}
        <LinkButton href={location} size="sm">Open in a new tab</LinkButton>
        <IconButton size="sm" icon="close" label="Close preview" onClick={onClose} data-testid="preview-close" />
      </div>
      <div className={styles["chrome"]}>
        <span className={styles["nav"]}>
          <IconButton size="sm" icon="arrow-right" label="Back" className={styles["back"]} disabled={history.back === 0} onClick={() => go(-1)} />
          <IconButton size="sm" icon="arrow-right" label="Forward" disabled={history.forward === 0} onClick={() => go(1)} />
          <IconButton size="sm" icon="retry" label="Reload" onClick={reload} />
        </span>
        <span className={styles["urlbar"]} title={location}>
          <span className={styles["lock"]}><Icon name="check" size={12} strokeWidth={2} /></span>
          <span className={styles["host"]}>{parsed ? parsed.host : bareUrl(location)}</span>
          {parsed && parsed.path !== "/" ? <span className={styles["path"]}>{parsed.path}</span> : null}
          {access ? <span className={styles["access"]}><Icon name="human" size={12} />{access}</span> : null}
        </span>
        <Segmented
          label="Viewport"
          size="sm"
          value={viewport}
          onChange={setViewport}
          options={[{ value: "desktop", label: "Desktop" }, { value: "mobile", label: "Mobile" }]}
        />
      </div>
      <div className={cx(styles["frameWrap"], viewport === "mobile" && styles["mobile"])}>
        <iframe
          ref={frame}
          className={styles["frame"]}
          title={`${name} preview`}
          // The page is the server's: it runs as itself, on its own origin,
          // and may open new windows; the shell's storage stays out of reach.
          sandbox="allow-scripts allow-same-origin allow-forms allow-popups allow-modals allow-downloads"
          {...(srcDoc !== undefined ? { srcDoc } : { src: url })}
        />
      </div>
      <div className={styles["foot"]}>
        {foot}
        {footNote ? <span className={styles["footNote"]}>{footNote}</span> : null}
      </div>
    </aside>
  );
}

/** The scrim behind a preview sheet over the page; a click on it closes the sheet. */
export function PreviewScrim({ onClose }: { readonly onClose: () => void }) {
  return <div className={styles["scrim"]} aria-hidden onClick={onClose} data-testid="preview-scrim" />;
}

function safeParse(url: string): { host: string; path: string } | null {
  try {
    const u = new URL(url);
    return { host: u.host, path: `${u.pathname}${u.search}${u.hash}` };
  } catch {
    return null;
  }
}
