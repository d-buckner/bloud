// SPDX-License-Identifier: AGPL-3.0-only

/**
 * The shared browser harness.
 *
 * One place for the knowledge of driving the Bloud UI in a browser: how to
 * sign in through Authentik, how an app's origin is derived, what the
 * dashboard grids are, the host-agent API client, and the page diagnostics
 * that make a failure readable. The e2e suite and an ad-hoc agent recipe both
 * import from here, so the two cannot drift.
 *
 * Nothing in this module imports `@playwright/test` at run time. The types
 * come from it, the runtime stays the caller's: the e2e runner and the CLI
 * each load exactly one Playwright instance.
 */
export * from './api';
// The API surface is also collected under one name, so a caller can hold the
// whole host-agent client (`api.installApp`) without importing each function.
export * as api from './api';
export * from './auth';
export * from './check';
export * from './config';
export * from './dashboard';
export * from './diagnostics';
export * from './forward-auth';
export * from './login';
export * from './origin';
export * from './page-objects';
export * from './report';
export * from './session';
export * from './wait';
