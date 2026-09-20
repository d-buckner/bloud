// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
//
// Flat ESLint config for the host-agent web frontend. This is the frontend
// counterpart to the repo-root .golangci.yml: a focused set of size/branch
// gates, reported without truncation, so code that is worse than the current
// ceiling errors and the ceiling only ever tightens.
//
// Thresholds (measured 2026-09-14, eslint@10.10.0 + eslint-plugin-svelte@3.23.0):
//   complexity ≤ 10  McCabe "easily testable" band; 152/160 funcs already pass.
//   max-depth   ≤ 4  no function exceeded depth 4 at gate time (free ceiling).
//   max-lines   ≤ 400 for .ts (no .ts file exceeded it at gate time).
//   max-lines   ≤ 450 for lib/*.svelte  (worst lib component was 425).
//   max-lines   ≤ 900 for routes/*.svelte  (worst route page was 867).
//
// Ratchet: all thresholds pass at the CURRENT worst (cyclop semantics: the
// gate blocks anything worse than today). Lower them as the debt is repaid:
//   - routes/+page.svelte (settings 867, developer 778) → split, then drop
//     the routes cap toward 500.
//   - complexity first target 8; max-lines .ts first drop to 300.
//
// Zero lint-disable directives: the Tailscale invite uses rel="external"
// (rule-recognized as outbound), so no suppression is needed anywhere in src.

import tseslint from 'typescript-eslint'
import svelte from 'eslint-plugin-svelte'

// Branch/size gates shared by every source file; `lines` is the max-lines cap.
const gates = (lines) => ({
  complexity: ['error', 10],
  'max-depth': ['error', 4],
  'max-lines': ['error', { max: lines, skipBlankLines: true, skipComments: true }],
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
    rules: gates(400),
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
    rules: gates(450),
  },
  {
    files: ['src/routes/**/*.svelte'],
    rules: gates(900),
  },
);
