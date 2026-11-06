import { fireEvent, render, screen } from '@testing-library/react';

import { Pill } from './pill';

describe('Pill', () => {
  const text = 'feature/a-branch-name-too-long-for-its-pill';

  it('titles itself in full on hover while its text is cut off', () => {
    render(<Pill>{text}</Pill>);
    const pill = screen.getByText(text);
    Object.defineProperty(pill, 'scrollWidth', { value: 500 });
    fireEvent.pointerEnter(pill);
    expect(pill.getAttribute('title')).toBe(text);
  });

  it('shows no tooltip while its text fits', () => {
    render(<Pill>{text}</Pill>);
    fireEvent.pointerEnter(screen.getByText(text));
    expect(screen.queryByTitle(text)).toBeNull();
  });
});
