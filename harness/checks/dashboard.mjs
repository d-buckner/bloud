// SPDX-License-Identifier: AGPL-3.0-only
//
// The dashboard, checked against what the API says is installed.
//
//   node harness/bin/browser.mjs run dashboard
//
// The grid and the installed list are two different reads of one fact, and they
// disagree in exactly the failures that matter: a store that never got the
// convergence event, a tile stuck on an install spinner, an app the graph reports
// as RUNNING that the home screen never heard about.

export const name = 'dashboard';
export const description = 'the home screen and the catalog, compared with what the API reports as installed';
export const options = { target: 'bloud', timeoutMs: 4 * 60_000 };

export default async function ({ page, api, check, note, shot, newPage, harness }) {
  const apps = await check.must('the API reports what is installed', () => api.listInstalledApps());
  const grid = apps.filter((app) => !app.is_system);
  note(`${grid.length} user apps installed: ${grid.map((a) => a.catalog_id).join(', ') || '(none)'}`);

  await check.appears('.app-slot', 'the home screen paints a grid', 45_000);
  // The dashboard asks its own API who is signed in before the identity provider
  // has answered, so a cold load records a 401 on that probe.
  await check.noProblemsIgnoringSignIn('the home screen loaded without problems');

  for (const app of grid) {
    try {
      await harness.assertRunningTile(page, app.display_name);
      check.ok(true, `the home screen shows ${app.display_name} as converged`);
    } catch (err) {
      check.fail(`the home screen shows ${app.display_name} as converged`, err.message);
    }
  }

  await page.goto('/catalog');
  // The catalog paints its cards after it has fetched the catalog and the
  // installed list, so a count taken the moment the document lands reads zero.
  await check.appears('.app-card', 'the catalog paints its cards', 30_000);

  for (const app of grid) {
    try {
      await harness.assertInstalledInCatalog(page, app.display_name);
      check.ok(true, `the catalog marks ${app.display_name} installed`);
    } catch (err) {
      check.fail(`the catalog marks ${app.display_name} installed`, err.message);
    }
  }

  // An anonymous visitor must not see the grid: it is the signed-in surface. The
  // dashboard sends a visitor who is not signed in to the identity provider, so
  // the wait is for the redirect to finish, not for the first byte.
  const anon = await newPage({ signedIn: false });
  try {
    await anon.open('bloud');
    await check.poll(
      'the dashboard is not readable without signing in',
      async () => {
        const review = await anon.review();
        return /if\/flow|login|log in|sign in|password/i.test(`${review.url}\n${review.text}`);
      },
      30_000,
    );
  } finally {
    await anon.close();
  }

  note(`screenshot: ${await shot('dashboard')}`);
}
