import { createContext, useCallback, useContext, useState, type ReactNode, type RefObject } from "react";
import * as Popover from "@radix-ui/react-popover";
import { Icon } from "../icons/index.tsx";
import { SearchPicker } from "./SearchPicker.tsx";
import styles from "./SearchSelector.module.css";

/**
 * The element a SearchSelector's popup hangs below instead of its own title:
 * a header provides itself so the list opens under the whole header and
 * leaves the identity lines beneath the title visible.
 */
export const SearchSelectorAnchor = createContext<RefObject<HTMLElement | null> | null>(null);

export interface SearchSelectorProps<T> {
  readonly title: ReactNode;
  readonly label: string;
  readonly options: ReadonlyArray<T>;
  readonly optionKey: (option: T) => string;
  readonly searchText: (option: T) => string;
  readonly renderOption: (option: T) => ReactNode;
  readonly onPick: (option: T) => void;
  readonly current?: ((option: T) => boolean) | undefined;
  readonly group?: ((option: T) => string) | undefined;
  readonly optionAttributes?: ((option: T) => Record<string, string | undefined>) | undefined;
  readonly listTestId?: string | undefined;
  readonly placeholder?: string | undefined;
  readonly empty?: ReactNode;
}

const TITLE_GAP = 16;

export function SearchSelector<T>({ title, label, options, optionKey, searchText, renderOption, onPick, current, group, placeholder, optionAttributes, listTestId, empty = "Nothing to select yet" }: SearchSelectorProps<T>) {
  const [open, setOpen] = useState(false);
  // Radix positions against the trigger; these offsets move the popup to the anchor's bottom-left edge.
  const [offsets, setOffsets] = useState({ side: TITLE_GAP, align: 0 });
  const anchor = useContext(SearchSelectorAnchor);
  const [trigger, setTrigger] = useState<HTMLButtonElement | null>(null);
  const find = useCallback(async (query: string) => {
    const words = query.toLocaleLowerCase().split(/\s+/).filter(Boolean);
    return options.filter((option) => words.every((word) => searchText(option).toLocaleLowerCase().includes(word)));
  }, [options, searchText]);
  const openChange = (next: boolean) => {
    const box = anchor?.current?.getBoundingClientRect();
    const own = trigger?.getBoundingClientRect();
    if (next && box && own) setOffsets({ side: Math.max(0, box.bottom - own.bottom), align: box.left - own.left });
    setOpen(next);
  };
  return (
    <Popover.Root open={open} onOpenChange={openChange}>
      <Popover.Trigger ref={setTrigger} className={styles["trigger"]} aria-label={label}>
        {title}<Icon name="chevron-down" size={14} />
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content className={styles["popup"]} side="bottom" align="start" sideOffset={offsets.side} alignOffset={offsets.align}
          collisionPadding={16} aria-label={label} data-testid="search-selector">
          <SearchPicker find={find} optionKey={optionKey} renderOption={renderOption} label={label} placeholder={placeholder}
            findOnEmpty delay={0} autoFocus group={group} optionCurrent={current} escapeClosesPicker optionAttributes={optionAttributes} listTestId={listTestId}
            empty={options.length ? () => "No matching options" : undefined} onCancel={() => setOpen(false)}
            onPick={(option) => { setOpen(false); onPick(option); }} />
          {options.length ? null : <p className={styles["empty"]}>{empty}</p>}
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
