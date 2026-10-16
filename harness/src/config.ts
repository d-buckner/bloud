// SPDX-License-Identifier: AGPL-3.0-only

/**
 * Where the harness points, and who it signs in as.
 *
 * The names match the e2e suite on purpose: a developer runs `./bloud dev` and
 * both the gate suite and an ad-hoc recipe drive the same instance with the
 * same credentials.
 *
 *   BLOUD_URL            the public origin (Traefik); default http://localhost:8080
 *   BLOUD_APP_BASE_URL   overrides the app subdomains only; default BLOUD_URL
 *   BLOUD_API_URL        the host-agent API; default http://localhost:3000
 *   BLOUD_E2E_USERNAME   the test user name; default admin
 *   BLOUD_E2E_PASSWORD   the test user password; default password
 */
export const BLOUD_URL = process.env.BLOUD_URL ?? 'http://localhost:8080';
export const BLOUD_API_URL = process.env.BLOUD_API_URL ?? 'http://localhost:3000';

/**
 * The origin an app subdomain is served under. It defaults to the dashboard
 * origin, so a https deployment keeps the apps on the same TLS story as the
 * dashboard. `BLOUD_APP_BASE_URL` overrides it when the apps are reachable
 * somewhere else.
 */
export const APP_BASE =
  process.env.BLOUD_APP_BASE_URL ?? BLOUD_URL;

export const CREDENTIALS = {
  username: process.env.BLOUD_E2E_USERNAME ?? 'admin',
  password: process.env.BLOUD_E2E_PASSWORD ?? 'password',
} as const;
