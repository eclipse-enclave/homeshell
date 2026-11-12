import { expect, test } from '@playwright/test';
import { writeFileSync } from 'node:fs';
import { join } from 'node:path';
import {
  buildEnclaveFixture,
  isolatedDaemon,
  removeEnclaveFixture,
} from '../enclave-helpers';
import { disposeIsolatedDaemons } from '../isolated-daemon';

test.describe('Enclave CLI contract', () => {
  test.beforeAll(buildEnclaveFixture);
  test.afterEach(disposeIsolatedDaemons);
  test.afterAll(removeEnclaveFixture);

  test('an Enclave whose --pass-env +NAME does not add to the list is unavailable', async () => {
    test.setTimeout(45_000);
    const daemon = await isolatedDaemon();
    try {
      const enclaveStatus = async () => {
        const providers = await (await daemon.api.get('/api/providers')).json();
        return providers.find((p: { id: string }) => p.id === 'enclave')
          ?.status;
      };
      await expect
        .poll(async () => (await enclaveStatus())?.state, { timeout: 15_000 })
        .toBe('available');

      writeFileSync(join(daemon.fixture, 'old-pass-env'), '');
      // The verdict is kept per binary, so a new daemon asks the old Enclave afresh.
      await daemon.restart();
      await expect.poll(enclaveStatus, { timeout: 15_000 }).toMatchObject({
        state: 'unavailable',
        reason: expect.stringContaining('update Enclave'),
      });
    } finally {
      await daemon.stop();
    }
  });
});
