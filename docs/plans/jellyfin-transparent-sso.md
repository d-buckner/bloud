# Plan: Jellyfin Transparent SSO via IAuthenticationProvider

## Context
Jellyfin users see a login screen even with a valid Authentik session. The goal is to bridge the Authentik session to a Jellyfin session transparently — user navigates to Jellyfin and lands directly on the dashboard.

The constraint that there's no Jellyfin API to impersonate a user without their password is solved by writing a minimal `IAuthenticationProvider` C# plugin. It validates HMAC-signed tokens, letting a trusted sidecar authenticate any user without knowing their LDAP password.

---

## Architecture

```
Browser → Traefik (forward auth) → sidecar → Jellyfin
                                         ↑         (only /web/index.html)
                          All other paths → Jellyfin directly
```

**Token format:**
```
Pw = "<unix_timestamp>:<base64(HMAC-SHA256(secret, username:timestamp))>"
```
Plugin validates: `|now - timestamp| ≤ 30s`, HMAC matches. Prevents replay attacks.

---

## Components

### 1. C# Plugin — `apps/jellyfin/auth-plugin/`
Minimal `IAuthenticationProvider` that validates the HMAC token in the `password` field.

**Files:**
- `JellyfinHmacAuth.csproj` — targets `net8.0`, references `Jellyfin.Controller 10.10.3` with `ExcludeAssets=runtime`
- `Plugin.cs` — extends `BasePlugin<PluginConfiguration>`, registers with Jellyfin DI
- `HmacAuthProvider.cs` — implements `IAuthenticationProvider`:
  - `Name` → `"HmacAuth"`
  - `IsEnabled` → `true`
  - `Authenticate(username, password, user)`:
    - Parse `<timestamp>:<hmac>` from password
    - Check timestamp within ±30s
    - Compute HMAC-SHA256(env `JELLYFIN_HMAC_SECRET`, `username:timestamp`)
    - Constant-time compare; return `ProviderAuthenticationResult { Username = username }` on success, throw on failure

**Artifact:** `apps/jellyfin/hmac-plugin/JellyfinHmacAuth.dll` — committed to git, follows same pattern as `authentik/branding/`.
Built via: `cd apps/jellyfin/auth-plugin && dotnet publish -c Release -o ../hmac-plugin`

Referenced in module.nix via `${./hmac-plugin}` Nix path.

---

### 2. Go Sidecar — `host-agent jellyfin-sidecar` subcommand

**New file:** `services/host-agent/cmd/host-agent/jellyfin_sidecar.go`

Handles a single entry point: `GET /web/index.html` and `GET /web/`. All other paths proxy to Jellyfin unchanged.

**Flow for `GET /web/index.html`:**
1. Check `jf_authed` cookie → if present, proxy to `http://127.0.0.1:8096/web/index.html`
2. If absent:
   - Get username from `X-authentik-username` header
   - If header missing → proxy unchanged (graceful degradation to normal login screen)
   - Generate HMAC token `<timestamp>:<hmac>`
   - `POST http://127.0.0.1:8096/Users/AuthenticateByName {Username, Pw}`
   - `GET http://127.0.0.1:8096/System/Info` → get `Id` (ServerId)
   - Serve HTML interstitial that:
     - Sets `jellyfin_credentials` in `localStorage` (`{Servers: [{Id, AccessToken, UserId, LocalAddress}]}`)
     - Sets `jf_authed=1` cookie scoped to `/embed/jellyfin/web/`, 8h TTL
     - `window.location.replace('/embed/jellyfin/web/index.html')` to reload
3. If Jellyfin API fails → proxy unchanged (graceful degradation)

The `LocalAddress` in stored credentials is computed from `window.location.origin + '/embed/jellyfin'` in the interstitial HTML, so it resolves correctly for any hostname (bloud.local, IP, etc.).

**Config via env vars:**
- `JELLYFIN_HMAC_SECRET` — shared secret (written by secrets manager)
- `JELLYFIN_SIDECAR_PORT` — default `9876`
- `JELLYFIN_INTERNAL_URL` — default `http://127.0.0.1:8096`

**Modify:** `services/host-agent/cmd/host-agent/main.go` — add:
```go
case "jellyfin-sidecar":
    os.Exit(runJellyfinSidecar(os.Args[2:]))
```

---

### 3. Secrets — `services/host-agent/internal/secrets/manager.go`

Add to `Secrets` struct:
```go
JellyfinHmacSecret string `json:"jellyfinHmacSecret"`
```

