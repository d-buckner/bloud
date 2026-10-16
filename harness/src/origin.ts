// SPDX-License-Identifier: AGPL-3.0-only
import { APP_BASE } from './config';

/**
 * App origins are derived, not hardcoded, so the same code drives the dev loop
 * (`http://<app>.localhost:8080`) and a proxied deployment
 * (`https://<app>.example.com`).
 */

/** The full origin an app's subdomain is served under, e.g. https://immich.example.com. */
export function appOrigin(app: string): string {
  const base = new URL(APP_BASE);
  return `${base.protocol}//${app}.${base.host}`;
}

/** The bare host:port an app's subdomain is served under, e.g. immich.example.com. */
export function appHost(app: string): string {
  const base = new URL(APP_BASE);
  return `${app}.${base.host}`;
}

/** Escape a literal string for use inside a RegExp. Exported so a caller can
 * compose its own pattern without re-deriving the origin. */
export function escapeRegExp(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

/**
 * A RegExp matching the app's own origin, for URL assertions. Escaped so the
 * dots in a hostname do not act as wildcards and match an unrelated host.
 */
export function appOriginPattern(app: string): RegExp {
  return new RegExp(escapeRegExp(appOrigin(app)));
}

/**
 * A RegExp matching the app's origin followed by a path pattern. The origin is
 * escaped and `pathPattern` is raw regex source, so a caller can pin the path it
 * expects without hand-writing the origin half.
 *
 * Anchored at the start of the URL: an unanchored origin pattern would also
 * match a redirect that merely contains the app's hostname.
 */
export function appUrlPattern(app: string, pathPattern = ''): RegExp {
  return new RegExp('^' + escapeRegExp(appOrigin(app)) + pathPattern);
}
