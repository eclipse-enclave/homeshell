import {
  QuickPick,
  type QuickPickItem,
  fuzzyFilter,
  fuzzyScore,
  tabStopItem,
} from '@homeshell/components';
import { useState } from 'react';

export interface PickOneProps<T extends QuickPickItem> {
  /** Accessible name of the dialog and its input. */
  label: string;
  placeholder: string;
  items: readonly T[];
  onPick: (item: T) => void;
  onClose: () => void;
}

/** A quick pick asking for one of `items`, found by its label. */
export function PickOne<T extends QuickPickItem>({
  label,
  placeholder,
  items,
  onPick,
  onClose,
}: PickOneProps<T>) {
  const [query, setQuery] = useState('');
  return (
    <QuickPick
      label={label}
      value={query}
      onValueChange={setQuery}
      items={fuzzyFilter(items, (item) => fuzzyScore(query, item.label))}
      onSelect={(item) => {
        onClose();
        onPick(item);
      }}
      onClose={onClose}
      placeholder={placeholder}
      emptyText="No matching items"
      fallbackFocus={() => tabStopItem(document)}
    />
  );
}
