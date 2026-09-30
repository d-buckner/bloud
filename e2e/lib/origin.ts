// SPDX-License-Identifier: AGPL-3.0-only

/**
 * App origins are derived, not hardcoded, so the same specs can drive the dev
 * loop (http://<app>.localhost:8080) and a proxied deployment
 * (https://<app>.example.com).
 *
 * The base defaults to BLOUD_URL, which is the origin the dashboard is served
 * from: apps live on the same host as subdomains, so a https deployment keeps
 * them on the same TLS story as the dashboard instead of silently falling back
 * to plain http. BLOUD_APP_BASE_URL overrides it for the case where the apps
 * are reachable somewhere other than the public origin.
 */
const APP_BASE =
  process.env.BLOUD_APP_BASE_URL ?? process.env.BLOUD_URL ?? 'http://localhost:8080';

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

/** Escape a literal string for use inside a RegExp. Exported so a spec can
 * compose its own pattern without re-deriving the origin. */
export function escapeRegExp(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

/**
 * A RegExp matching the app's own origin, for toHaveURL assertions. Escaped so
 * the dots in a hostname do not act as wildcards and match an unrelated host.
 */
export function appOriginPattern(app: string): RegExp {
  return new RegExp(escapeRegExp(appOrigin(app)));
}

/**
 * A RegExp matching the app's origin followed by a path pattern. The origin is
 * escaped and `pathPattern` is raw regex source, so a spec can pin the path it
 * expects without hand-writing the origin half.
 *
 * Anchored at the start of the URL: toHaveURL tests the whole URL, and an
 * unanchored origin pattern would also match a redirect that merely contains
 * the app's hostname.
 */
export function appUrlPattern(app: string, pathPattern = ''): RegExp {
  return new RegExp('^' + escapeRegExp(appOrigin(app)) + pathPattern);
}
