import assert from 'node:assert/strict';
import { test } from 'node:test';
import { cartTotals } from './cart.js';

test('an empty cart costs nothing', () => {
  assert.deepEqual(cartTotals([]), { subtotal: 0, shipping: 0, tax: 0, total: 0 });
});

test('small orders pay shipping', () => {
  const totals = cartTotals([{ sku: 'mug', priceCents: 1200, quantity: 2 }]);
  assert.equal(totals.shipping, 495);
  assert.equal(totals.total, 2400 + 495 + 480);
});

test('orders from 50 euros ship free', () => {
  assert.equal(cartTotals([{ sku: 'lamp', priceCents: 5000, quantity: 1 }]).shipping, 0);
});
