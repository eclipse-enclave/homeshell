import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import type { UntrackedWorktree } from '@homeshell/model';
import {
  UntrackedWorktrees,
  type UntrackedWorktreesProps,
} from './untracked-worktrees';

const feature: UntrackedWorktree = {
  name: 'feature',
  branch: 'feature',
  path: '/work/feature',
  sessionCount: 2,
};
const loose: UntrackedWorktree = {
  name: 'loose',
  path: '/work/loose',
  sessionCount: 0,
};

const expand = (label: RegExp) =>
  fireEvent.click(screen.getByRole('button', { name: label }));

describe('UntrackedWorktrees', () => {
  it('renders nothing when there are none', () => {
    const { container } = render(
      <UntrackedWorktrees worktrees={[]} onAdd={vi.fn()} />,
    );
    expect(container.childElementCount).toBe(0);
  });

  it('is collapsed by default and says how many', () => {
    const { rerender } = render(
      <UntrackedWorktrees worktrees={[feature, loose]} onAdd={vi.fn()} />,
    );
    const fold = screen.getByRole('button', { name: '2 untracked worktrees' });
    expect(fold.getAttribute('aria-expanded')).toBe('false');
    expect(screen.queryByRole('button', { name: /^Add/ })).toBeNull();

    rerender(<UntrackedWorktrees worktrees={[feature]} onAdd={vi.fn()} />);
    expect(
      screen.getByRole('button', { name: '1 untracked worktree' }),
    ).toBeTruthy();
  });

  it('expanded, lists each with its branch, folder and sessions', () => {
    render(
      <UntrackedWorktrees
        worktrees={[
          feature,
          loose,
          { ...feature, path: '/w/s', sessionCount: 1 },
        ]}
        onAdd={vi.fn()}
      />,
    );
    expand(/untracked worktrees/);
    const items = screen.getAllByRole('listitem');
    expect(items).toHaveLength(3);
    expect(items[0].textContent).toContain('/work/feature');
    // Named after its branch, it does not show the branch twice.
    expect(items[0].textContent?.match(/feature/g)).toHaveLength(2);
    expect(items[0].textContent).toContain('2 sessions');
    expect(items[1].textContent).toContain('detached');
    expect(items[1].textContent).not.toMatch(/session/);
    expect(items[2].textContent).toContain('1 session');
    expect(items[2].textContent).not.toContain('1 sessions');
  });

  it("Add hands the folder's path to onAdd", () => {
    const onAdd = vi.fn();
    render(<UntrackedWorktrees worktrees={[feature, loose]} onAdd={onAdd} />);
    expand(/untracked worktrees/);
    fireEvent.click(screen.getByRole('button', { name: 'Add loose' }));
    expect(onAdd).toHaveBeenCalledWith('/work/loose');
  });

  it('describes each Add by its folder, so same-named worktrees differ', () => {
    const onAdd = vi.fn();
    const twin = (path: string): UntrackedWorktree => ({
      name: 'wt',
      path,
      sessionCount: 0,
    });
    render(
      <UntrackedWorktrees
        worktrees={[twin('/a/wt'), twin('/b/wt')]}
        onAdd={onAdd}
      />,
    );
    expand(/untracked worktrees/);
    fireEvent.click(
      screen.getByRole('button', { name: 'Add wt', description: '/b/wt' }),
    );
    expect(onAdd).toHaveBeenCalledWith('/b/wt');
  });

  it.each([
    ['an Add is in flight', { adding: '/work/loose' }, 'Adding a worktree'],
    ['there is a reason', { disabledReason: 'Offline' }, 'Offline'],
  ])('no Add is made while %s', (_case, props, title) => {
    const onAdd = vi.fn();
    render(
      <UntrackedWorktrees
        worktrees={[feature, loose]}
        onAdd={onAdd}
        {...props}
      />,
    );
    expand(/untracked worktrees/);
    for (const name of ['Add loose', 'Add feature']) {
      const button = screen.getByRole('button', { name });
      expect(button.getAttribute('title')).toBe(title);
      fireEvent.click(button);
    }
    expect(onAdd).not.toHaveBeenCalled();
  });

  it('marks the Add in flight busy', () => {
    render(
      <UntrackedWorktrees
        worktrees={[feature, loose]}
        onAdd={vi.fn()}
        adding="/work/loose"
      />,
    );
    expand(/untracked worktrees/);
    expect(
      screen
        .getByRole('button', { name: 'Add loose' })
        .getAttribute('aria-busy'),
    ).toBe('true');
    expect(
      screen
        .getByRole('button', { name: 'Add feature' })
        .getAttribute('aria-busy'),
    ).not.toBe('true');
  });

  // The fold and its rows are items of the workstream list's arrow-key tree.
  describe('from the keyboard', () => {
    const renderFold = (props: Partial<UntrackedWorktreesProps> = {}) => {
      const onAdd = vi.fn();
      render(
        <UntrackedWorktrees
          worktrees={[feature, loose]}
          onAdd={onAdd}
          {...props}
        />,
      );
      return {
        onAdd,
        fold: screen.getByRole('button', { name: '2 untracked worktrees' }),
      };
    };
    const row = (name: string) =>
      screen.getAllByRole('listitem').find((item) => item.textContent?.startsWith(name))!;

    it('Right opens the fold, then goes to its first row; Left goes back', () => {
      const { fold } = renderFold();
      fireEvent.keyDown(fold, { key: 'ArrowRight' });
      expect(fold.getAttribute('aria-expanded')).toBe('true');

      fireEvent.keyDown(fold, { key: 'ArrowRight' });
      expect(document.activeElement).toBe(row('feature'));

      fireEvent.keyDown(row('feature'), { key: 'ArrowLeft' });
      expect(document.activeElement).toBe(fold);
      fireEvent.keyDown(fold, { key: 'ArrowLeft' });
      expect(fold.getAttribute('aria-expanded')).toBe('false');
    });

    it('Enter on a row adds it', () => {
      const { onAdd, fold } = renderFold();
      fireEvent.keyDown(fold, { key: 'ArrowRight' });
      expect(row('loose').getAttribute('aria-keyshortcuts')).toContain('Enter');
      fireEvent.keyDown(row('loose'), { key: 'Enter' });
      expect(onAdd).toHaveBeenCalledWith('/work/loose');
    });

    it.each([
      ['the fold', 'untracked-worktrees', [0, -1, -1], [-1, -1]],
      ['a row', 'untracked:/work/loose', [-1, -1, 0], [-1, undefined]],
      ['a card', 'w1', [-1, -1, -1], [-1, -1]],
    ])(
      'gives Tab only to %s holding the tab stop, and Add only from its row',
      (_holder, tabStopId, items, adds) => {
        const { fold } = renderFold({ tabStopId });
        fireEvent.keyDown(fold, { key: 'ArrowRight' });
        expect(
          [fold, row('feature'), row('loose')].map((item) => item.tabIndex),
        ).toEqual(items);
        expect(
          ['Add feature', 'Add loose'].map(
            (name) =>
              screen.getByRole('button', { name }).getAttribute('tabindex') ??
              undefined,
          ),
        ).toEqual(adds.map((index) => index?.toString()));
      },
    );

    it('folded, gives the tab stop of a row to the fold', () => {
      const { fold } = renderFold({ tabStopId: 'untracked:/work/loose' });
      expect(fold.tabIndex).toBe(0);
    });

    it('Enter does nothing while an Add is in flight', () => {
      const { onAdd, fold } = renderFold({ adding: '/work/feature' });
      fireEvent.keyDown(fold, { key: 'ArrowRight' });
      fireEvent.keyDown(row('loose'), { key: 'Enter' });
      expect(onAdd).not.toHaveBeenCalled();
    });
  });
});
