/**
 * The one line-icon set (Lucide). Components import icons only from here. Each
 * is drawn with `strokeWidth={ICON_STROKE}` and `aria-hidden`, so a call site
 * passes only `size`; the `Icon` suffix keeps the names clear of domain types
 * such as `Settings` in the barrel.
 */
import type { LucideIcon, LucideProps } from 'lucide-react';
import {
  Check,
  ChevronDown,
  Copy,
  Ellipsis,
  ExternalLink,
  Folder,
  FolderOpen,
  FolderTree,
  GitBranch,
  GitFork,
  Moon,
  Play,
  Plus,
  Settings,
  Square,
  SquareTerminal,
  StickyNote,
  Sun,
  Trash2,
} from 'lucide-react';
import { createElement } from 'react';

/** An icon from this module: a component taking Lucide's props, `size` mostly. */
type Icon = (props: LucideProps) => ReturnType<typeof createElement>;

/** Stroke width every icon is drawn with. */
const ICON_STROKE = 1.75;

function withDefaults(icon: LucideIcon): Icon {
  return (props: LucideProps) =>
    createElement(icon, {
      strokeWidth: ICON_STROKE,
      'aria-hidden': true,
      ...props,
    });
}

// Pure, so a bundler drops the wrappers (and their Lucide icon) nobody uses.
export const CheckIcon = /* @__PURE__ */ withDefaults(Check);
export const ChevronDownIcon = /* @__PURE__ */ withDefaults(ChevronDown);
export const CopyIcon = /* @__PURE__ */ withDefaults(Copy);
export const EllipsisIcon = /* @__PURE__ */ withDefaults(Ellipsis);
export const ExternalLinkIcon = /* @__PURE__ */ withDefaults(ExternalLink);
export const FolderIcon = /* @__PURE__ */ withDefaults(Folder);
export const FolderOpenIcon = /* @__PURE__ */ withDefaults(FolderOpen);
export const FolderTreeIcon = /* @__PURE__ */ withDefaults(FolderTree);
export const GitBranchIcon = /* @__PURE__ */ withDefaults(GitBranch);
export const GitForkIcon = /* @__PURE__ */ withDefaults(GitFork);
export const MoonIcon = /* @__PURE__ */ withDefaults(Moon);
export const PlayIcon = /* @__PURE__ */ withDefaults(Play);
export const PlusIcon = /* @__PURE__ */ withDefaults(Plus);
export const SettingsIcon = /* @__PURE__ */ withDefaults(Settings);
export const SquareIcon = /* @__PURE__ */ withDefaults(Square);
export const StickyNoteIcon = /* @__PURE__ */ withDefaults(StickyNote);
export const SunIcon = /* @__PURE__ */ withDefaults(Sun);
export const TerminalIcon = /* @__PURE__ */ withDefaults(SquareTerminal);
export const TrashIcon = /* @__PURE__ */ withDefaults(Trash2);