Add to migration block in `Load()`:
```go
if secrets.JellyfinHmacSecret == "" {
    secrets.JellyfinHmacSecret = generateSecret(32)
    updated = true
}
```

Add to `generateAndSave()`:
```go
JellyfinHmacSecret: generateSecret(32),
```

Add to `writeEnvFiles()` — write `jellyfin.env`:
```
JELLYFIN_HMAC_SECRET=<secret>
```

---

### 4. NixOS Integration — `apps/jellyfin/module.nix`

**Add `envFile`** to the existing `mkPodmanApp` call:
```nix
envFile = "${secretsDir}/jellyfin.env"
```

**Add plugin volume mount** to `volumes`:
```nix
"${./hmac-plugin}:/config/plugins/HmacAuth:ro"
```
Use `builtins.pathExists ./hmac-plugin` guard so Nix eval doesn't fail before the DLL is built.

**Add sidecar systemd service** to `extraServices`:
```nix
jellyfin-auth-sidecar = {
  description = "Jellyfin auth sidecar";
  after = [ "bloud-init-secrets.service" "podman-jellyfin.service" ];
  wantedBy = [ "bloud-apps.target" ];
  partOf = [ "bloud-apps.target" ];
  serviceConfig = {
    Type = "simple";
    EnvironmentFile = "${secretsDir}/jellyfin.env";
    Environment = [
      "JELLYFIN_SIDECAR_PORT=9876"
      "JELLYFIN_INTERNAL_URL=http://127.0.0.1:8096"
    ];
    ExecStart = "${config.bloud.agentPath} jellyfin-sidecar";
    Restart = "on-failure";
  };
};
```

**Add Traefik route** via activation script in `extraConfig` (using `lib.stringAfter [ "users" "bloud-traefik-config" ]`):
```yaml
http:
  routers:
    jellyfin-web-sidecar:
      rule: "PathPrefix(`/embed/jellyfin/web`)"
      service: jellyfin-auth-sidecar
      priority: 200
      middlewares: [jellyfin-sidecar-strip]
  services:
    jellyfin-auth-sidecar:
      loadBalancer:
        servers:
          - url: "http://127.0.0.1:9876"
  middlewares:
    jellyfin-sidecar-strip:
      stripPrefix:
        prefixes: ["/embed/jellyfin"]
```

---

## Files Summary

| Action | File |
|--------|------|
| Create | `apps/jellyfin/auth-plugin/JellyfinHmacAuth.csproj` |
| Create | `apps/jellyfin/auth-plugin/Plugin.cs` |
| Create | `apps/jellyfin/auth-plugin/HmacAuthProvider.cs` |
| Create | `services/host-agent/cmd/host-agent/jellyfin_sidecar.go` |
| Modify | `services/host-agent/cmd/host-agent/main.go` |
| Modify | `services/host-agent/internal/secrets/manager.go` |
| Modify | `apps/jellyfin/module.nix` |
| Build & commit | `apps/jellyfin/hmac-plugin/JellyfinHmacAuth.dll` |

---

## Build Instructions

The C# plugin DLL must be built and committed before `./bloud rebuild` will include it.
dotnet is not in the macOS dev environment; build in NixOS:

```bash
# Option A: via nix-shell on any machine with Nix
nix-shell -p dotnet-sdk_8 --run \
  "cd apps/jellyfin/auth-plugin && dotnet publish -c Release -o ../hmac-plugin"

# Option B: via the NixOS dev environment
./bloud shell "cd /path/to/bloud/apps/jellyfin/auth-plugin && dotnet publish -c Release -o ../hmac-plugin"

# Then commit the artifact
git add apps/jellyfin/hmac-plugin/
git commit -m "build: add Jellyfin HMAC auth plugin DLL"
```

---

## Known Limitations

- Users auto-provisioned by HMAC auth will be regular users (not admins). Jellyfin admin rights must be assigned manually for admin users — LDAP group check only runs on LDAP logins. Normal media access is unaffected.
- The `jf_authed` cookie has an 8h TTL. After expiry the user is silently re-authenticated on next page load.

---

## Verification

1. Build plugin DLL (see Build Instructions above)
2. `./bloud rebuild` to apply NixOS changes
3. Open `http://localhost/embed/jellyfin/web/index.html` — should land on dashboard without a login prompt
4. DevTools → Application → Local Storage → confirm `jellyfin_credentials` is set
5. Clear localStorage + delete `jf_authed` cookie → reload → should auto-authenticate again
