# Authentik SSO Integration Design

> **Status: PARTIALLY IMPLEMENTED** - Forward auth and OIDC working. LDAP in progress (see `docs/investigations/ldap-infrastructure-design.md`).

## Overview

Every app in Bloud should automatically get SSO via Authentik. This document describes how we achieve automatic, zero-config SSO integration for all apps.

## Integration Strategies

Apps need different auth methods depending on how they're accessed:

| Method | Use Case | Flow |
|--------|----------|------|
| **OIDC** | Web browsers | Redirect to Authentik → login → redirect back |
| **LDAP** | Mobile/TV apps, API clients | Username/password directly to app |
| **Forward Auth** | Apps without native auth | Traefik checks Authentik before proxying |

**Key insight:** Many apps need BOTH OIDC and LDAP:
- Web UI → OIDC (seamless SSO experience)
- Mobile app → LDAP (no browser redirect possible)
- TV app → LDAP (can't do OAuth flows)
- API access → LDAP or API keys

| Strategy | Description | Example Apps |
|----------|-------------|--------------|
| **OIDC only** | Simple web apps | Miniflux |
| **OIDC + LDAP** | Apps with mobile/TV clients | Jellyfin, Immich |
| **Forward Auth** | Apps without native auth | Radarr, Sonarr, qBittorrent |
| **Forward Auth + LDAP** | Web protected, but API needs auth | Some apps |

```
┌─────────────────────────────────────────────────────────────────────┐
│                           User Request                               │
│                        app.bloud.local                               │
└─────────────────────────────────────────────────────────────────────┘
                                │
                                ▼
┌─────────────────────────────────────────────────────────────────────┐
│                            Traefik                                   │
│  ┌─────────────────────────────────────────────────────────────┐   │
│  │  Forward Auth Middleware (for apps without native OIDC)      │   │
│  │  → Redirects to Authentik if not authenticated               │   │
│  └─────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────┘
                                │
           ┌────────────────────┼────────────────────┐
           ▼                    ▼                    ▼
    ┌─────────────┐     ┌─────────────┐      ┌─────────────┐
    │  Native     │     │  Forward    │      │  Header     │
    │  OIDC App   │     │  Auth App   │      │  Auth App   │
    │  (Miniflux) │     │  (Radarr)   │      │  (Proxy)    │
    └─────────────┘     └─────────────┘      └─────────────┘
           │                    │                    │
           └────────────────────┼────────────────────┘
                                ▼
                        ┌─────────────┐
                        │  Authentik  │
                        │  (IdP)      │
                        └─────────────┘
```

---

## App SSO Metadata

Each app definition includes SSO configuration:

```yaml
# catalog/apps/miniflux.yaml
name: miniflux
displayName: Miniflux
# ...
sso:
  strategy: native-oidc
  config:
    # Environment variables to set
    env:
      OAUTH2_PROVIDER: oidc
      OAUTH2_CLIENT_ID: "{{ client_id }}"
      OAUTH2_CLIENT_SECRET: "{{ client_secret }}"
      OAUTH2_REDIRECT_URL: "{{ app_url }}/oauth2/oidc/callback"
      OAUTH2_OIDC_DISCOVERY_ENDPOINT: "{{ authentik_url }}/application/o/{{ app_slug }}/.well-known/openid-configuration"
      OAUTH2_USER_CREATION: "1"
```

```yaml
# catalog/apps/radarr.yaml
name: radarr
displayName: Radarr
# ...
sso:
  strategy: forward-auth
  # No app-side config needed - Traefik handles auth
```

```yaml
# catalog/apps/jellyfin.yaml
name: jellyfin
displayName: Jellyfin
# ...
sso:
  strategy: native-oidc
  plugin: jellyfin-plugin-sso  # Requires plugin installation
  config:
    # Jellyfin uses XML config, handled specially
```

---

## Authentik Configuration

### OAuth2 Provider Template

For each app, we create an OAuth2 provider in Authentik:

```yaml
# Generated Authentik blueprint for each app
version: 1
metadata:
  name: "Bloud: {{ app_name }}"
entries:
  # OAuth2 Provider
  - model: authentik_providers_oauth2.oauth2provider
    id: provider-{{ app_slug }}
    attrs:
      name: "{{ app_display_name }}"
      authorization_flow: !Find [authentik_flows.flow, [slug, default-provider-authorization-implicit-consent]]
      client_type: confidential
      client_id: "{{ generated_client_id }}"
      client_secret: "{{ generated_client_secret }}"
      redirect_uris: |
        {{ app_url }}/oauth2/oidc/callback
        {{ app_url }}/api/auth/callback/authentik
      signing_key: !Find [authentik_crypto.certificatekeypair, [name, authentik Self-signed Certificate]]

  # Application
  - model: authentik_core.application
    id: app-{{ app_slug }}
    attrs:
      name: "{{ app_display_name }}"
      slug: "{{ app_slug }}"
      provider: !KeyOf provider-{{ app_slug }}
      meta_launch_url: "{{ app_url }}"
```

### LDAP Outpost

For apps that need LDAP (mobile/TV clients), we run an LDAP outpost:

```yaml
entries:
  # LDAP Provider
  - model: authentik_providers_ldap.ldapprovider
    id: provider-ldap
    attrs:
      name: "Bloud LDAP"
      authorization_flow: !Find [authentik_flows.flow, [slug, default-authentication-flow]]
      search_group: !Find [authentik_core.group, [name, Users]]
      bind_mode: direct
      search_mode: direct

  # LDAP Outpost
  - model: authentik_outposts.outpost
    id: outpost-ldap
    attrs:
      name: "Bloud LDAP Outpost"
      type: ldap
      providers:
        - !KeyOf provider-ldap
      config:
        authentik_host: "{{ authentik_url }}"
```

**LDAP Connection Details:**
- Host: `authentik` (container name) or `ldap.{{ domain }}`
- Port: `636` (LDAPS) or `389` (LDAP)
- Base DN: `dc=bloud,dc=local`
- Bind DN: `cn={{ username }},ou=users,dc=bloud,dc=local`

**App LDAP Configuration Example (Jellyfin):**
```
LDAP Server: authentik
LDAP Port: 636
Use SSL: true
Base DN: dc=bloud,dc=local
User Filter: (objectClass=user)
Admin Filter: (memberOf=cn=jellyfin-admins,ou=groups,dc=bloud,dc=local)
```

### Forward Auth Provider

For apps using forward auth, we create a **per-app proxy provider**. This allows each app to have its own Authentik application entry and enables proper redirect handling.

```yaml
# Forward auth blueprint for qBittorrent (auto-generated by host-agent)
version: 1
metadata:
  name: qbittorrent-sso-blueprint
  labels:
    managed-by: bloud

entries:
  # Per-app Proxy Provider
  - model: authentik_providers_proxy.proxyprovider
    id: qbittorrent-proxy-provider
    identifiers:
      name: qBittorrent Proxy Provider
    attrs:
      authorization_flow: !Find [authentik_flows.flow, [slug, default-provider-authorization-implicit-consent]]
      invalidation_flow: !Find [authentik_flows.flow, [slug, default-provider-invalidation-flow]]
      mode: forward_single
      # IMPORTANT: external_host must be the ROOT URL, not app-specific path
      # This ensures OAuth callback goes to /outpost.goauthentik.io/callback at root level
      external_host: "http://localhost:8080"
      access_token_validity: minutes=5

  # Application
  - model: authentik_core.application
    id: qbittorrent-application
    identifiers:
      slug: qbittorrent
    attrs:
      name: qBittorrent
      provider: !KeyOf qbittorrent-proxy-provider
      policy_engine_mode: any
      group: ""
      meta_launch_url: "http://localhost:8080/embed/qbittorrent"
```

**Key implementation details:**

1. **Per-app providers**: Each forward-auth app gets its own proxy provider and application, not a shared one. This enables proper application-level policies and auditing.

2. **external_host must be root URL**: Set to `http://localhost:8080` (not `http://localhost:8080/embed/qbittorrent/`). The OAuth callback path `/outpost.goauthentik.io/callback` is appended to this, and Traefik only routes this at the root level.

3. **Embedded outpost association**: The host-agent generates a `bloud-outpost.yaml` blueprint that adds all forward-auth proxy providers to the embedded outpost and configures `authentik_host`/`authentik_host_browser` for correct OAuth redirects. This blueprint uses `!Find` to reference providers by name, so they can be created by separate app blueprints.

```yaml
# bloud-outpost.yaml (auto-generated)
entries:
  - model: authentik_outposts.outpost
    state: present
    identifiers:
      name: authentik Embedded Outpost
    attrs:
      providers:
        - !Find [authentik_providers_proxy.proxyprovider, [name, "App Proxy Provider"]]
      config:
        authentik_host: "http://localhost:8080"
        authentik_host_browser: "http://localhost:8080"
```

---

## Traefik Integration

### Forward Auth Middleware

Each forward-auth app gets its own middleware. The forwardAuth address must go **directly to the Authentik outpost** (port 9001), not through Traefik (port 8080).

```yaml
# Generated Traefik dynamic config (apps-routes.yml)
http:
  middlewares:
    qbittorrent-forwardauth:
      forwardAuth:
        # IMPORTANT: Direct to port 9001, NOT through Traefik (8080)
        # Going through Traefik overwrites X-Forwarded-* headers,
        # breaking post-login redirect back to the original URL
        address: "http://localhost:9001/outpost.goauthentik.io/auth/traefik"
        trustForwardHeader: true
        authResponseHeaders:
          - X-authentik-username
          - X-authentik-groups
          - X-authentik-email
          - X-authentik-name
          - X-authentik-uid
```

**Why direct to port 9001?**

The forward-auth flow has two separate redirect concerns:

1. **OAuth redirect URLs** (authorize, callback) - Controlled by the outpost's `authentik_host` config. Should use port 8080 so redirects go through Traefik with iframe-friendly headers.

2. **Post-login redirect** - Controlled by `X-Forwarded-Uri` header that Traefik sends to the forwardAuth endpoint. The outpost uses this to redirect users back to their original URL after login.

If forwardAuth goes through Traefik (port 8080), Traefik overwrites the `X-Forwarded-*` headers for the internal request, losing the original URL. Going directly to port 9001 preserves these headers.

```
Browser → Traefik (8080) → forwardAuth middleware
                              ↓
                         Direct to Authentik (9001)
                         X-Forwarded-Uri: /embed/qbittorrent/ ✓ preserved
                              ↓
                         OAuth flow (through 8080 for iframe headers)
                              ↓
                         Post-login redirect to /embed/qbittorrent/ ✓
```

### Per-App Router Config

```yaml
# Generated Traefik config for forward-auth app (qBittorrent)
http:
  routers:
    qbittorrent-backend:
      rule: "PathPrefix(`/embed/qbittorrent`)"
      middlewares:
        - qbittorrent-forwardauth  # Per-app forward auth (must be first)
        - qbittorrent-stripprefix  # Remove /embed/qbittorrent prefix
        - iframe-headers           # Remove X-Frame-Options
        - embed-isolation          # Add COOP/COEP for iframe embedding
      service: qbittorrent
      priority: 100

  services:
    qbittorrent:
      loadBalancer:
        servers:
          - url: "http://localhost:8086"
```

```yaml
# App with native OIDC (no forward-auth middleware)
http:
  routers:
    miniflux-backend:
      rule: "PathPrefix(`/embed/miniflux`)"
      middlewares:
        - miniflux-stripprefix
        - iframe-headers
        - embed-isolation
      service: miniflux
      priority: 100
```

---

## Configuration Flow

### When Authentik is Installed

```
1. Install:      Authentik container starts with postgres/redis
                          │
                          ▼
2. Bootstrap:    Create admin user, generate signing keys
                          │
                          ▼
3. Configure:    Apply base blueprints (flows, branding)
                          │
                          ▼
4. Outpost:      Create embedded forward-auth outpost
                          │
                          ▼
5. Ready:        Authentik available for app integrations
```

### When an App is Installed

```
1. Install:      App container starts
                          │
                          ▼
2. Check SSO:    Does app have sso config?
                          │
              ┌───────────┴───────────┐
              ▼                       ▼
             YES                      NO
              │                       │
              ▼                       ▼
3. Create:    Generate OAuth2        Skip SSO setup
              provider in Authentik
                          │
                          ▼
4. Configure: Based on strategy:
              - native-oidc: Set env vars in Nix
              - forward-auth: Add Traefik middleware
              - header-auth: Configure trusted headers
                          │
                          ▼
5. Traefik:   Update dynamic config with router
                          │
                          ▼
6. Done:      App accessible with SSO
```

---

## Implementation Details

### Authentik Configurator

```go
type AuthentikConfigurator struct {
    baseURL  string
    apiToken string
}

func (c *AuthentikConfigurator) Sync(ctx context.Context, integration string, sources []string) error {
    switch integration {
    case "oauth2-provider":
        return c.syncOAuth2Providers(ctx, sources)
    case "forward-auth-app":
        return c.syncForwardAuthApps(ctx, sources)
    }
    return nil
}

func (c *AuthentikConfigurator) syncOAuth2Providers(ctx context.Context, apps []string) error {
    // For each app that needs an OAuth2 provider:
    // 1. Check if provider exists
    // 2. Create or update provider
    // 3. Create or update application
    // 4. Return client credentials for app configuration
}
```

### Credential Management

OAuth2 client credentials need to flow from Authentik to apps:

```
┌─────────────┐     ┌─────────────┐     ┌─────────────┐
│  Authentik  │────▶│   Bloud     │────▶│  apps.nix   │
│  (creates   │     │   Agent     │     │  (env vars) │
│   creds)    │     │  (stores)   │     │             │
└─────────────┘     └─────────────┘     └─────────────┘
```

**Option 1: Generate in Nix (Preferred)**
```nix
# Generate deterministic client ID/secret from app name + host secret
let
  hostSecret = builtins.readFile /etc/bloud/host-secret;
  clientId = builtins.hashString "sha256" "client-id:${appName}:${hostSecret}";
  clientSecret = builtins.hashString "sha256" "client-secret:${appName}:${hostSecret}";
in {
  # Use in app config
}
```

**Option 2: Store in Database**
```go
// Agent generates and stores credentials
type AppCredentials struct {
    AppName      string
    ClientID     string
    ClientSecret string
    CreatedAt    time.Time
}
```

### Nix Module for SSO

```nix
# nixos/lib/sso.nix
{ config, lib, pkgs, ... }:

let
  cfg = config.bloud.sso;

  # Generate client credentials deterministically
  mkClientId = appName:
    builtins.hashString "sha256" "bloud-client-id:${appName}:${cfg.hostSecret}";

  mkClientSecret = appName:
    builtins.hashString "sha256" "bloud-client-secret:${appName}:${cfg.hostSecret}";

in {
  options.bloud.sso = {
    enable = lib.mkEnableOption "Bloud SSO integration";
    authentikUrl = lib.mkOption {
      type = lib.types.str;
      default = "http://authentik:9000";
    };
    hostSecret = lib.mkOption {
      type = lib.types.str;
      description = "Host-specific secret for generating credentials";
    };
  };

  config = lib.mkIf cfg.enable {
    # Authentik blueprints are generated here
    # Traefik forward-auth middleware configured here
  };
}
```

---

## MVP App SSO Requirements

| App | OIDC | LDAP | Forward Auth | Notes |
|-----|:----:|:----:|:------------:|-------|
| **jellyfin** | ✓ | ✓ | - | OIDC for web, LDAP for TV/mobile apps |
| **immich** | ✓ | ✓ | - | OIDC for web, LDAP for mobile app |
| **jellyseerr** | ✓ | - | - | Web-only, native OIDC |
| **miniflux** | ✓ | - | - | Web-only, native OIDC |
| **radarr** | - | - | ✓ | No native auth, API uses keys |
| **sonarr** | - | - | ✓ | No native auth, API uses keys |
| **qbittorrent** | - | - | ✓ | Has basic auth, forward-auth adds SSO |
| **adguard-home** | - | - | ✓ | No OIDC support |
| **traefik** | - | - | - | Dashboard optional, internal service |
| **authentik** | - | - | - | Is the identity provider |
| **postgres** | - | - | - | Internal service, no web UI |
| **redis** | - | - | - | Internal service, no web UI |
| **gluetun** | - | - | - | Internal service, no web UI |

**Legend:**
- ✓ = Required for this app
- \- = Not applicable

---

## App-Specific Notes

### OIDC-Only Apps

| App | Config Method | Notes |
|-----|---------------|-------|
| Miniflux | Env vars | `OAUTH2_*` environment variables, web-only |

### OIDC + LDAP Apps (Multi-Client)

These apps have web UI and mobile/TV clients that need different auth:

| App | OIDC Config | LDAP Config | Notes |
|-----|-------------|-------------|-------|
| Jellyfin | SSO Plugin | Native LDAP | TV/mobile apps use LDAP |
| Immich | Env vars | Native LDAP | Mobile app uses LDAP |

**Jellyfin Example:**

```
┌─────────────────────────────────────────────────────────────────────┐
│                         Jellyfin Auth                                │
├─────────────────────────────────────────────────────────────────────┤
│                                                                      │
│   Web Browser ──────▶ OIDC ──────▶ Authentik ──────▶ Jellyfin      │
│   (redirect flow)     Plugin      /application/o/   (session)       │
│                                                                      │
│   Mobile App ───────▶ LDAP ──────▶ Authentik ──────▶ Jellyfin      │
│   (username/pass)     Native      :636 (LDAPS)      (session)       │
│                                                                      │
│   TV App ───────────▶ LDAP ──────▶ Authentik ──────▶ Jellyfin      │
│   (username/pass)     Native      :636 (LDAPS)      (session)       │
│                                                                      │
└─────────────────────────────────────────────────────────────────────┘
```

### Apps Needing Forward Auth

| App | Notes |
|-----|-------|
| Radarr | No native auth, forward-auth works well |
| Sonarr | Same as Radarr |
| qBittorrent | Has basic auth, forward-auth adds SSO |
| Jellyseerr | Has native OIDC but forward-auth simpler |
| AdGuard Home | No OIDC, needs forward-auth |

### Apps with Special Requirements

| App | Requirement |
|-----|-------------|
| Jellyfin | SSO plugin must be installed first |
| Immich | Admin must complete first-run wizard before OIDC works |

---

## Execution Order

SSO configuration must happen in the correct order:

```
Level 0: postgres, redis
         │
         ▼
Level 1: authentik (needs postgres, redis)
         │
         ▼
Level 2: traefik (needs authentik for forward-auth)
         │
         ▼
Level 3: All other apps (can now configure SSO)
```

---

## Reconciliation

On reconciliation:
1. Ensure Authentik has OAuth2 providers for all installed apps
2. Ensure Traefik has correct middleware config
3. Ensure app environment variables match expected values
4. Remove providers for uninstalled apps

---

## FAQs

### What if Authentik isn't installed?

Apps work without SSO. When Authentik is later installed:
1. Reconciliation runs
2. OAuth2 providers created for existing apps
3. Apps reconfigured with SSO credentials (requires rebuild)

### What if an app doesn't support any auth?

Use forward-auth. Traefik intercepts all requests and requires Authentik login before proxying to the app.

### Can users disable SSO for an app?

Yes, via overrides:
```yaml
overrides:
  radarr:
    sso: disabled
```

### How are admin users created?

First user to log in via SSO becomes admin (for apps that support this). Otherwise, Bloud creates admin user during app initialization.

### What about API access?

Apps that need API access (e.g., Radarr API for Jellyseerr) bypass forward-auth using:
1. API key authentication (app-native)
2. Service accounts in Authentik
3. Internal network access (container-to-container)
