import { afterEach, describe, expect, it, vi } from 'vitest';
import { copyText } from './clipboard';

/**
 * Replaces navigator.clipboard for one test. `undefined` removes it entirely,
 * which is what the desktop shell's webview looks like.
 */
function withClipboard(writeText: ((text: string) => Promise<void>) | undefined) {
  const original = Object.getOwnPropertyDescriptor(navigator, 'clipboard');
  Object.defineProperty(navigator, 'clipboard', {
    value: writeText ? { writeText } : undefined,
    configurable: true,
  });
  return () => {
    if (original) Object.defineProperty(navigator, 'clipboard', original);
    else Reflect.deleteProperty(navigator as object, 'clipboard');
  };
}

/** Stands in for the document copy the fallback path relies on. */
function stubExecCommand(result: boolean | (() => boolean)) {
  const copied: string[] = [];
  const exec = vi.fn(() => {
    // The fallback must have put the text in a selected textarea by now.
    const textarea = document.querySelector('textarea');
    if (textarea) copied.push(textarea.value);
    return typeof result === 'function' ? result() : result;
  });
  (document as unknown as { execCommand: () => boolean }).execCommand = exec;
  return { exec, copied };
}

const restorers: Array<() => void> = [];

afterEach(() => {
  while (restorers.length) restorers.pop()?.();
  vi.restoreAllMocks();
});

describe('copyText', () => {
  it('uses the Clipboard API when the browser has one', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    restorers.push(withClipboard(writeText));
    const { exec } = stubExecCommand(true);

    await expect(copyText('/home/user/project')).resolves.toBe(true);

    expect(writeText).toHaveBeenCalledWith('/home/user/project');
    expect(exec).not.toHaveBeenCalled();
  });

  // The desktop shell's WebKitGTK webview exposes no navigator.clipboard at
  // all, even on the daemon's loopback origin. This is the case that used to
  // throw and leave the copy button doing nothing.
  it('falls back to execCommand when there is no Clipboard API', async () => {
    restorers.push(withClipboard(undefined));
    const { exec, copied } = stubExecCommand(true);

    await expect(copyText('/home/user/project')).resolves.toBe(true);

    expect(exec).toHaveBeenCalledWith('copy');
    expect(copied).toEqual(['/home/user/project']);
  });

  it('falls back when the Clipboard API rejects', async () => {
    const writeText = vi.fn().mockRejectedValue(new Error('NotAllowedError'));
    restorers.push(withClipboard(writeText));
    const { exec, copied } = stubExecCommand(true);

    await expect(copyText('line one\nline two')).resolves.toBe(true);

    expect(writeText).toHaveBeenCalled();
    expect(copied).toEqual(['line one\nline two']);
    expect(exec).toHaveBeenCalledWith('copy');
  });

  it('reports failure when neither route works', async () => {
    restorers.push(withClipboard(undefined));
    stubExecCommand(false);

    await expect(copyText('nope')).resolves.toBe(false);
  });

  it('reports failure when execCommand throws', async () => {
    restorers.push(withClipboard(undefined));
    stubExecCommand(() => {
      throw new Error('not supported');
    });

    await expect(copyText('nope')).resolves.toBe(false);
  });

  // The textarea is an implementation detail; leaving one behind would stack up
  // one per copy and, being focusable, break tab order.
  it('leaves no textarea behind', async () => {
    restorers.push(withClipboard(undefined));
    stubExecCommand(true);

    await copyText('one');
    await copyText('two');

    expect(document.querySelectorAll('textarea')).toHaveLength(0);
  });
});
