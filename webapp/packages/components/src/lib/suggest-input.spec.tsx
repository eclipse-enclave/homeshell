import { fireEvent, render, screen } from '@testing-library/react';
import { useState } from 'react';

import { SuggestInput } from './suggest-input';

const suggestions = ['main', 'feature/login', 'origin/main', 'v1.0'];

function Harness({
  initial = '',
  onFormEscape = vi.fn(),
  offered = suggestions,
}: {
  initial?: string;
  onFormEscape?: () => void;
  offered?: string[];
}) {
  const [value, setValue] = useState(initial);
  return (
    <form
      onKeyDown={(event) => {
        if (event.key === 'Escape') onFormEscape();
      }}
    >
      <SuggestInput
        label="Base ref"
        value={value}
        onChange={setValue}
        suggestions={offered}
        disabled={false}
      />
    </form>
  );
}

const input = () => screen.getByRole('combobox', { name: 'Base ref' });
const options = () =>
  screen.queryAllByRole('option').map((option) => option.textContent);
const highlighted = () =>
  screen
    .queryAllByRole('option')
    .find((option) => option.getAttribute('aria-selected') === 'true')
    ?.textContent;
const press = (key: string, init: Record<string, unknown> = {}) =>
  fireEvent.keyDown(input(), { key, ...init });

describe('SuggestInput', () => {
  it('starts closed, a combobox that owns its listbox', () => {
    render(<Harness />);
    expect(input().getAttribute('aria-expanded')).toBe('false');
    expect(input().getAttribute('aria-controls')).toBe(
      screen.getByRole('listbox', { hidden: true }).id,
    );
    expect(options()).toEqual([]);
  });

  it('lists every suggestion on Down, highlighting the current value', () => {
    render(<Harness initial="origin/main" />);
    press('ArrowDown');
    expect(input().getAttribute('aria-expanded')).toBe('true');
    expect(options()).toEqual(suggestions);
    expect(highlighted()).toBe('origin/main');
    expect(input().getAttribute('aria-activedescendant')).toBe(
      screen.getByRole('option', { name: 'origin/main' }).id,
    );
  });

  it('scrolls to the highlight on reopening at the same one', () => {
    const scrolled: (string | null)[] = [];
    const spy = vi
      .spyOn(Element.prototype, 'scrollIntoView')
      .mockImplementation(function (this: Element) {
        scrolled.push(this.textContent);
      });
    render(<Harness initial="v1.0" />);
    press('ArrowDown');
    press('Escape');
    scrolled.length = 0;
    press('ArrowDown');
    expect(scrolled).toEqual(['v1.0']);
    spy.mockRestore();
  });

  it('moves the highlight with the arrows and picks it on Enter', () => {
    render(<Harness />);
    press('ArrowDown');
    expect(highlighted()).toBeUndefined();
    press('ArrowDown');
    press('ArrowDown');
    expect(highlighted()).toBe('feature/login');
    press('ArrowUp');
    press('ArrowUp');
    expect(highlighted()).toBe('v1.0');
    expect(press('Enter')).toBe(false);
    expect(input()).toHaveProperty('value', 'v1.0');
    expect(options()).toEqual([]);
  });

  it('lists the matching suggestions as you type, best first', () => {
    render(<Harness />);
    fireEvent.change(input(), { target: { value: 'main' } });
    expect(options()).toEqual(['main', 'origin/main']);
    expect(highlighted()).toBeUndefined();
  });

  it('keeps free text: Enter with nothing highlighted is the form’s', () => {
    render(<Harness />);
    fireEvent.change(input(), { target: { value: 'mai' } });
    expect(press('Enter')).toBe(true);
    expect(input()).toHaveProperty('value', 'mai');
  });

  it('picks a suggestion on click, keeping focus', () => {
    render(<Harness />);
    input().focus();
    press('ArrowDown');
    const option = screen.getByRole('option', { name: 'v1.0' });
    expect(fireEvent.mouseDown(option)).toBe(false);
    fireEvent.click(option);
    expect(input()).toHaveProperty('value', 'v1.0');
    expect(document.activeElement).toBe(input());
  });

  it('keeps focus on a click anywhere in the list', () => {
    render(<Harness />);
    press('ArrowDown');
    expect(fireEvent.mouseDown(screen.getByRole('listbox'))).toBe(false);
  });

  it('lists every suggestion when the input is clicked', () => {
    render(<Harness initial="main" />);
    fireEvent.pointerDown(input());
    expect(options()).toEqual(suggestions);
    expect(highlighted()).toBe('main');
    // A second click leaves the list as it is.
    press('ArrowDown');
    fireEvent.pointerDown(input());
    expect(highlighted()).toBe('feature/login');
  });

  it('closes on Escape without letting the form see it', () => {
    const onFormEscape = vi.fn();
    render(<Harness onFormEscape={onFormEscape} />);
    press('ArrowDown');
    press('Escape');
    expect(options()).toEqual([]);
    expect(onFormEscape).not.toHaveBeenCalled();
    // Closed, Escape is the form's again.
    press('Escape');
    expect(onFormEscape).toHaveBeenCalledTimes(1);
  });

  it('closes when focus leaves, and lets Tab move it', () => {
    render(<Harness />);
    press('ArrowDown');
    expect(press('Tab')).toBe(true);
    fireEvent.blur(input());
    expect(options()).toEqual([]);
  });

  it('leaves keys to an input method that is composing', () => {
    render(<Harness />);
    press('ArrowDown', { isComposing: true });
    expect(options()).toEqual([]);
  });

  it('lists a suggestion offered twice twice, as the list changes', () => {
    const error = vi
      .spyOn(console, 'error')
      .mockImplementation(() => undefined);
    render(<Harness offered={['main', 'dev', 'main']} />);
    fireEvent.change(input(), { target: { value: 'dev' } });
    expect(options()).toEqual(['dev']);
    press('Escape');
    press('ArrowDown');
    expect(options()).toEqual(['main', 'dev', 'main']);
    expect(error).not.toHaveBeenCalled();
    error.mockRestore();
  });

  it('titles a suggestion in full on hover while it is cut off', () => {
    render(<Harness />);
    press('ArrowDown');
    const option = screen.getByRole('option', { name: 'feature/login' });
    Object.defineProperty(option, 'scrollWidth', { value: 500 });
    fireEvent.pointerEnter(option);
    expect(option.getAttribute('title')).toBe('feature/login');
  });
});
