import { fireEvent, render, screen } from '@testing-library/react';

import { TextField } from './text-field';

describe('TextField', () => {
  it('renders the value and hint', () => {
    render(<TextField label="Name" value="hello" onChange={vi.fn()} hint="a hint" />);
    expect((screen.getByLabelText('Name') as HTMLInputElement).value).toBe('hello');
    expect(screen.getByText('a hint')).toBeTruthy();
  });

  it('reports typed input', () => {
    const onChange = vi.fn();
    render(<TextField label="Name" value="" onChange={onChange} />);
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'abc' } });
    expect(onChange).toHaveBeenCalledWith('abc');
  });
});
