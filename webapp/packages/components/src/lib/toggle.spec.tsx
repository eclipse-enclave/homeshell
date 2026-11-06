import { fireEvent, render, screen } from '@testing-library/react';

import { Toggle } from './toggle';

describe('Toggle', () => {
  it('reflects the checked state', () => {
    render(<Toggle label="Enabled" checked onChange={vi.fn()} />);
    expect((screen.getByRole('checkbox', { name: /Enabled/ }) as HTMLInputElement).checked).toBe(
      true
    );
  });

  it('reports the new value on click', () => {
    const onChange = vi.fn();
    render(<Toggle label="Enabled" checked={false} onChange={onChange} />);
    fireEvent.click(screen.getByRole('checkbox', { name: /Enabled/ }));
    expect(onChange).toHaveBeenCalledWith(true);
  });

  it('can be disabled and show a badge', () => {
    render(
      <Toggle
        label="Enabled"
        checked
        onChange={vi.fn()}
        disabled
        badge="applies when supported"
      />
    );
    expect((screen.getByRole('checkbox', { name: /Enabled/ }) as HTMLInputElement).disabled).toBe(
      true
    );
    expect(screen.getByText('applies when supported')).toBeTruthy();
  });
});
