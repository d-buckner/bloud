// SPDX-License-Identifier: AGPL-3.0-only
import { defineConfig } from 'vitest/config';

/**
 * The harness unit tests only.
 *
 * `cli/recipe.spec.ts` is a Playwright spec, and Playwright refuses to be loaded
 * by anything but its own runner. Vitest's default include would match that file
 * by name and fail the run for a reason that has nothing to do with the code under
 * test, so the pattern is stated here instead.
 */
export default defineConfig({
  test: {
    include: ['test/**/*.test.ts'],
  },
});
