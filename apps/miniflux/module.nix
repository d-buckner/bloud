{ config, pkgs, lib, ... }:

let
  mkBloudApp = import ../../nixos/lib/bloud-app.nix { inherit config pkgs lib; };
  authentikCfg = config.bloud.apps.authentik;
  authentikEnabled = authentikCfg.enable or false;
in
mkBloudApp {
  name = "miniflux";
  description = "Miniflux RSS reader";
  image = "miniflux/miniflux:latest";
  port = 8085;
  network = "host";
  database = "miniflux";

  options = {
    adminUsername = {
      default = "admin";
      description = "Admin username for initial setup";
    };
    adminPassword = {
      default = "admin123";
      description = "Admin password for initial setup";
    };
    openidClientId = {
      default = "miniflux-client";
      description = "OpenID Connect client ID";
    };
    openidClientSecret = {
      default = "miniflux-secret-change-in-production";
      description = "OpenID Connect client secret";
    };
  };

  environment = cfg: {
    # Connect to postgres via localhost (host networking)
    DATABASE_URL = "postgres://${cfg.postgresUser}:${cfg.postgresPassword}@localhost:5432/miniflux?sslmode=disable";
    RUN_MIGRATIONS = "1";
    CREATE_ADMIN = "1";
    ADMIN_USERNAME = cfg.adminUsername;
    ADMIN_PASSWORD = cfg.adminPassword;
    BASE_URL = "${cfg.externalHost}:${toString cfg.traefikPort}/embed/miniflux";
    # Listen on configured port (avoid conflict with Traefik on 8080)
    PORT = toString cfg.port;
  } // lib.optionalAttrs authentikEnabled {
    # Hide local login form when SSO is enabled
    DISABLE_LOCAL_AUTH = "true";
    # SSO configuration when Authentik is enabled
    OAUTH2_PROVIDER = "oidc";
    OAUTH2_CLIENT_ID = cfg.openidClientId;
    OAUTH2_CLIENT_SECRET = cfg.openidClientSecret;
    # Discovery endpoint uses auth subdomain to avoid SW rewriting
    OAUTH2_OIDC_DISCOVERY_ENDPOINT = "${cfg.authentikExternalHost}:${toString cfg.traefikPort}/application/o/miniflux/";
    OAUTH2_REDIRECT_URL = "${cfg.externalHost}:${toString cfg.traefikPort}/embed/miniflux/oauth2/oidc/callback";
    OAUTH2_OIDC_PROVIDER_NAME = "Bloud SSO";
    OAUTH2_USER_CREATION = "1";
  };

  # All runtime configuration handled by Go configurator:
  # apps/miniflux/configurator.go
  # - PreStart: Creates traefik SSO redirect config (when Authentik enabled)
  # - PostStart: Sets admin user theme via API
}
