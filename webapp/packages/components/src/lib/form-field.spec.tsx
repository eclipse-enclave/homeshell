import { render, screen } from '@testing-library/react';

import { FormField } from './form-field';

describe('FormField', () => {
  it('associates the label with the control and shows hint, badge and error', () => {
    render(
      <FormField
        label="Field"
        htmlFor="ctrl"
        hint="the hint"
        badge="applies when supported"
        error="bad value"
      >
        <input id="ctrl" />
      </FormField>
    );
    expect(screen.getByLabelText('Field')).toBeTruthy();
    expect(screen.getByText('the hint')).toBeTruthy();
    expect(screen.getByText('applies when supported')).toBeTruthy();
    expect(screen.getByRole('alert').textContent).toBe('bad value');
  });
});
