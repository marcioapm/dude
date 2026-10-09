import { useId, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import { Badge } from "../primitives/Badge.tsx";
import { Kbd } from "../primitives/Kbd.tsx";
import { Tooltip } from "../primitives/Tooltip.tsx";
import styles from "./ImagePicker.module.css";

/*
 * Picking a container image from a library: a combobox (SearchPicker's
 * grammar: ↑ ↓ move, Enter picks, Escape closes) whose list floats under
 * the field, since it sits in a settings row, not a dialog's section. It
 * stores an image's id; the version is never chosen — whoever names an
 * image runs its latest published version. The list is the app's, already
 * loaded: an organization's images are tens, so the filtering is here.
 */

export interface ImageChoiceView {
  readonly id: string;
  readonly name: string;
  readonly description?: string | undefined;
  /** Its published version's number; null for none yet. */
  readonly version: number | null;
  /** The organization's default base. */
  readonly isDefault?: boolean | undefined;
  /** Hidden from the list unless it is the one chosen. */
  readonly archived?: boolean | undefined;
  /** A newer version building or waiting, or one that failed. */
  readonly status?: { readonly kind: "building" | "waiting" | "failed"; readonly version: number } | null | undefined;
  /** Its published version can run containers. */
  readonly canRunContainers?: boolean | undefined;
}

export interface ImagePickerProps {
  readonly images: ReadonlyArray<ImageChoiceView>;
  /** The chosen image's id; null for none. */
  readonly value: string | null;
  readonly onChange: (id: string | null) => void;
  readonly label: string;
  /** What choosing none means here: "Acme's default base · acme-base". Shown when value is null. */
  readonly noneLabel?: ReactNode;
  /** Offer "none" in the list, with this text ("Use the organisation's"). */
  readonly allowNone?: string | undefined;
  /** "Acme's images": the list's heading. */
  readonly heading?: string | undefined;
  /** No match for the words: offer making an image FROM them. */
  readonly onCreateFrom?: ((text: string) => void) | undefined;
  /** "Manage images", at the list's foot. */
  readonly manage?: { readonly label: string; readonly onClick: () => void } | undefined;
  readonly disabled?: boolean | undefined;
  readonly placeholder?: string | undefined;
  readonly "data-testid"?: string | undefined;
}

/** What a choice's state says, as a badge: building, waiting or failed. */
export function ImageStatusBadge({ status }: { readonly status: ImageChoiceView["status"] }) {
  if (!status) return null;
  const tone = status.kind === "failed" ? "danger" : "info";
  const words = status.kind === "failed" ? `v${status.version} failed` : status.kind === "building" ? `v${status.version} building` : `v${status.version} waiting`;
  return (
    <Badge size="sm" tone={tone} emphasis="tinted" dot={status.kind === "building"}>
      {words}
    </Badge>
  );
}

/**
 * "Can run containers": an image whose published version can, wherever it
 * is listed. With tooltip it is a Run's, in its header: a focusable trigger,
 * as MachineChip is, that opens the tooltip on hover, focus and press.
 */
export function CanRunContainersBadge({ size = "sm", lower, tooltip }: {
  readonly size?: "sm" | "md" | undefined;
  readonly lower?: boolean | undefined;
  readonly tooltip?: ReactNode | undefined;
}) {
  const badge = (
    <Badge size={size} icon="cube" emphasis="tinted" data-testid={tooltip ? undefined : "can-run-containers"}>
      {lower ? "can run containers" : "Can run containers"}
    </Badge>
  );
  if (!tooltip) return badge;
  return (
    <Tooltip content={tooltip} side="bottom" keepOnPress>
      <button type="button" className={styles["badgeTrigger"]} data-testid="can-run-containers"
        aria-label="Can run containers: this Run can start containers inside it">
        {badge}
      </button>
    </Tooltip>
  );
}

/** An image's face: a cube, on the accent tint when it is the default base. */
export function ImageMark({ isDefault, size = 28 }: { readonly isDefault?: boolean | undefined; readonly size?: number | undefined }) {
  return (
    <span className={cx(styles["mark"], isDefault && styles["markDefault"])} style={{ width: size, height: size }} aria-hidden>
      <Icon name="cube" size={Math.round(size * 0.55)} />
    </span>
  );
}

/** Where `words` match in `text`, wrapped strong. */
function highlight(text: string, words: string): ReactNode {
  const at = words ? text.toLowerCase().indexOf(words.toLowerCase()) : -1;
  if (at < 0) return text;
  return (
    <>
      {text.slice(0, at)}
      <b className={styles["match"]}>{text.slice(at, at + words.length)}</b>
      {text.slice(at + words.length)}
    </>
  );
}

type Row = { kind: "image"; image: ImageChoiceView } | { kind: "none" } | { kind: "create"; text: string };

export function ImagePicker({
  images,
  value,
  onChange,
  label,
  noneLabel,
  allowNone,
  heading,
  onCreateFrom,
  manage,
  disabled,
  placeholder = "Find an image",
  "data-testid": testId,
}: ImagePickerProps) {
  const listId = useId();
  const input = useRef<HTMLInputElement>(null);
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const chosen = images.find((i) => i.id === value) ?? null;

  const rows = useMemo<Row[]>(() => {
    const words = query.trim().toLowerCase();
    const matching = images.filter(
      (i) => (!i.archived || i.id === value) && (!words || i.name.includes(words) || (i.description ?? "").toLowerCase().includes(words)),
    );
    const out: Row[] = [];
    if (allowNone && !words) out.push({ kind: "none" });
    for (const image of matching) out.push({ kind: "image", image });
    if (words && matching.length === 0 && onCreateFrom) out.push({ kind: "create", text: query.trim() });
    return out;
  }, [images, query, value, allowNone, onCreateFrom]);

  const close = () => {
    setOpen(false);
    setQuery("");
  };
  const pick = (row: Row) => {
    if (row.kind === "image") onChange(row.image.id);
    else if (row.kind === "none") onChange(null);
    else onCreateFrom?.(row.text);
    close();
  };
  const optionId = (i: number) => `${listId}-${i}`;

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      if (!open) setOpen(true);
      else if (rows.length) setActive((i) => (i + 1) % rows.length);
    } else if (e.key === "ArrowUp" && open && rows.length) {
      e.preventDefault();
      setActive((i) => (i - 1 + rows.length) % rows.length);
    } else if (e.key === "Enter" && open && rows[active]) {
      e.preventDefault();
      pick(rows[active]!);
    } else if (e.key === "Escape" && open) {
      e.preventDefault();
      e.stopPropagation();
      close();
    }
  };

  const shown = open ? query : chosen ? chosen.name : "";
  return (
    <div className={styles["root"]} data-testid={testId}>
      <div className={cx(styles["field"], open && styles["fieldOpen"])} data-disabled={disabled ? "true" : undefined}>
        <ImageMark isDefault={chosen?.isDefault} size={22} />
        <input
          ref={input}
          role="combobox"
          aria-label={label}
          aria-expanded={open}
          aria-controls={listId}
          aria-autocomplete="list"
          spellCheck={false}
          autoComplete="off"
          aria-activedescendant={open && rows[active] ? optionId(active) : undefined}
          className={cx(styles["input"], chosen && !open && styles["inputChosen"])}
          value={shown}
          placeholder={chosen ? undefined : open ? placeholder : undefined}
          disabled={disabled}
          onFocus={() => {
            setOpen(true);
            setActive(0);
          }}
          onBlur={close}
          onChange={(e) => {
            setQuery(e.target.value);
            setActive(0);
            setOpen(true);
          }}
          onKeyDown={onKeyDown}
        />
        {!open && !chosen && noneLabel ? <span className={styles["none"]}>{noneLabel}</span> : null}
        {!open && chosen ? (
          <span className={styles["chosenMeta"]}>
            {chosen.archived ? (
              <Badge size="sm" tone="attention" icon="archive">
                Archived
              </Badge>
            ) : null}
            <span className="ds-tnum">{chosen.version ? `v${chosen.version} now` : "nothing published"}</span>
          </span>
        ) : null}
        <Icon name="chevron-down" size={14} className={styles["chevron"]} aria-hidden />
      </div>
      {open ? (
        <div className={styles["popover"]}>
          {heading ? <div className={cx(styles["heading"], "ds-label")}>{heading}</div> : null}
          <ul id={listId} role="listbox" aria-label={label} className={styles["list"]}>
            {rows.length === 0 ? <li className={styles["empty"]}>No image matches.</li> : null}
            {rows.map((row, i) => (
              <li
                key={row.kind === "image" ? row.image.id : row.kind}
                id={optionId(i)}
                role="option"
                aria-selected={i === active}
                className={cx(styles["option"], i === active && styles["active"])}
                onMouseDown={(e) => {
                  e.preventDefault();
                  pick(row);
                }}
                onMouseEnter={() => setActive(i)}
              >
                {row.kind === "image" ? (
                  <>
                    <ImageMark isDefault={row.image.isDefault} />
                    <span className={styles["text"]}>
                      <span className={styles["name"]}>{highlight(row.image.name, query.trim())}</span>
                      {row.image.description ? <span className={styles["desc"]}>{highlight(row.image.description, query.trim())}</span> : null}
                    </span>
                    <span className={styles["trail"]}>
                      {row.image.isDefault ? (
                        <Badge size="sm" tone="info" emphasis="subtle">
                          default
                        </Badge>
                      ) : null}
                      <ImageStatusBadge status={row.image.status} />
                      {row.image.canRunContainers ? <CanRunContainersBadge lower /> : null}
                      <span className={cx(styles["version"], "ds-tnum")}>{row.image.version ? `v${row.image.version}` : "—"}</span>
                    </span>
                  </>
                ) : row.kind === "none" ? (
                  <>
                    <span className={styles["mark"]} aria-hidden>
                      <Icon name="arrow-down" size={14} />
                    </span>
                    <span className={styles["text"]}>
                      <span className={styles["name"]}>{allowNone}</span>
                      {noneLabel ? <span className={styles["desc"]}>{noneLabel}</span> : null}
                    </span>
                  </>
                ) : (
                  <>
                    <span className={styles["mark"]} aria-hidden>
                      <Icon name="plus" size={14} />
                    </span>
                    <span className={styles["text"]}>
                      <span className={styles["name"]}>
                        Make an image <code>FROM {row.text}</code>
                      </span>
                      <span className={styles["desc"]}>A one-line Containerfile; dude adds its layer when it builds.</span>
                    </span>
                  </>
                )}
              </li>
            ))}
          </ul>
          <div className={styles["foot"]}>
            <span className={styles["keys"]}>
              <Kbd>↑</Kbd>
              <Kbd>↓</Kbd> move <Kbd>Enter</Kbd> pick <Kbd>Esc</Kbd> close
            </span>
            {manage ? (
              <button
                type="button"
                className={styles["manage"]}
                onMouseDown={(e) => {
                  e.preventDefault();
                  close();
                  manage.onClick();
                }}
              >
                {manage.label}
              </button>
            ) : null}
          </div>
        </div>
      ) : null}
    </div>
  );
}
