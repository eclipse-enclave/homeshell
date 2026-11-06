import { act, render, screen } from '@testing-library/react';
import { useRef } from 'react';

import { useModalFocus, usePaneFocus, useReturnFocus } from './focus-return';

const button = (name: string) => screen.getByRole('button', { name });

function Modal({ fallback }: { fallback?: () => HTMLElement | null }) {
  const ref = useRef<HTMLButtonElement>(null);
  useModalFocus(() => ref.current, fallback);
  return (
    <button ref={ref} type="button">
      Inside
    </button>
  );
}

describe('useModalFocus', () => {
  it('focuses its initial element and gives focus back on close', () => {
    const { rerender } = render(<button type="button">Opener</button>);
    button('Opener').focus();
    rerender(
      <>
        <button type="button">Opener</button>
        <Modal />
      </>,
    );
    expect(document.activeElement).toBe(button('Inside'));
    rerender(<button type="button">Opener</button>);
    expect(document.activeElement).toBe(button('Opener'));
  });

  it('falls back when the opener has left the page', () => {
    const { rerender } = render(<button type="button">Opener</button>);
    button('Opener').focus();
    const fallback = () => screen.getByRole('button', { name: 'Fallback' });
    rerender(
      <>
        <button type="button">Fallback</button>
        <Modal fallback={fallback} />
      </>,
    );
    rerender(<button type="button">Fallback</button>);
    expect(document.activeElement).toBe(button('Fallback'));
  });
});

function Pane() {
  const ref = useRef<HTMLElement>(null);
  usePaneFocus(() => ref.current);
  return (
    <section ref={ref} aria-label="Pane" tabIndex={-1}>
      <button type="button">In pane</button>
    </section>
  );
}

describe('usePaneFocus', () => {
  const page = (pane: boolean) => (
    <>
      <button type="button">Opener</button>
      <button type="button">Elsewhere</button>
      {pane && <Pane />}
    </>
  );

  it('focuses the pane, and gives focus back while it is still inside', () => {
    const { rerender } = render(page(false));
    button('Opener').focus();
    rerender(page(true));
    expect(document.activeElement).toBe(
      screen.getByRole('region', { name: 'Pane' }),
    );
    act(() => button('In pane').focus());
    rerender(page(false));
    expect(document.activeElement).toBe(button('Opener'));
  });

  it('leaves focus the user took elsewhere', () => {
    const { rerender } = render(page(false));
    button('Opener').focus();
    rerender(page(true));
    act(() => button('Elsewhere').focus());
    rerender(page(false));
    expect(document.activeElement).toBe(button('Elsewhere'));
  });
});

function Toggle({
  open,
  request,
}: {
  open: boolean;
  request: (set: (value: boolean) => void) => void;
}) {
  const fallbackRef = useRef<HTMLButtonElement>(null);
  const setReturn = useReturnFocus(open, () => fallbackRef.current);
  request(setReturn);
  return (
    <>
      <button ref={fallbackRef} type="button">
        Fallback
      </button>
      {!open && <button type="button">Opener</button>}
      {open && <input aria-label="Box" autoFocus />}
    </>
  );
}

describe('useReturnFocus', () => {
  let setReturn: (value: boolean) => void = () => undefined;
  const capture = (set: (value: boolean) => void) => {
    setReturn = set;
  };

  it('focuses the fallback, when asked, once the opener has gone', () => {
    const { rerender } = render(<Toggle open={false} request={capture} />);
    button('Opener').focus();
    rerender(<Toggle open request={capture} />);
    act(() => setReturn(true));
    rerender(<Toggle open={false} request={capture} />);
    expect(document.activeElement).toBe(button('Fallback'));
  });

  it('leaves focus alone unless asked', () => {
    const { rerender } = render(<Toggle open={false} request={capture} />);
    rerender(<Toggle open request={capture} />);
    const box = screen.getByRole('textbox', { name: 'Box' });
    expect(document.activeElement).toBe(box);
    rerender(<Toggle open={false} request={capture} />);
    expect(document.activeElement).toBe(document.body);
  });
});
