import { fireEvent, render, screen } from '@testing-library/react';

import { DefaultableField } from './defaultable-field';

function control({
  value,
  disabled,
}: {
  value: string;
  disabled: boolean;
}) {
  return <input aria-label="ctrl" value={value} disabled={disabled} readOnly />;
}

describe('DefaultableField', () => {
  it('enables the control and toggles last-used on click', () => {
    const onChange = vi.fn();
    render(
      <DefaultableField
        field={{ value: 'a', useLastUsed: false }}
        onChange={onChange}
      >
        {control}
      </DefaultableField>,
    );
    expect((screen.getByLabelText('ctrl') as HTMLInputElement).disabled).toBe(false);
    fireEvent.click(screen.getByRole('checkbox', { name: /Last used/ }));
    expect(onChange).toHaveBeenCalledWith({ value: 'a', useLastUsed: true });
  });

  it('disables the control while last-used is selected', () => {
    render(
      <DefaultableField
        field={{ value: 'a', useLastUsed: true, lastUsed: 'b' }}
        onChange={vi.fn()}
      >
        {control}
      </DefaultableField>,
    );
    expect((screen.getByLabelText('ctrl') as HTMLInputElement).disabled).toBe(true);
    expect(screen.getByText(/Remembering: b/)).toBeTruthy();
  });
});
