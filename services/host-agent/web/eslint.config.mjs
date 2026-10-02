// SPDX-License-Identifier: AGPL-3.0-only
//
// Flat ESLint config for the host-agent web frontend. This is the frontend
// counterpart to the repo-root .golangci.yml: a focused set of size/branch
// gates, reported without truncation, so code that is worse than the current
// ceiling errors and the ceiling only ever tightens.
//
// Thresholds (re-measured 2026-10-01 over src/: 45 .svelte + 38 .ts files,
// eslint@10.10.0 + eslint-plugin-svelte@3.23.0):
//
//   no-else-return  allowElseIf: false. Zero violations today: the codebase
//   already returns early, including through else-if chains. This one is a
//   guard, not a backlog.
//
//   complexity  <= 10  Unchanged. Worst is exactly 10 (clients/httpClient.ts,
//   stores/appProgress.ts); p90 is 6. First ratchet target: 8 (5 over).
//
//   max-depth  <= 4  Unchanged. Worst is 3 (SetupWizard, +layout, settings,
//   gridPlacement).
//
//   max-lines  <= 320 .ts / 420 lib .svelte / 700 routes .svelte. Snapped to
//   the measured worst (314 graphLayout.ts / 404 AISettingsSection / 692
//   settings/+page). The old 400 / 450 / 900 caps sat 25-30% above reality,
//   which is room a page can grow into without ever tripping anything.
//
//   max-lines-per-function  <= 50 (80 under __tests__). Worst non-test is 78
//   (timeline/graphLayout.ts) then 65 (stores/grid.ts); the worst Svelte
//   handler is 41. p90 is 20, so 50 is already 2.5x a typical function.
//   Test bodies carry fixtures and assertions and get 80.
//
//   max-len  <= 120  Tabs count as 4 columns (tabWidth), so 120 is ~108 raw
//   chars at 3-deep indent. Comments, URLs, and regexp literals are exempt;
//   strings and template literals are NOT, so a long literal still has to be
//   wrapped rather than waved through.
//
// Ratchet: every cap sits at or just above the current worst, so the gate
// blocks anything worse than today. Lower them as the debt is repaid; the open
// violations at these caps are listed in the PR that introduced them.
//
// Zero lint-disable directives: the Tailscale invite uses rel="external"
// (rule-recognized as outbound), so no suppression is needed anywhere in src.

import tseslint from 'typescript-eslint'
import svelte from 'eslint-plugin-svelte'

// Branch/size gates shared by every source file. `lines` is the max-lines cap,
// `fnLines` the per-function cap.
const gates = ({ lines, fnLines = 50 }) => ({
  complexity: ['error', 10],
  'max-depth': ['error', 4],
  'max-lines': ['error', { max: lines, skipBlankLines: true, skipComments: true }],
  'max-lines-per-function': ['error', { max: fnLines, skipBlankLines: true, skipComments: true }],
  'max-len': ['error', {
    code: 120,
    tabWidth: 4,
    ignoreUrls: true,
    ignoreComments: true,
    ignoreRegExpLiterals: true,
  }],
  'no-else-return': ['error', { allowElseIf: false }],
})

export default tseslint.config(
  {
    ignores: [
      'node_modules/**',
      '.svelte-kit/**',
      'build/**',
      'dist/**',
      'static/**',
      'playwright-report/**',
      'test-results/**',
    ],
  },

  // TypeScript sources (lib + routes logic).
  {
    files: ['**/*.ts'],
    extends: [tseslint.configs.recommended],
    rules: gates({ lines: 320 }),
  },

  // Svelte components: base wiring (processor + parser) then override the
  // inner script parser to TypeScript so `<script lang="ts">` parses.
  // max-lines counts rendered source; route pages legitimately carry more
  // template + style than a reusable component, hence the higher cap.
  ...svelte.configs['flat/recommended'],
  {
    files: ['**/*.svelte'],
    languageOptions: {
      parserOptions: {
        parser: '@typescript-eslint/parser',
        ecmaVersion: 'latest',
        sourceType: 'module',
      },
    },
    rules: gates({ lines: 420 }),
  },
  {
    files: ['src/routes/**/*.svelte'],
    rules: gates({ lines: 700 }),
  },

  // Test bodies state a case in full: fixtures, arrange/act/assert, and the
  // expectation table all land in one function. Looser per-function cap,
  // same everything else.
  {
    files: ['**/__tests__/**'],
    rules: {
      'max-lines-per-function': ['error', { max: 80, skipBlankLines: true, skipComments: true }],
    },
  },
);
