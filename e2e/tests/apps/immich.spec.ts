import { expect } from '@playwright/test';
import { test } from '@playwright/test';
import { appTest } from '../../lib/appTest';

appTest('immich', 'Immich', async ({ appFrame }) => {
  await test.step('Immich loads and shows SSO login button', async () => {
    // Immich renders a login page with an SSO button when autoLaunch is disabled.
    // Wait up to 60s for the Next.js app to hydrate after first load.
    await expect(
      appFrame.getByRole('button', { name: /sign in with bloud/i }),
    ).toBeVisible({ timeout: 60_000 });
  });
});
