import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { type BrowseResponse, FolderPicker } from './folder-picker';

function mockBrowse(response: BrowseResponse) {
  return vi.fn().mockResolvedValue(response);
}

const homeResponse: BrowseResponse = {
  path: '/home/user',
  parent: '/home',
  entries: [
    { name: 'projects', path: '/home/user/projects', isDir: true },
    { name: 'documents', path: '/home/user/documents', isDir: true },
  ],
};

describe('FolderPicker', () => {
  it('renders folder entries from the browse response', async () => {
    const fetchBrowse = mockBrowse(homeResponse);
    render(
      <FolderPicker
        onSelect={vi.fn()}
        onCancel={vi.fn()}
        fetchBrowse={fetchBrowse}
      />,
    );

    await waitFor(() => {
      expect(screen.getByText('projects')).toBeTruthy();
      expect(screen.getByText('documents')).toBeTruthy();
    });
    expect(screen.getByTestId('current-path').textContent).toBe('/home/user');
  });

  it('titles a folder name in full on hover while it is cut off', async () => {
    render(
      <FolderPicker
        onSelect={vi.fn()}
        onCancel={vi.fn()}
        fetchBrowse={mockBrowse(homeResponse)}
      />,
    );

    const name = await screen.findByText('projects');
    Object.defineProperty(name, 'scrollWidth', { value: 500 });
    fireEvent.pointerEnter(name);
    expect(name.getAttribute('title')).toBe('projects');
  });

  it('selects the current folder when Select is clicked', async () => {
    const fetchBrowse = mockBrowse(homeResponse);
    const onSelect = vi.fn();
    render(
      <FolderPicker
        onSelect={onSelect}
        onCancel={vi.fn()}
        fetchBrowse={fetchBrowse}
      />,
    );

    await waitFor(() => {
      expect(screen.getByText('projects')).toBeTruthy();
    });

    fireEvent.click(screen.getByRole('button', { name: 'Select' }));
    expect(onSelect).toHaveBeenCalledWith('/home/user');
  });

  it('navigates into a folder on click', async () => {
    const childResponse: BrowseResponse = {
      path: '/home/user/projects',
      parent: '/home/user',
      entries: [{ name: 'app', path: '/home/user/projects/app', isDir: true }],
    };
    const fetchBrowse = vi
      .fn()
      .mockResolvedValueOnce(homeResponse)
      .mockResolvedValueOnce(childResponse);

    const onSelect = vi.fn();
    render(
      <FolderPicker
        onSelect={onSelect}
        onCancel={vi.fn()}
        fetchBrowse={fetchBrowse}
      />,
    );

    await waitFor(() => {
      expect(screen.getByText('projects')).toBeTruthy();
    });

    fireEvent.click(screen.getByText('projects'));

    await waitFor(() => {
      expect(screen.getByTestId('current-path').textContent).toBe(
        '/home/user/projects',
      );
      expect(screen.getByText('app')).toBeTruthy();
    });

    fireEvent.click(screen.getByRole('button', { name: 'Select' }));
    expect(onSelect).toHaveBeenCalledWith('/home/user/projects');
  });

  it('navigates to parent folder on up button click', async () => {
    const parentResponse: BrowseResponse = {
      path: '/home',
      parent: '/',
      entries: [{ name: 'user', path: '/home/user', isDir: true }],
    };
    const fetchBrowse = vi
      .fn()
      .mockResolvedValueOnce(homeResponse)
      .mockResolvedValueOnce(parentResponse);

    render(
      <FolderPicker
        onSelect={vi.fn()}
        onCancel={vi.fn()}
        fetchBrowse={fetchBrowse}
      />,
    );

    await waitFor(() => {
      expect(
        screen.getByRole('button', { name: 'Go to parent folder' }),
      ).toBeTruthy();
    });

    fireEvent.click(
      screen.getByRole('button', { name: 'Go to parent folder' }),
    );

    await waitFor(() => {
      expect(screen.getByTestId('current-path').textContent).toBe('/home');
      expect(screen.getByText('user')).toBeTruthy();
    });
    expect(fetchBrowse).toHaveBeenCalledWith('/home', false);
  });

  it('calls onCancel when Cancel is clicked', async () => {
    const fetchBrowse = mockBrowse(homeResponse);
    const onCancel = vi.fn();
    render(
      <FolderPicker
        onSelect={vi.fn()}
        onCancel={onCancel}
        fetchBrowse={fetchBrowse}
      />,
    );

    await waitFor(() => {
      expect(screen.getByText('projects')).toBeTruthy();
    });

    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(onCancel).toHaveBeenCalled();
  });

  it('shows empty state when no subdirectories exist', async () => {
    const emptyResponse: BrowseResponse = {
      path: '/tmp/empty',
      parent: '/tmp',
      entries: [],
    };
    const fetchBrowse = mockBrowse(emptyResponse);

    render(
      <FolderPicker
        onSelect={vi.fn()}
        onCancel={vi.fn()}
        fetchBrowse={fetchBrowse}
      />,
    );

    await waitFor(() => {
      expect(screen.getByText('No subdirectories')).toBeTruthy();
    });
  });

  it('shows error when fetch fails', async () => {
    const fetchBrowse = vi
      .fn()
      .mockRejectedValue(new Error('permission denied'));

    render(
      <FolderPicker
        onSelect={vi.fn()}
        onCancel={vi.fn()}
        fetchBrowse={fetchBrowse}
      />,
    );

    await waitFor(() => {
      expect(screen.getByText('permission denied')).toBeTruthy();
    });
  });

  it('opens on the initial path', async () => {
    const fetchBrowse = mockBrowse({
      path: '/srv/typed',
      parent: '/srv',
      entries: [],
    });
    render(
      <FolderPicker
        initialPath="/srv/typed"
        onSelect={vi.fn()}
        onCancel={vi.fn()}
        fetchBrowse={fetchBrowse}
      />,
    );

    await waitFor(() =>
      expect(screen.getByTestId('current-path').textContent).toBe('/srv/typed'),
    );
    expect(fetchBrowse).toHaveBeenCalledTimes(1);
    expect(fetchBrowse).toHaveBeenCalledWith('/srv/typed', false);
  });

  it('opens at home when the daemon refuses the initial path', async () => {
    const fetchBrowse = vi
      .fn()
      .mockRejectedValueOnce(new Error('no such directory'))
      .mockResolvedValueOnce(homeResponse);
    render(
      <FolderPicker
        initialPath="/nowhere"
        onSelect={vi.fn()}
        onCancel={vi.fn()}
        fetchBrowse={fetchBrowse}
      />,
    );

    await waitFor(() =>
      expect(screen.getByTestId('current-path').textContent).toBe('/home/user'),
    );
    expect(fetchBrowse).toHaveBeenLastCalledWith(undefined, false);
    expect(screen.queryByText('no such directory')).toBeNull();
  });

  it('ignores stale navigation responses', async () => {
    const slowResponse: BrowseResponse = {
      path: '/slow',
      parent: '/',
      entries: [],
    };
    const fastResponse: BrowseResponse = {
      path: '/home',
      parent: '/',
      entries: [{ name: 'winner', path: '/home/winner', isDir: true }],
    };
    let resolveFirst: (value: BrowseResponse) => void;
    const firstPromise = new Promise<BrowseResponse>((resolve) => {
      resolveFirst = resolve;
    });
    const fetchBrowse = vi
      .fn()
      .mockResolvedValueOnce(homeResponse)
      .mockReturnValueOnce(firstPromise)
      .mockResolvedValueOnce(fastResponse);

    render(
      <FolderPicker
        onSelect={vi.fn()}
        onCancel={vi.fn()}
        fetchBrowse={fetchBrowse}
      />,
    );

    await waitFor(() => {
      expect(screen.getByText('projects')).toBeTruthy();
    });

    // First navigation (slow — will hang on firstPromise)
    fireEvent.click(screen.getByText('projects'));
    // Second navigation via the up button (always visible while browseData set)
    fireEvent.click(
      screen.getByRole('button', { name: 'Go to parent folder' }),
    );

    // Fast response resolves and should win
    await waitFor(() => {
      expect(screen.getByTestId('current-path').textContent).toBe('/home');
      expect(screen.getByText('winner')).toBeTruthy();
    });

    // Now resolve the slow first request — it should be ignored
    resolveFirst!(slowResponse);
    // Give it a tick to process
    await new Promise((resolve) => setTimeout(resolve, 10));
    expect(screen.getByTestId('current-path').textContent).toBe('/home');
  });

  it('restores focus to previously focused element on unmount', async () => {
    const fetchBrowse = mockBrowse(homeResponse);
    const trigger = document.createElement('button');
    trigger.textContent = 'Browse';
    document.body.appendChild(trigger);
    trigger.focus();

    const { unmount } = render(
      <FolderPicker
        onSelect={vi.fn()}
        onCancel={vi.fn()}
        fetchBrowse={fetchBrowse}
      />,
    );

    await waitFor(() => {
      expect(screen.getByText('projects')).toBeTruthy();
    });

    unmount();
    expect(document.activeElement).toBe(trigger);
    document.body.removeChild(trigger);
  });

  it('re-fetches with showHidden when the checkbox is toggled', async () => {
    const hiddenResponse: BrowseResponse = {
      path: '/home/user',
      parent: '/home',
      entries: [
        { name: '.config', path: '/home/user/.config', isDir: true },
        { name: 'projects', path: '/home/user/projects', isDir: true },
      ],
    };
    const fetchBrowse = vi
      .fn()
      .mockResolvedValueOnce(homeResponse)
      .mockResolvedValueOnce(hiddenResponse);

    render(
      <FolderPicker
        onSelect={vi.fn()}
        onCancel={vi.fn()}
        fetchBrowse={fetchBrowse}
      />,
    );

    await waitFor(() => {
      expect(screen.getByText('projects')).toBeTruthy();
    });

    fireEvent.click(screen.getByLabelText('Show hidden'));

    await waitFor(() => {
      expect(screen.getByText('.config')).toBeTruthy();
    });

    expect(fetchBrowse).toHaveBeenLastCalledWith('/home/user', true);
  });
});

