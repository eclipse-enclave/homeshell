import { fireEvent, render, screen } from '@testing-library/react';

import { NumberField } from './number-field';

describe('NumberField', () => {
  it('renders the numeric value', () => {
    render(<NumberField label="Port" value={8420} onChange={vi.fn()} />);
    expect((screen.getByLabelText('Port') as HTMLInputElement).value).toBe('8420');
  });

  it('reports numeric changes and ignores non-numeric input', () => {
    const onChange = vi.fn();
    render(<NumberField label="Port" value={0} onChange={onChange} />);
    fireEvent.change(screen.getByLabelText('Port'), { target: { value: '42' } });
    expect(onChange).toHaveBeenCalledWith(42);

    onChange.mockClear();
    fireEvent.change(screen.getByLabelText('Port'), { target: { value: '' } });
    expect(onChange).not.toHaveBeenCalled();
  });

  it('emits what is typed while editing, without clamping mid-keystroke', () => {
    const onChange = vi.fn();
    render(
      <NumberField label="Port" value={10} onChange={onChange} min={100} max={9000} />
    );
    const input = screen.getByLabelText('Port');
    // A first digit below the min must not snap to the bound (that would make a
    // multi-digit value impossible to type toward).
    fireEvent.change(input, { target: { value: '2' } });
    expect(onChange).toHaveBeenLastCalledWith(2);
    fireEvent.change(input, { target: { value: '200' } });
    expect(onChange).toHaveBeenLastCalledWith(200);
  });

  it('clamps out-of-range input to the nearest bound on blur', () => {
    const onChange = vi.fn();
    render(
      <NumberField label="Port" value={10} onChange={onChange} min={1} max={100} />
    );
    const input = screen.getByLabelText('Port');

    fireEvent.change(input, { target: { value: '0' } });
    fireEvent.blur(input, { target: { value: '0' } });
    expect(onChange).toHaveBeenLastCalledWith(1); // below min → min

    fireEvent.change(input, { target: { value: '9999' } });
    fireEvent.blur(input, { target: { value: '9999' } });
    expect(onChange).toHaveBeenLastCalledWith(100); // above max → max

    onChange.mockClear();
    fireEvent.blur(input, { target: { value: '42' } });
    expect(onChange).not.toHaveBeenCalled(); // in range → no correction on blur
  });

  it('keeps a partial decimal entry in the field while typing', () => {
    const onChange = vi.fn();
    render(<NumberField label="Interval" value={5} onChange={onChange} />);
    const input = screen.getByLabelText('Interval') as HTMLInputElement;
    // A trailing decimal point must survive (a number input would drop it).
    fireEvent.change(input, { target: { value: '0.' } });
    expect(input.value).toBe('0.');
    fireEvent.change(input, { target: { value: '0.5' } });
    expect(input.value).toBe('0.5');
    expect(onChange).toHaveBeenLastCalledWith(0.5);
  });

  it('does not clobber a focused partial entry when the value prop changes', () => {
    // A trailing-dot entry parses to 0, so a naive resync would replace "0."
    // with the incoming number. While the field is focused the user owns it, so
    // an external value change (e.g. a settings refetch) must leave "0." alone.
    const { rerender } = render(
      <NumberField label="Interval" value={5} onChange={vi.fn()} commitOnBlur />
    );
    const input = screen.getByLabelText('Interval') as HTMLInputElement;
    fireEvent.focus(input);
    fireEvent.change(input, { target: { value: '0.' } });
    expect(input.value).toBe('0.');

    rerender(
      <NumberField label="Interval" value={9} onChange={vi.fn()} commitOnBlur />
    );
    expect(input.value).toBe('0.'); // partial entry preserved while focused

    // Once unfocused, a later external value change is adopted normally.
    fireEvent.blur(input, { target: { value: '0.' } });
    rerender(
      <NumberField label="Interval" value={7} onChange={vi.fn()} commitOnBlur />
    );
    expect(input.value).toBe('7');
  });

  it('in commitOnBlur mode defers onChange until blur and clamps then', () => {
    const onChange = vi.fn();
    render(
      <NumberField
        label="Interval"
        value={5}
        onChange={onChange}
        min={0.001}
        commitOnBlur
      />
    );
    const input = screen.getByLabelText('Interval');
    // Typing does not round-trip while editing.
    fireEvent.change(input, { target: { value: '0.5' } });
    expect(onChange).not.toHaveBeenCalled();
    // The fractional value is committed on blur.
    fireEvent.blur(input, { target: { value: '0.5' } });
    expect(onChange).toHaveBeenLastCalledWith(0.5);
    // A below-min value is clamped to the floor on blur.
    fireEvent.change(input, { target: { value: '0' } });
    fireEvent.blur(input, { target: { value: '0' } });
    expect(onChange).toHaveBeenLastCalledWith(0.001);
  });

  it('commits the clamped value on blur (not on keystroke) when onCommit is set', () => {
    const onChange = vi.fn();
    const onCommit = vi.fn();
    render(
      <NumberField
        label="Lines"
        value={20}
        onChange={onChange}
        onCommit={onCommit}
        min={1}
        max={24}
      />
    );
    const input = screen.getByLabelText('Lines');

    // Typing tracks the edit but never commits.
    fireEvent.change(input, { target: { value: '5' } });
    expect(onCommit).not.toHaveBeenCalled();

    // Blur commits the clamped value.
    fireEvent.blur(input, { target: { value: '99' } });
    expect(onCommit).toHaveBeenLastCalledWith(24);
    // The display is also corrected to the clamp.
    expect(onChange).toHaveBeenLastCalledWith(24);
  });

  it('shows a validation error beneath the input', () => {
    render(
      <NumberField
        label="Port"
        value={0}
        onChange={vi.fn()}
        error="must be at least 1"
      />
    );
    expect(screen.getByRole('alert').textContent).toContain('must be at least 1');
  });
});
