// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '../lib/fixtures';
import AxeBuilder from '@axe-core/playwright';

/**
 * The accessibility gate for #270.
 *
 * The audit that opened this epic was a one-off: axe-core run by hand against a
 * hosted instance, 36 findings written down, and nothing to stop any of them
 * coming back. A list of fixed defects is a list of defects in progress. This is
 * the part that keeps them fixed.
 *
 * Four routes, the four the epic named. Each is audited after it has actually
 * rendered, because axe on a loading shell passes for the wrong reason: the
 * spinner has no contrast to fail.
 *
 * Only `serious` and `critical` are asserted. That is the epic's bar, and it is
 * also the honest one: axe's lower severities include judgement calls about
 * heading order and landmark redundancy that a design is entitled to make, and a
 * gate that cries wolf gets switched off. The two severities kept here are the
 * ones where a person using assistive technology cannot use the page at all.
 */

const ROUTES = [
  { path: '/', name: 'Home', wait: 'h1, .app-tile, .empty-state' },
  { path: '/catalog', name: 'Catalog', wait: 'h1' },
  { path: '/settings', name: 'Settings', wait: 'h2' },
  { path: '/developer', name: 'Developer', wait: '.svelte-flow' }
];

for (const route of ROUTES) {
  test.describe(`a11y: ${route.name}`, () => {
    test(`${route.path} has no serious axe-core violations`, async ({ authenticatedPage: page }) => {
      await page.goto(route.path);
      await page.waitForSelector(route.wait, { timeout: 60_000 });

      const results = await new AxeBuilder({ page })
        .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'])
        .analyze();

      const blocking = results.violations.filter(
        (v) => v.impact === 'serious' || v.impact === 'critical'
      );

      // Failure has to be readable in a CI log, not a 40KB JSON blob. One line
      // per violation, with the selectors axe blamed, which is the whole
      // information a fix needs.
      const summary = blocking
        .map((v) => `${v.impact}: ${v.id} (${v.help})\n    ${v.nodes.map((n) => n.target.join(' ')).join('\n    ')}`)
        .join('\n\n');

      expect(blocking, `${route.path}: ${blocking.length} serious violation(s)\n\n${summary}`).toHaveLength(0);
    });
  });
}
