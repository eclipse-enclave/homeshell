import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { Button } from './button';

describe('Button', () => {
  it('calls onClick', () => {
    const onClick = vi.fn();
    render(<Button onClick={onClick}>Go</Button>);
    fireEvent.click(screen.getByRole('button', { name: 'Go' }));
    expect(onClick).toHaveBeenCalledTimes(1);
  });

  // aria-disabled keeps the button focusable, unlike disabled, but it still
  // does nothing: not its handler, and not a submit.
  it('does nothing while aria-disabled', () => {
    const onClick = vi.fn();
    const onSubmit = vi.fn((event: Event) => event.preventDefault());
    render(
      <form onSubmit={(event) => onSubmit(event.nativeEvent)}>
        <Button onClick={onClick} aria-disabled>
          Go
        </Button>
        <Button type="submit" aria-disabled="true">
          Send
        </Button>
      </form>,
    );
    fireEvent.click(screen.getByRole('button', { name: 'Go' }));
    fireEvent.click(screen.getByRole('button', { name: 'Send' }));
    expect(onClick).not.toHaveBeenCalled();
    expect(onSubmit).not.toHaveBeenCalled();
  });

  // Each variant carries its own class so the theme can style it; the styling
  // itself lives in controls.module.css.
  it('renders a distinct class per variant', () => {
    render(
      <>
        <Button>Ghost</Button>
        <Button variant="primary">Primary</Button>
        <Button variant="danger">Danger</Button>
      </>,
    );
    const classes = ['Ghost', 'Primary', 'Danger'].map(
      (name) => screen.getByRole('button', { name }).className,
    );
    expect(new Set(classes).size).toBe(3);
  });

  // The caller's class is added to the variant's, not substituted for it. Checked
  // by comparing against the same button without one, rather than by pinning a
  // class count that any new modifier would break for no real reason.
  it('keeps a caller-supplied class alongside the variant', () => {
    render(
      <>
        <Button className="extra">Ghost</Button>
        <Button>Plain</Button>
      </>,
    );
    const withExtra = screen.getByRole('button', { name: 'Ghost' });
    const plain = screen.getByRole('button', { name: 'Plain' });
    expect(withExtra.classList.contains('extra')).toBe(true);
    for (const variantClass of Array.from(plain.classList)) {
      expect(withExtra.classList.contains(variantClass)).toBe(true);
    }
  });
});

describe('Button with keys', () => {
  it('marks its letter and names its keys in the tooltip', () => {
    render(
      <Button mnemonic="o" shortcuts={['enter']} title="Open it">
        Open
      </Button>,
    );
    const button = screen.getByRole('button', { name: 'Open' });
    expect(button.getAttribute('data-mnemonic')).toBe('o');
    expect(button.getAttribute('aria-keyshortcuts')).toBe('Enter Alt+O');
    expect(button.title).toMatch(/^Open it \((Enter, Alt\+O|Enter, ⌥O)\)$/);
    expect(button.querySelector('span span')?.textContent).toBe('O');
  });

  it('titles itself with its label when given no title', () => {
    render(<Button mnemonic="d">Delete</Button>);
    expect(screen.getByRole('button').title).toMatch(
      /^Delete \((Alt\+D|⌥D)\)$/,
    );
  });

  // Its keys do nothing while it is disabled, so the tooltip is only the reason.
  it('names no keys while disabled', () => {
    render(
      <>
        <Button mnemonic="o" disabled title="Offline">
          Open
        </Button>
        <Button mnemonic="d" aria-disabled title="Offline">
          Delete
        </Button>
      </>,
    );
    for (const button of screen.getAllByRole('button')) {
      expect(button.title).toBe('Offline');
    }
  });

  // A label it cannot read as text is named by aria-label, as a menu's is.
  it('falls back to aria-label for the tooltip of a non-text label', () => {
    render(
      <Button mnemonic="o" aria-label="Open">
        <b>Open</b>
      </Button>,
    );
    expect(screen.getByRole('button').title).toMatch(/^Open \((Alt\+O|⌥O)\)$/);
  });

  it('renders non-text children as they are', () => {
    render(
      <Button mnemonic="o">
        <b>Open</b>
      </Button>,
    );
    expect(screen.getByRole('button').innerHTML).toBe('<b>Open</b>');
  });
});
