export const TAX_RATE = 0.2;

/** Tax for one cart line, rounded per line to whole cents. */
export function taxFor(item) {
  return Math.round(item.priceCents * item.quantity * TAX_RATE);
}
