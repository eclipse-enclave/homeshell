import { fireEvent, render, screen } from '@testing-library/react';

import { titleWhenTruncated } from './truncated-title';

/** Gives `element` the widths jsdom does not lay out. */
function setWidths(
  element: HTMLElement,
  scrollWidth: number,
  clientWidth: number,
) {
  Object.defineProperty(element, 'scrollWidth', { value: scrollWidth });
  Object.defineProperty(element, 'clientWidth', { value: clientWidth });
}

describe('titleWhenTruncated', () => {
  const renderLabel = () => {
    render(<span {...titleWhenTruncated('a long label')}>a long label</span>);
    return screen.getByText('a long label');
  };

  it('titles the element in full on hover while its text is cut off', () => {
    const label = renderLabel();
    expect(label.hasAttribute('title')).toBe(false);
    setWidths(label, 200, 100);
    fireEvent.pointerEnter(label);
    expect(label.getAttribute('title')).toBe('a long label');
  });

  it('leaves text that fits untitled', () => {
    const label = renderLabel();
    setWidths(label, 100, 100);
    fireEvent.pointerEnter(label);
    expect(label.hasAttribute('title')).toBe(false);
  });

  it('measures text that changes under a title again', () => {
    const { rerender } = render(
      <span {...titleWhenTruncated('a long label')}>a long label</span>,
    );
    const label = screen.getByText('a long label');
    setWidths(label, 200, 100);
    fireEvent.pointerEnter(label);

    rerender(<span {...titleWhenTruncated('longer still')}>longer still</span>);
    expect(label.getAttribute('title')).toBe('longer still');
  });

  it('drops the title of text replaced by text that fits', () => {
    let scrollWidth = 200;
    const { rerender } = render(
      <span {...titleWhenTruncated('a long label')}>a long label</span>,
    );
    const label = screen.getByText('a long label');
    Object.defineProperty(label, 'scrollWidth', { get: () => scrollWidth });
    Object.defineProperty(label, 'clientWidth', { value: 100 });
    fireEvent.pointerEnter(label);

    scrollWidth = 50;
    rerender(<span {...titleWhenTruncated('short')}>short</span>);
    expect(label.hasAttribute('title')).toBe(false);
  });
});
