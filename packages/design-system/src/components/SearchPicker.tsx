import { useEffect, useId, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
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
 */

export interface SearchPickerProps<T> {
  /** The options for the words typed. */
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
}

export function SearchPicker<T>({ find, optionKey, renderOption, onPick, onCancel, placeholder, label, autoFocus, delay = 200 }: SearchPickerProps<T>) {
  const listId = useId();
  const [query, setQuery] = useState("");
  const [options, setOptions] = useState<ReadonlyArray<T>>([]);
  const [active, setActive] = useState(0);
  const latest = useRef(0);

  useEffect(() => {
    const words = query.trim();
    const mine = ++latest.current;
    if (!words) {
      setOptions([]);
      return;
    }
    const t = setTimeout(() => {
      find(words).then(
        (found) => {
          if (mine !== latest.current) return;
          setOptions(found);
          setActive(0);
        },
        () => mine === latest.current && setOptions([]),
      );
    }, delay);
    return () => clearTimeout(t);
  }, [query, find, delay]);

  const open = options.length > 0;
  const optionId = (i: number) => `${listId}-${i}`;

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "ArrowDown" && open) {
      e.preventDefault();
      setActive((i) => (i + 1) % options.length);
    } else if (e.key === "ArrowUp" && open) {
      e.preventDefault();
      setActive((i) => (i - 1 + options.length) % options.length);
    } else if (e.key === "Enter" && open) {
      e.preventDefault();
      onPick(options[active]!);
    } else if (e.key === "Escape") {
      // Inside a dialog: close the list first, the picker next, the dialog last.
      e.stopPropagation();
      if (open) {
        e.preventDefault();
        setOptions([]);
      } else {
        onCancel?.();
      }
    }
  };

  return (
    <div className={styles["root"]}>
      <Input
        size="sm"
        role="combobox"
        aria-label={label}
        aria-expanded={open}
        aria-controls={listId}
        aria-autocomplete="list"
        aria-activedescendant={open ? optionId(active) : undefined}
        autoFocus={autoFocus}
        leading={<Icon name="search" size={14} />}
        placeholder={placeholder}
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        onKeyDown={onKeyDown}
      />
      <ul id={listId} role="listbox" aria-label={label} className={styles["list"]} hidden={!open}>
        {options.map((o, i) => (
          <li
            key={optionKey(o)}
            id={optionId(i)}
            role="option"
            aria-selected={i === active}
            className={cx(styles["option"], i === active && styles["active"])}
            // mousedown, not click: the field keeps its focus.
            onMouseDown={(e) => {
              e.preventDefault();
              onPick(o);
            }}
            onMouseEnter={() => setActive(i)}
          >
            {renderOption(o)}
          </li>
        ))}
      </ul>
    </div>
  );
}
