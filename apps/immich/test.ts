import { test, expect, criticalErrors } from '../../integration/lib/app-test';

test.describe('immich', () => {
  test('loads in iframe without critical resource errors', async ({ openApp, resourceErrors }) => {
    const frame = await openApp();

    await expect(
      frame.getByRole('button', { name: /sign in with bloud/i }),
    ).toBeVisible({ timeout: 60_000 });

    expect(criticalErrors(resourceErrors)).toHaveLength(0);
  });

  test('health check responds', async ({ api, appName, embedPath, request }) => {
    await api.ensureAppRunning(appName);

    const response = await request.get(`${embedPath}api/server/ping`);
    expect(response.ok()).toBe(true);
  });
});
