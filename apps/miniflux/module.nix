{ config, pkgs, lib, ... }:

let
  mkBloudApp = import ../../nixos/lib/bloud-app.nix { inherit config pkgs lib; };
  bloudCfg = config.bloud;
  secretsDir = "/home/${bloudCfg.user}/.local/share/${bloudCfg.dataDir}";
in
mkBloudApp {
  name = "miniflux";
  description = "Miniflux RSS reader";
  image = "miniflux/miniflux:latest";
  port = 8085;
  network = "host";
  database = "miniflux";

  # Load secrets from env file: DATABASE_URL, OAUTH_CLIENT_SECRET
  envFile = "${secretsDir}/miniflux.env";

  options = {
    openidClientId = {
      default = "miniflux-client";
      description = "OpenID Connect client ID";
    };
    # openidClientSecret loaded from miniflux.env as OAUTH_CLIENT_SECRET
  };

  environment = cfg: {
    # DATABASE_URL loaded from envFile (includes postgres password)
    RUN_MIGRATIONS = "1";
    BASE_URL = "${cfg.externalHost}:${toString cfg.traefikPort}/embed/miniflux";
    # Listen on configured port (avoid conflict with Traefik on 8080)
    PORT = toString cfg.port;
    # SSO via Authentik - users created via OAuth, no local admin needed
    DISABLE_LOCAL_AUTH = "true";
    # Host-dependent SSO env vars (OAUTH2_OIDC_DISCOVERY_ENDPOINT, OAUTH2_REDIRECT_URL,
    # OAUTH2_CLIENT_ID, OAUTH2_CLIENT_SECRET, etc.) are written to the env file at runtime
    # by the host-agent prestart hook, using detected local IPs for dynamic host support.
  };

  # All runtime configuration handled by Go configurator:
  # apps/miniflux/configurator.go
  # - PreStart: Creates traefik SSO redirect config (when Authentik enabled)
  # - PostStart: Sets admin user theme via API
}
