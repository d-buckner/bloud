{ config, pkgs, lib, ... }:

let
  mkBloudApp = import ../../nixos/lib/bloud-app.nix { inherit config pkgs lib; };
  bloudCfg = config.bloud;
  secretsDir = "/home/${bloudCfg.user}/.local/share/${bloudCfg.dataDir}";
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

  # Load secrets from env file: DATABASE_URL, OAUTH_OIDC_CLIENT_SECRET
  envFile = "${secretsDir}/affine.env";

  options = {
    openidClientId = {
      default = "affine-client";
      description = "OpenID Connect client ID";
    };
    # openidClientSecret loaded from affine.env as OAUTH_OIDC_CLIENT_SECRET
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
    # DATABASE_URL loaded from envFile (includes postgres password)
    AFFINE_INDEXER_ENABLED = "false";
    AFFINE_SERVER_EXTERNAL_URL = "${cfg.externalHost}:${toString cfg.traefikPort}/embed/affine";
  } // lib.optionalAttrs authentikEnabled {
    # Static OIDC settings (not host-dependent)
    OAUTH_OIDC_ENABLED = "true";
    OAUTH_OIDC_SCOPE = "openid email profile offline_access";
    OAUTH_OIDC_CLAIM_MAP_ID = "sub";
    OAUTH_OIDC_CLAIM_MAP_EMAIL = "email";
    OAUTH_OIDC_CLAIM_MAP_NAME = "name";
    # Host-dependent SSO env vars (OAUTH_OIDC_ISSUER, OAUTH_OIDC_CLIENT_ID,
    # OAUTH_OIDC_CLIENT_SECRET) are written to the env file at runtime
    # by the host-agent prestart hook, using detected local IPs.
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
        # Load DATABASE_URL from env file for migrations
        ExecStart = pkgs.writeShellScript "affine-migration" ''
          set -e
          echo "Running AFFiNE database migrations..."
          ${pkgs.podman}/bin/podman rm -f affine-migration 2>/dev/null || true
          ${pkgs.podman}/bin/podman run --rm \
            --name affine-migration \
            --network apps-net \
            --env-file=${secretsDir}/affine.env \
            -e REDIS_SERVER_HOST=apps-redis \
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
