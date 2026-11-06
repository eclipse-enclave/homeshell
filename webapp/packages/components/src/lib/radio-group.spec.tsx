import { fireEvent, render, screen } from '@testing-library/react';

import { RadioGroup } from './radio-group';

const options = [
  { value: 'system', label: 'System' },
  { value: 'light', label: 'Light' },
  { value: 'dark', label: 'Dark' },
];

describe('RadioGroup', () => {
  it('checks the selected option', () => {
    render(
      <RadioGroup label="Theme" value="light" options={options} onChange={vi.fn()} />
    );
    expect((screen.getByRole('radio', { name: 'Light' }) as HTMLInputElement).checked).toBe(true);
    expect((screen.getByRole('radio', { name: 'Dark' }) as HTMLInputElement).checked).toBe(false);
  });

  it('reports the chosen option', () => {
    const onChange = vi.fn();
    render(
      <RadioGroup label="Theme" value="light" options={options} onChange={onChange} />
    );
    fireEvent.click(screen.getByRole('radio', { name: 'Dark' }));
    expect(onChange).toHaveBeenCalledWith('dark');
  });
});
