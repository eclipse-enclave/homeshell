import { expect, test } from '../fixtures';
import { selectProject } from './workstream-helpers';

test('an out-of-date session says which settings changed and how to apply them', async ({
  page,
}) => {
  await page.goto('/');
  await selectProject(page, 'acme-webshop');
  const workstream = page.getByRole('region', {
    name: 'Workstream bugfix/checkout-tax',
  });
  const sessionRow = (title: string) =>
    workstream.getByRole('listitem').filter({ hasText: title });

  await expect(
    sessionRow('Add regression tests').getByText(
      'Settings changed since it started: allow_domains, features. Restart it with settings to apply them.',
    ),
  ).toBeVisible();
  await expect(
    sessionRow('Fix rounding in tax calculation').getByText(
      'Settings changed since it started',
    ),
  ).toHaveCount(0);
});
