import { useCallback, useRef, useState } from 'react';
import { copyText } from './clipboard';
import styles from './copyable-path.module.css';
import { titleWhenTruncated } from './truncated-title';

export interface CopyablePathProps {
  /** The filesystem path to display. */
  path: string;
  className?: string;
}

/**
 * Displays a filesystem path with a small copy-to-clipboard button. After
 * copying, the button briefly shows a checkmark before reverting.
 */
export function CopyablePath({ path, className }: CopyablePathProps) {
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
      <span className={styles.path} {...titleWhenTruncated(path)}>
        {path}
      </span>
      <button
        type="button"
        className={styles.copyButton}
        onClick={handleCopy}
        aria-label="Copy path"
        title={copied ? 'Copied!' : 'Copy path'}
      >
        {copied ? (
          <svg width="12" height="12" viewBox="0 0 16 16" fill="none" aria-hidden="true">
            <path d="M3 8.5 6.5 12 13 4" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
          </svg>
        ) : (
          <svg width="12" height="12" viewBox="0 0 16 16" fill="none" aria-hidden="true">
            <rect x="5" y="5" width="9" height="9" rx="1.5" stroke="currentColor" strokeWidth="1.5" />
            <path d="M11 5V3.5A1.5 1.5 0 0 0 9.5 2h-7A1.5 1.5 0 0 0 1 3.5v7A1.5 1.5 0 0 0 2.5 12H5" stroke="currentColor" strokeWidth="1.5" />
          </svg>
        )}
      </button>
    </span>
  );
}
