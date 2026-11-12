/** The class names given, without the falsy ones, as one `className`. */
export function cx(...names: (string | false | null | undefined)[]): string {
  return names.filter(Boolean).join(' ');
}