describe('FolderPicker by keyboard', () => {
  const projects: BrowseResponse = {
    path: '/home/user/projects',
    parent: '/home/user',
    entries: [{ name: 'app', path: '/home/user/projects/app', isDir: true }],
  };
  const empty: BrowseResponse = {
    path: '/home/user/projects/app',
    parent: '/home/user/projects',
    entries: [],
  };
  /** Browses a fixed tree: /home/user holds projects, which holds app. */
  const tree = vi.fn(async (path?: string) =>
    path === projects.path
      ? projects
      : path === empty.path
        ? empty
        : homeResponse,
  );

  async function renderPicker(onSelect = vi.fn()) {
    render(
      <FolderPicker
        onSelect={onSelect}
        onCancel={vi.fn()}
        fetchBrowse={tree}
      />,
    );
    await waitFor(() => expect(document.activeElement).toBe(entry('projects')));
    return onSelect;
  }

  const entry = (name: string) =>
    screen.getByRole('button', { name: new RegExp(`${name}$`) });
  const current = () => screen.getByTestId('current-path').textContent;

  it('is modal', async () => {
    await renderPicker();
    expect(screen.getByRole('dialog').getAttribute('aria-modal')).toBe('true');
  });

  it('moves between entries with one tab stop', async () => {
    await renderPicker();
    fireEvent.keyDown(entry('projects'), { key: 'ArrowDown' });
    expect(document.activeElement).toBe(entry('documents'));
    expect(entry('documents').tabIndex).toBe(0);
    expect(entry('projects').tabIndex).toBe(-1);
    fireEvent.keyDown(entry('documents'), { key: 'Home' });
    expect(document.activeElement).toBe(entry('projects'));
    fireEvent.keyDown(entry('projects'), { key: 'End' });
    expect(document.activeElement).toBe(entry('documents'));
    fireEvent.keyDown(entry('documents'), { key: 'ArrowUp' });
    expect(document.activeElement).toBe(entry('projects'));
  });

  it.each(['Enter', 'ArrowRight'])(
    'opens the focused folder on %s, focusing its first entry',
    async (key) => {
      await renderPicker();
      fireEvent.keyDown(entry('projects'), { key });
      await waitFor(() => expect(document.activeElement).toBe(entry('app')));
      expect(current()).toBe('/home/user/projects');
    },
  );

  it('focuses the list when a folder is empty', async () => {
    await renderPicker();
    fireEvent.keyDown(entry('projects'), { key: 'Enter' });
    await waitFor(() => expect(document.activeElement).toBe(entry('app')));
    fireEvent.keyDown(entry('app'), { key: 'Enter' });
    await waitFor(() =>
      expect(document.activeElement).toBe(
        screen.getByRole('list', { name: 'Folders' }),
      ),
    );
  });

  it.each([
    ['Backspace', {}],
    ['ArrowUp', { altKey: true }],
  ])('goes to the parent on %s', async (key, modifiers) => {
    await renderPicker();
    fireEvent.keyDown(entry('projects'), { key: 'Enter' });
    await waitFor(() => expect(document.activeElement).toBe(entry('app')));
    fireEvent.keyDown(entry('app'), { key, ...modifiers });
    await waitFor(() => expect(current()).toBe('/home/user'));
    await waitFor(() => expect(document.activeElement).toBe(entry('projects')));
  });

  // Mod+Enter, as the hint says: Cmd on a Mac, Ctrl elsewhere, not either.
  it.each([
    ['Linux', 'Ctrl', { ctrlKey: true }, { metaKey: true }],
    ['Macintosh', 'Cmd', { metaKey: true }, { ctrlKey: true }],
  ])(
    'selects the folder shown on %s by %s+Enter only',
    async (platform, _modifier, modifiers, other) => {
      const userAgent = vi
        .spyOn(navigator, 'userAgent', 'get')
        .mockReturnValue(`Mozilla/5.0 (${platform})`);
      onTestFinished(() => userAgent.mockRestore());
      const onSelect = await renderPicker();
      fireEvent.keyDown(entry('projects'), { key: 'Enter', ...other });
      expect(onSelect).not.toHaveBeenCalled();
      fireEvent.keyDown(entry('projects'), { key: 'Enter', ...modifiers });
      expect(onSelect).toHaveBeenCalledWith('/home/user');
    },
  );

  it('keeps focus in the dialog while a folder loads', async () => {
    const onCancel = vi.fn();
    const fetchBrowse = vi
      .fn()
      .mockResolvedValueOnce(homeResponse)
      .mockReturnValue(new Promise(() => undefined));
    render(
      <FolderPicker
        onSelect={vi.fn()}
        onCancel={onCancel}
        fetchBrowse={fetchBrowse}
      />,
    );
    await waitFor(() => expect(document.activeElement).toBe(entry('projects')));

    fireEvent.keyDown(entry('projects'), { key: 'Enter' });
    expect(screen.getByText('Loading…')).toBeTruthy();
    expect(document.activeElement).toBe(
      screen.getByRole('list', { name: 'Folders' }),
    );
    fireEvent.keyDown(document.activeElement as HTMLElement, {
      key: 'Escape',
    });
    expect(onCancel).toHaveBeenCalled();
  });

  it('leaves focus on Show hidden when it re-lists the folder', async () => {
    await renderPicker();
    const checkbox = screen.getByLabelText('Show hidden');
    checkbox.focus();
    fireEvent.click(checkbox);
    await waitFor(() =>
      expect(tree).toHaveBeenLastCalledWith('/home/user', true),
    );
    await waitFor(() => expect(entry('projects')).toBeTruthy());
    expect(document.activeElement).toBe(checkbox);
  });

  it('keeps its keys from the elements around it', async () => {
    const onKeyDown = vi.fn();
    render(
      <div onKeyDown={onKeyDown}>
        <FolderPicker
          onSelect={vi.fn()}
          onCancel={vi.fn()}
          fetchBrowse={tree}
        />
      </div>,
    );
    await screen.findByTestId('current-path');
    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' });
    expect(onKeyDown).not.toHaveBeenCalled();
  });

  it('names the keys that open, go up and select in a hint line', async () => {
    render(
      <FolderPicker
        onSelect={vi.fn()}
        onCancel={vi.fn()}
        fetchBrowse={mockBrowse(homeResponse)}
      />,
    );

    await screen.findByText('projects');
    expect(screen.getByText('Enter').parentElement?.textContent).toBe(
      'Enter open',
    );
    expect(screen.getByText('Backspace').parentElement?.textContent).toBe(
      'Backspace up',
    );
    expect(screen.getByText('Ctrl+Enter').parentElement?.textContent).toBe(
      'Ctrl+Enter select',
    );
  });
});
