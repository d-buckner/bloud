// SPDX-License-Identifier: AGPL-3.0-only
//
// One installed app, from four directions at once: what the orchestrator says,
// what the app's own address answers, what a signed-in page renders, and what an
// anonymous visitor reaches.
//
//   node harness/bin/browser.mjs run app-health jellyfin
//
// Each direction is a separate question, so the report names which one broke. An
// app that is RUNNING in the graph and serving a 502 is a real state, and it is a
// different fix from an app that never converged.
//
// A check file imports nothing: the runner hands over everything it uses. That is
// what lets `browser.mjs checks` list this file without a browser or a TypeScript
// transform.

// A check file imports nothing it cannot get from the context, and it stays plain
// JavaScript so `browser.mjs checks` can list it without a transform.

export const name = 'app health';
export const description = 'one app: orchestrator state, its own address, a signed-in page, an anonymous one';
export const options = { target: 'jellyfin', timeoutMs: 8 * 60_000 };

export default async function ({ api, page, check, open, shot, note, target, newPage, harness }) {
  const status = await api.getAppStatus(target);
  if (status !== 'running') {
    check.fail(
      `${target} is running`,
      `its state is "${status ?? 'not installed'}". Install it first: ` +
        `node harness/bin/browser.mjs api POST /api/apps/${target}/install '{}'`,
    );
    return;
  }
  check.ok(true, `${target} is running`);

  const catalog = await api.listCatalogApps().catch(() => []);
  const strategy = catalog.find((a) => a.catalogId === target)?.sso?.strategy ?? 'unknown';
  const origin = harness.appOrigin(target);
  note(`${origin} declares SSO strategy "${strategy}"`);

  // Traefik covers a dead upstream with Bloud's waiting page, so a browser alone
  // cannot tell "the app is serving" from "the proxy is speaking for it".
  const answer = await check.must('the app address answers', () => harness.probeAppAnswer(origin));
  check.ok(
    !answer.loading,
    'the app serves its own page rather than the Bloud waiting page',
    answer.loading ? 'the response carries X-Bloud-Loading: Traefik is covering a dead upstream' : '',
  );
  check.ok(answer.status < 500, 'the app address answers below 500', `status ${answer.status}`);

  await open(target);
  const text = await check.text();
  check.ok(
    text.trim().length > 0,
    'the signed-in page renders content',
    text.trim().length > 0 ? '' : `the page is blank at ${page.url()}`,
  );

  // An app that delegates sign-in asks its own backend who the visitor is before
  // the round trip has finished, and the answer is 401. `noProblemsIgnoringSignIn`
  // sets that aside and prints what it set aside; anything else still fails.
  await check.noProblemsIgnoringSignIn('the signed-in page loaded without problems');
  note(`screenshot: ${await shot(target)}`);

  // The anonymous leg answers a different question: does the gate hold? An app
  // that declares SSO must not hand its UI to a visitor who has not signed in.
  const anon = await newPage({ signedIn: false });
  try {
    await anon.open(target);
    note(`an anonymous visitor lands on ${anon.page.url()}`);
    // An identity provider renders its flow after the document, so one read of
    // the text can land on a page that is still filling in.
    await check.poll(
      'the anonymous page shows something rather than a blank shell',
      async () => ((await anon.review()).text ?? '').trim().length > 0,
      20_000,
    );
    const review = await anon.review();
    if (strategy !== 'none') {
      const gated = /if\/flow|login|log in|sign in|password/i.test(`${review.url}\n${review.text}`);
      check.ok(
        gated,
        'an anonymous visitor meets a sign-in prompt',
        `landed on ${review.url} with no prompt in sight`,
      );
    } else {
      check.skip('an anonymous visitor meets a sign-in prompt', `${target} declares SSO strategy "none"`);
    }
  } finally {
    await anon.close();
  }
}
