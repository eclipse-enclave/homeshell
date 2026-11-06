import { fireEvent, render, screen } from '@testing-library/react';

import { Select } from './select';

const options = [
  { value: 'a', label: 'Alpha' },
  { value: 'b', label: 'Beta' },
];

describe('Select', () => {
  it('renders the label, options and current value', () => {
    render(
      <Select label="Letter" value="b" options={options} onChange={vi.fn()} />
    );
    const control = screen.getByLabelText('Letter') as HTMLSelectElement;
    expect(control.value).toBe('b');
    expect(screen.getByRole('option', { name: 'Alpha' })).toBeTruthy();
  });

  it('reports the chosen value', () => {
    const onChange = vi.fn();
    render(
      <Select label="Letter" value="a" options={options} onChange={onChange} />
    );
    fireEvent.change(screen.getByLabelText('Letter'), { target: { value: 'b' } });
    expect(onChange).toHaveBeenCalledWith('b');
  });

  it('renders a badge and can be disabled', () => {
    render(
      <Select
        label="Letter"
        value="a"
        options={options}
        onChange={vi.fn()}
        badge="applies when supported"
        disabled
      />
    );
    expect(screen.getByText('applies when supported')).toBeTruthy();
    expect((screen.getByLabelText('Letter') as HTMLSelectElement).disabled).toBe(true);
  });
});
