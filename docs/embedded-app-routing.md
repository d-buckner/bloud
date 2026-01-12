# Embedded App URL Routing Problem

## Problem Statement

Bloud embeds third-party apps in iframes at `/apps/{app-name}`. The iframe loads content from `/embed/{app-name}/`, which Traefik proxies to the app's container.

Some apps (AdGuard Home, Actual Budget) don't support `BASE_URL` configuration - they assume they're served from `/`. When these apps:
- Redirect to `/install.html`
- Request `/static/app.js`
- Navigate to `/dashboard`

...the browser resolves these against the origin root, breaking out of the `/embed/{app}/` namespace.

## Current Approach: Service Worker

Intercept requests and rewrite URLs to stay within `/embed/{app}/`.

### What Works
- Intercepting `fetch()` calls from JS (regardless of how URL was constructed)
- Rewriting redirect `Location` headers
- Determining app context from referer header

### What Doesn't Work
- `window.location = '/path'` navigation
- `<a href="/path">` clicks (navigation, not fetch)
- `<form action="/path">` submissions
- Any URL usage that bypasses fetch()

### Complexity Issues
- Need to identify "Bloud routes" vs "app routes" for root-level requests
- Referer can come from `/embed/` or `/apps/` paths
- Edge cases keep accumulating
- Null referer scenarios

## Alternative: Server-Side Rewriting

Rewrite responses in Traefik or Go backend before they reach the browser.

### What Works
- Redirect `Location` headers
- Static strings in HTML/JS/CSS via regex replacement

### What Doesn't Work
- Dynamically constructed URLs in JavaScript
- Runtime template strings
- Any URL built at execution time

### Complexity Issues
- Regex replacement in response bodies is fragile
- Need to handle different content types
- Risk of corrupting binary data or breaking valid content

## Alternative: Subdomain Isolation

Use `adguard.bloud.local` instead of `/embed/adguard-home/`.

### What Works
- Apps genuinely are at root path - no rewriting needed
- Clean separation of concerns
- Works with any app regardless of BASE_URL support

### What Doesn't Work
- Requires wildcard DNS or hosts file entries
- More complex local development setup
- Cookie/auth complexity across subdomains
- May not work well with Authentik SSO

## Alternative: Accept Limitations

Only support apps that have BASE_URL configuration.

### Apps That Work
- Miniflux (supports BASE_URL)
- Many modern apps

### Apps That Don't Work
- AdGuard Home
- Actual Budget
- Legacy apps assuming root

## Open Questions

1. How common is dynamic URL construction vs static paths in these apps?

2. For AdGuard Home specifically, what URL patterns actually break?
   - Initial redirect to /install.html
   - Asset loading
   - API calls
   - Navigation

3. Is "mostly works" acceptable, or do we need 100% compatibility?

4. Could we contribute BASE_URL support upstream to these apps?

5. What's the user experience when something breaks? Can we fail gracefully?

## Investigation Needed

- [ ] Audit AdGuard Home's actual URL usage patterns
- [ ] Audit Actual Budget's URL usage patterns
- [ ] Test what breaks with current SW implementation
- [ ] Determine if partial support is acceptable UX

## Decision Criteria

- Complexity vs completeness tradeoff
- Maintenance burden
- User experience when things break
- Number of apps affected
