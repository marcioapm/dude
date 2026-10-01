import { useEffect, useId, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import { Badge } from "../primitives/Badge.tsx";
import { Input } from "../primitives/Input.tsx";
import styles from "./SearchPicker.module.css";

/*
 * A combobox: type, and pick one of what the app finds for the words. The
 * field is the combobox (`aria-expanded`, `aria-controls`,
 * `aria-activedescendant`); the options a listbox under it, in the menu's
 * row grammar. ↑↓ move, Enter picks, Escape closes (and asks the app to
 * cancel when nothing is open). The app does the finding, as Select's
 * comment asks: `find` is called for the words after a pause, and only its
 * latest answer lands.
 *
 * Opt-in, for picking several: suggestions before any words (`findOnEmpty`
 * asks `find("")`), headed groups (`group`), an option shown but never
 * picked, saying why (`optionDisabled`), and the field emptied after a pick
 * (`clearOnPick`). The picks themselves are the app's — a `RemovableList`
 * above the field — with Backspace in an empty field dropping the last
 * (`onBackspaceEmpty`) and ⌘/Ctrl+Enter doing what they were for (`onSubmit`).
 */

export interface SearchPickerProps<T> {
  /** The options for the words typed; "" for suggestions, with `findOnEmpty`. */
  readonly find: (query: string) => Promise<ReadonlyArray<T>>;
  readonly optionKey: (option: T) => string;
  readonly renderOption: (option: T) => ReactNode;
  readonly onPick: (option: T) => void;
  /** Escape with nothing open. */
  readonly onCancel?: (() => void) | undefined;
  readonly placeholder?: string | undefined;
  readonly label: string;
  readonly autoFocus?: boolean | undefined;
  /** Milliseconds to wait after the last key before finding. */
  readonly delay?: number | undefined;
  readonly size?: "sm" | "md" | undefined;
  /** Ask `find("")` before any words: suggestions. */
  readonly findOnEmpty?: boolean | undefined;
  /** An option's group: a heading starts each run of one group, in the order found. */
  readonly group?: ((option: T) => string) | undefined;
  /** A group's heading; the group's name by default. */
  readonly renderGroup?: ((group: string) => ReactNode) | undefined;
  /** Why an option is shown but cannot be picked, or nothing when it can. */
  readonly optionDisabled?: ((option: T) => string | null | undefined) | undefined;
  /** Empty the field after a pick, to find the next. */
  readonly clearOnPick?: boolean | undefined;
  /** Words found nothing: what to say. Without it, the list just closes. */
  readonly empty?: ((query: string) => ReactNode) | undefined;
  /** Backspace in an empty field. */
  readonly onBackspaceEmpty?: (() => void) | undefined;
  /** ⌘/Ctrl+Enter. */
  readonly onSubmit?: (() => void) | undefined;
  /** Find again for the same words when this changes: the app's picks changed what it offers. */
  readonly version?: string | number | undefined;
}

export function SearchPicker<T>({
  find, optionKey, renderOption, onPick, onCancel, placeholder, label, autoFocus, delay = 200, size,
  findOnEmpty, group, renderGroup, optionDisabled, clearOnPick, empty, onBackspaceEmpty, onSubmit, version,
}: SearchPickerProps<T>) {
  const listId = useId();
  const [query, setQuery] = useState("");
  const [options, setOptions] = useState<ReadonlyArray<T>>([]);
  // The words the options answer: an empty answer to words is "nothing found".
  const [answered, setAnswered] = useState<string | null>(null);
  const [active, setActive] = useState(0);
  // Escape closed the list; typing or an arrow opens it again.
  const [closed, setClosed] = useState(false);
  const latest = useRef(0);
  const disabled = useRef(optionDisabled);
  disabled.current = optionDisabled;
  const why = (o: T) => disabled.current?.(o) || null;

  useEffect(() => {
    const words = query.trim();
    const mine = ++latest.current;
    if (!words && !findOnEmpty) {
      setOptions([]);
      setAnswered(null);
      return;
    }
    const t = setTimeout(() => {
      find(words).then(
        (found) => {
          if (mine !== latest.current) return;
          setOptions(found);
          setAnswered(words);
          setActive(Math.max(0, found.findIndex((o) => !why(o))));
        },
        () => {
          if (mine !== latest.current) return;
          setOptions([]);
          setAnswered(null);
        },
      );
    }, words ? delay : 0);
    return () => clearTimeout(t);
  }, [query, find, delay, findOnEmpty, version]);

  const open = !closed && options.length > 0;
  const nothing = !closed && !open && !!answered && empty !== undefined;
  const optionId = (i: number) => `${listId}-${i}`;
  // The next option that can be picked, from `from`, `by` rows at a time.
  const step = (from: number, by: 1 | -1) => {
    for (let i = from, n = 0; n < options.length; n++) {
      i = (i + by + options.length) % options.length;
      if (!why(options[i]!)) return i;
    }
    return from;
  };
  const pick = (o: T) => {
    if (why(o)) return;
    onPick(o);
    if (clearOnPick) setQuery("");
  };

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if ((e.key === "ArrowDown" || e.key === "ArrowUp") && (open || closed)) {
      e.preventDefault();
      if (closed) setClosed(false);
      else setActive((i) => step(i, e.key === "ArrowDown" ? 1 : -1));
    } else if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && onSubmit) {
      e.preventDefault();
      onSubmit();
    } else if (e.key === "Enter") {
      // Enter picks, or does nothing: a field that finds is never a form's
      // submit (a settings page saving on half-typed words).
      e.preventDefault();
      if (open) pick(options[active]!);
    } else if (e.key === "Backspace" && query === "" && onBackspaceEmpty) {
      onBackspaceEmpty();
    } else if (e.key === "Escape") {
      // Inside a dialog: close the list first, the picker next, the dialog last.
      e.stopPropagation();
      if (open || nothing) {
        e.preventDefault();
        setClosed(true);
      } else {
        onCancel?.();
      }
    }
  };

  const rows: ReactNode[] = [];
  options.forEach((o, i) => {
    const g = group?.(o);
    if (g !== undefined && (i === 0 || group!(options[i - 1]!) !== g)) {
      rows.push(<li key={`group:${g}`} role="presentation" className={styles["group"]}>{renderGroup ? renderGroup(g) : g}</li>);
    }
    const off = why(o);
    rows.push(
      <li
        key={optionKey(o)}
        id={optionId(i)}
        role="option"
        aria-selected={i === active}
        aria-disabled={off ? true : undefined}
        className={cx(styles["option"], i === active && !off && styles["active"], off && styles["disabled"])}
        // mousedown, not click: the field keeps its focus.
        onMouseDown={(e) => {
          e.preventDefault();
          pick(o);
        }}
        onMouseEnter={() => !off && setActive(i)}
      >
        {renderOption(o)}
        {off ? <Badge size="sm" className={styles["why"]}>{off}</Badge> : null}
      </li>,
    );
  });

  return (
    <div className={styles["root"]}>
      <Input
        role="combobox"
        size={size}
        aria-label={label}
        aria-expanded={open}
        aria-controls={listId}
        aria-autocomplete="list"
        aria-activedescendant={open ? optionId(active) : undefined}
        autoFocus={autoFocus}
        leading={<Icon name="search" size={14} />}
        placeholder={placeholder}
        value={query}
        onChange={(e) => {
          setQuery(e.target.value);
          setClosed(false);
        }}
        onKeyDown={onKeyDown}
      />
      <ul id={listId} role="listbox" aria-label={label} className={styles["list"]} hidden={!open && !nothing}>
        {open ? rows : nothing ? <li role="presentation" className={styles["empty"]}>{empty(answered)}</li> : null}
      </ul>
    </div>
  );
}
