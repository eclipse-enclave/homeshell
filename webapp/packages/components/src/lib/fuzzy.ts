// Per matched character, plus a bonus for continuing a run or starting a word.
// Runs weigh most, as in VS Code, so a prefix beats an acronym of word starts.
const MATCH = 1;
const RUN_BONUS = 5;
const WORD_START_BONUS = 3;

// Where the "previous match" sits before the first one, so that no index
// continues a run from it.
const BEFORE_START = -2;

const startsWord = (text: string, index: number) =>
  index === 0 || /[^a-z0-9]/.test(text[index - 1]);

function isSubsequence(word: string, text: string, from: number): boolean {
  let next = from;
  for (const char of word) {
    next = text.indexOf(char, next) + 1;
    if (next === 0) return false;
  }
  return true;
}

/**
 * A later occurrence of `char` that starts a word and still leaves room for
 * `rest`, else `index`: leftmost is not always best, and for "tl" the l of
 * "Logs" beats the one inside "Toggle".
 */
function wordStartFrom(
  char: string,
  rest: string,
  text: string,
  index: number,
) {
  for (
    let next = text.indexOf(char, index + 1);
    next !== -1;
    next = text.indexOf(char, next + 1)
  ) {
    if (startsWord(text, next) && isSubsequence(rest, text, next + 1)) {
      return next;
    }
  }
  return index;
}

/** One query word against `text`, as `fuzzyScore` describes. */
function wordScore(word: string, text: string): number | null {
  let score = 0;
  let from = 0;
  let previous = BEFORE_START;
  for (let i = 0; i < word.length; i++) {
    let index = text.indexOf(word[i], from);
    if (index === -1) return null;
    if (index !== previous + 1 && !startsWord(text, index)) {
      index = wordStartFrom(word[i], word.slice(i + 1), text, index);
    }
    score += MATCH;
    if (index === previous + 1) score += RUN_BONUS;
    if (startsWord(text, index)) score += WORD_START_BONUS;
    previous = index;
    from = index + 1;
  }
  return score;
}

/**
 * Scores `text` against `query` the way VS Code's quick open matches: each
 * word of the query has to appear in the text with its characters in order,
 * case-insensitively, and characters that continue a run or start a word score
 * higher. The words match independently, so `alpha impl` finds "Implement" in
 * project alpha. Null when something does not match.
 */
export function fuzzyScore(query: string, text: string): number | null {
  const haystack = text.toLowerCase();
  let score = 0;
  for (const word of query.toLowerCase().split(/\s+/).filter(Boolean)) {
    const points = wordScore(word, haystack);
    if (points === null) return null;
    score += points;
  }
  return score;
}

/** The items `score` does not reject, best first; ties keep their order. */
export function fuzzyFilter<T>(
  items: readonly T[],
  score: (item: T) => number | null,
): T[] {
  return items
    .map((item) => ({ item, score: score(item) }))
    .filter(
      (entry): entry is { item: T; score: number } => entry.score !== null,
    )
    .sort((a, b) => b.score - a.score)
    .map((entry) => entry.item);
}
