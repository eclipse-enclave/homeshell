/** `text` as a sentence begins: its first letter upper-cased. */
export function sentence(text: string): string {
  return text.charAt(0).toUpperCase() + text.slice(1);
}
