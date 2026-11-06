import { fireEvent, render, screen } from '@testing-library/react';

import { CopyablePath } from './copyable-path';

describe('CopyablePath', () => {
  it('titles the path in full on hover while it is cut off', () => {
    const path = '/home/user/projects/homeshell';
    render(<CopyablePath path={path} />);
    const shown = screen.getByText(path);
    Object.defineProperty(shown, 'scrollWidth', { value: 500 });
    fireEvent.pointerEnter(shown);
    expect(shown.getAttribute('title')).toBe(path);
  });
});
