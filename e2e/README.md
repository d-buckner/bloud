# Portable Runtime End-to-End Tests

This Playwright suite verifies the user-visible lifecycle of apps on an already-provisioned
portable Debian runtime. Environment provisioning and teardown belong outside Playwright.

The current release slice covers Jellyfin:

1. Sign in to Bloud.
2. Install Jellyfin through the catalog UI if it is not already installed.
3. Verify the Jellyfin embed endpoint is reachable.
4. Open Jellyfin from the Bloud dashboard.
5. Sign in to Jellyfin using the Bloud account through LDAP.

Run against a prepared runtime:

```bash
BLOUD_URL=http://localhost:3000 \
BLOUD_E2E_USERNAME=e2etest \
BLOUD_E2E_PASSWORD=e2etest123 \
npm --prefix e2e test -- --project=jellyfin
```
