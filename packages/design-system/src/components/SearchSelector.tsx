import { useCallback, useState, type ReactNode } from "react";
import * as Popover from "@radix-ui/react-popover";
import { Icon } from "../icons/index.tsx";
import { SearchPicker } from "./SearchPicker.tsx";
import styles from "./SearchSelector.module.css";

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

export function SearchSelector<T>({ title, label, options, optionKey, searchText, renderOption, onPick, current, group, placeholder, optionAttributes, listTestId, empty = "Nothing to select yet" }: SearchSelectorProps<T>) {
  const [open, setOpen] = useState(false);
  const find = useCallback(async (query: string) => {
    const words = query.toLocaleLowerCase().split(/\s+/).filter(Boolean);
    return options.filter((option) => words.every((word) => searchText(option).toLocaleLowerCase().includes(word)));
  }, [options, searchText]);
  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Trigger className={styles["trigger"]} aria-label={label}>
        {title}<Icon name="chevron-down" size={14} />
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content className={styles["popup"]} side="bottom" align="start" sideOffset={16} collisionPadding={16} aria-label={label} data-testid="search-selector">
          <SearchPicker find={find} optionKey={optionKey} renderOption={renderOption} label={label} placeholder={placeholder}
            findOnEmpty delay={0} autoFocus group={group} optionCurrent={current} escapeClosesPicker optionAttributes={optionAttributes} listTestId={listTestId}
            empty={(query) => query ? "No matching options" : empty} onCancel={() => setOpen(false)}
            onPick={(option) => { setOpen(false); onPick(option); }} />
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
