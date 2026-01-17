{ config, pkgs, lib, ... }:

let
  mkBloudApp = import ../../nixos/lib/bloud-app.nix { inherit config pkgs lib; };
  authentikCfg = config.bloud.apps.authentik;
  authentikEnabled = authentikCfg.enable or false;
in
mkBloudApp {
  name = "affine";
  description = "AFFiNE knowledge base";
  image = "ghcr.io/toeverything/affine:stable";
  port = 3010;
  database = "affine";
  # AFFiNE needs to write to container paths, don't use keep-id
  userns = null;

  options = {
    openidClientId = {
      default = "affine-client";
      description = "OpenID Connect client ID";
    };
    openidClientSecret = {
      default = "affine-secret-change-in-production";
      description = "OpenID Connect client secret";
    };
  };

  # Volumes for persistent data
  # affine.js is created by configurator.go PreStart hook
  volumes = cfg: [
    "${cfg.appDataPath}/storage:/root/.affine/storage:z"
    "${cfg.appDataPath}/config:/root/.affine/config:z"
    "${cfg.appDataPath}/affine.js:/app/dist/config/affine.js:ro"
  ];

  environment = cfg: {
    REDIS_SERVER_HOST = "apps-redis";
    DATABASE_URL = "postgresql://${cfg.postgresUser}:${cfg.postgresPassword}@apps-postgres:5432/affine";
    AFFINE_INDEXER_ENABLED = "false";
    AFFINE_SERVER_EXTERNAL_URL = "${cfg.externalHost}:${toString cfg.traefikPort}/embed/affine";
  } // lib.optionalAttrs authentikEnabled {
    # OIDC configuration via environment variables
    # Use authentikExternalHost (auth.localhost) to avoid service worker URL rewriting
    OAUTH_OIDC_ENABLED = "true";
    OAUTH_OIDC_ISSUER = "${cfg.authentikExternalHost}:${toString cfg.traefikPort}/application/o/affine/";
    OAUTH_OIDC_CLIENT_ID = cfg.openidClientId;
    OAUTH_OIDC_CLIENT_SECRET = cfg.openidClientSecret;
    OAUTH_OIDC_SCOPE = "openid email profile offline_access";
    OAUTH_OIDC_CLAIM_MAP_ID = "sub";
    OAUTH_OIDC_CLAIM_MAP_EMAIL = "email";
    OAUTH_OIDC_CLAIM_MAP_NAME = "name";
  };

  dependsOn = [ "postgres" "redis" ];

  waitFor = [
    { container = "apps-postgres"; command = "pg_isready -U apps"; }
    { container = "apps-redis"; command = "redis-cli ping"; }
  ];

  extraServices = cfg: {
    affine-migration = {
      description = "Run AFFiNE database migrations";
      after = [ "affine-db-init.service" "podman-apps-redis.service" ];
      requires = [ "affine-db-init.service" ];
      before = [ "podman-affine.service" ];
      wantedBy = [ "bloud-apps.target" ];
      partOf = [ "bloud-apps.target" ];
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        TimeoutStartSec = 300;
        ExecStart = pkgs.writeShellScript "affine-migration" ''
          set -e
          echo "Running AFFiNE database migrations..."
          ${pkgs.podman}/bin/podman rm -f affine-migration 2>/dev/null || true
          ${pkgs.podman}/bin/podman run --rm \
            --name affine-migration \
            --network apps-net \
            -e REDIS_SERVER_HOST=apps-redis \
            -e "DATABASE_URL=postgresql://${cfg.postgresUser}:${cfg.postgresPassword}@apps-postgres:5432/affine" \
            -e AFFINE_INDEXER_ENABLED=false \
            ghcr.io/toeverything/affine:stable \
            node ./scripts/self-host-predeploy.js
          echo "AFFiNE migrations completed"
        '';
      };
    };
  };

  extraConfig = cfg: {
    systemd.user.services.podman-affine = {
      after = [ "affine-migration.service" ];
      requires = [ "affine-migration.service" ];
    };
  };
}
