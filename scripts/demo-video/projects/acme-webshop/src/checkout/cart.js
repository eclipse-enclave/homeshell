import { taxFor } from './tax.js';

export const SHIPPING_CENTS = 495;
export const FREE_SHIPPING_FROM_CENTS = 5000;

/** Totals for a cart: `items` are `{ sku, priceCents, quantity }`. */
export function cartTotals(items) {
  const subtotal = items.reduce((sum, item) => sum + item.priceCents * item.quantity, 0);
  const shipping = subtotal === 0 || subtotal >= FREE_SHIPPING_FROM_CENTS ? 0 : SHIPPING_CENTS;
  const tax = items.reduce((sum, item) => sum + taxFor(item), 0);
  return { subtotal, shipping, tax, total: subtotal + shipping + tax };
}
