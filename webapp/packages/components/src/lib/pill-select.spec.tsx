import { fireEvent, render, screen } from '@testing-library/react';
import { useState } from 'react';
import { vi } from 'vitest';

import { PillSelect } from './pill-select';

const options = [
  { value: 'a', label: 'Alpha' },
  { value: 'b', label: 'Beta' },
  { value: 'c', label: 'Gamma' },
];

function Controlled({ initial }: { initial: string }) {
  const [value, setValue] = useState(initial);
  return (
    <PillSelect
      label="Letter"
      options={options}
      value={value}
      onChange={setValue}
    />
  );
}

const pill = (name: string) => screen.getByRole('radio', { name });
const tabStops = () =>
  screen.getAllByRole('radio').filter((radio) => radio.tabIndex === 0);

describe('PillSelect', () => {
  it('is one tab stop, on the checked pill', () => {
    render(<Controlled initial="b" />);
    expect(tabStops()).toEqual([pill('Beta')]);
  });

  it('puts the tab stop on the first pill when none is checked', () => {
    render(<Controlled initial="" />);
    expect(tabStops()).toEqual([pill('Alpha')]);
  });

  it.each([
    ['ArrowRight', 'Beta', 'Gamma'],
    ['ArrowDown', 'Beta', 'Gamma'],
    ['ArrowLeft', 'Beta', 'Alpha'],
    ['ArrowUp', 'Beta', 'Alpha'],
    ['ArrowRight', 'Gamma', 'Alpha'],
    ['ArrowLeft', 'Alpha', 'Gamma'],
    ['Home', 'Gamma', 'Alpha'],
    ['End', 'Alpha', 'Gamma'],
  ])('%s from %s checks and focuses %s', (key, from, to) => {
    const initial = options.find((o) => o.label === from)?.value ?? '';
    render(<Controlled initial={initial} />);
    pill(from).focus();
    fireEvent.keyDown(pill(from), { key });
    expect(pill(to).getAttribute('aria-checked')).toBe('true');
    expect(document.activeElement).toBe(pill(to));
    expect(tabStops()).toEqual([pill(to)]);
  });

  it('leaves modified arrows alone', () => {
    render(<Controlled initial="a" />);
    pill('Alpha').focus();
    expect(
      fireEvent.keyDown(pill('Alpha'), { key: 'ArrowRight', altKey: true }),
    ).toBe(true);
    expect(pill('Alpha').getAttribute('aria-checked')).toBe('true');
  });

  it('submits the form it is in on Enter, as a native radio does', () => {
    const onSubmit = vi.fn((event: { preventDefault: () => void }) =>
      event.preventDefault(),
    );
    render(
      <form aria-label="Create" onSubmit={onSubmit}>
        <Controlled initial="a" />
      </form>,
    );
    expect(fireEvent.keyDown(pill('Beta'), { key: 'Enter' })).toBe(false);
    expect(onSubmit).toHaveBeenCalledTimes(1);
    expect(pill('Alpha').getAttribute('aria-checked')).toBe('true');
  });

  it('leaves Enter alone outside a form', () => {
    render(<Controlled initial="a" />);
    expect(fireEvent.keyDown(pill('Alpha'), { key: 'Enter' })).toBe(true);
  });
});
