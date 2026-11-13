import { cx } from './cx';

describe('cx', () => {
  it('joins the class names given, leaving out the falsy ones', () => {
    expect(cx('a', false, undefined, 'b', null, '')).toBe('a b');
  });
});
