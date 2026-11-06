import { fuzzyFilter, fuzzyScore } from './fuzzy';

describe('fuzzyScore', () => {
  it.each([
    ['', 'Open Settings'],
    ['os', 'Open Settings'],
    ['OPSET', 'Open Settings'],
    ['open set', 'Open Settings'],
    ['set open', 'Open Settings'],
  ])('matches %j in %j', (query, text) => {
    expect(fuzzyScore(query, text)).not.toBeNull();
  });

  it.each([
    ['so', 'Open Settings'],
    ['x', 'Open Settings'],
    ['open x', 'Open Settings'],
  ])('does not match %j in %j', (query, text) => {
    expect(fuzzyScore(query, text)).toBeNull();
  });

  it('prefers word starts and runs over scattered letters', () => {
    const wordStarts = fuzzyScore('ts', 'Toggle Settings') ?? 0;
    const scattered = fuzzyScore('ts', 'Rotates') ?? 0;
    const run = fuzzyScore('set', 'Open Settings') ?? 0;
    const spread = fuzzyScore('set', 'Show Entries Today') ?? 0;
    expect(wordStarts).toBeGreaterThan(scattered);
    expect(run).toBeGreaterThan(spread);
  });

  it('takes a later word start over an earlier letter mid-word', () => {
    expect(fuzzyScore('tl', 'Toggle Logs')).toBe(fuzzyScore('tl', 'T L'));
  });

  it('still matches when only the earlier letter leaves room for the rest', () => {
    expect(fuzzyScore('tle', 'Toggle Lamp')).not.toBeNull();
  });
});

describe('fuzzyFilter', () => {
  const items = ['Toggle Logs', 'Open Settings', 'Show Entries Today'];

  it('keeps the matches, best first', () => {
    expect(fuzzyFilter(items, (item) => fuzzyScore('set', item))).toEqual([
      'Open Settings',
      'Show Entries Today',
    ]);
  });

  it('keeps everything, in order, for an empty query', () => {
    expect(fuzzyFilter(items, (item) => fuzzyScore('', item))).toEqual(items);
  });
});
