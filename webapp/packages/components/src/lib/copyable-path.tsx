import { useCallback, useRef, useState } from 'react';
import { copyText } from './clipboard';
import { CheckIcon, CopyIcon } from './icons';
import styles from './copyable-path.module.css';
import { titleWhenTruncated } from './truncated-title';

export interface CopyablePathProps {
  /** The filesystem path to copy, and to display unless `label` is given. */
  path: string;
  /** A shorter form of the path to display; its title is the full path. */
  label?: string;
  className?: string;
  /** For its copy button: -1 takes it out of the Tab order. */
  tabIndex?: number;
}

/**
 * Displays a filesystem path with a small copy-to-clipboard button. After
 * copying, the button briefly shows a checkmark before reverting.
 */
export function CopyablePath({
  path,
  label,
  className,
  tabIndex,
}: CopyablePathProps) {
  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined);

  // copyText, not navigator.clipboard directly: the desktop shell's webview has
  // no Clipboard API, so a direct call throws there and the button does nothing.
  const handleCopy = useCallback(() => {
    copyText(path).then((copiedOk) => {
      if (!copiedOk) return;
      setCopied(true);
      clearTimeout(timer.current);
      timer.current = setTimeout(() => setCopied(false), 1500);
    });
  }, [path]);

  return (
    <span className={`${styles.wrapper} ${className ?? ''}`}>
      <span
        className={styles.path}
        {...(label ? { title: path } : titleWhenTruncated(path))}
      >
        {label ?? path}
      </span>
      <button
        type="button"
        className={styles.copyButton}
        onClick={handleCopy}
        tabIndex={tabIndex}
        aria-label="Copy path"
        title={copied ? 'Copied!' : 'Copy path'}
      >
        {copied ? <CheckIcon size={12} /> : <CopyIcon size={12} />}
      </button>
    </span>
  );
}
