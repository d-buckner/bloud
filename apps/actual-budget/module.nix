{ config, pkgs, lib, ... }:

let
  mkBloudApp = import ../../nixos/lib/bloud-app.nix { inherit config pkgs lib; };
  bloudCfg = config.bloud;
  secretsDir = "/home/${bloudCfg.user}/.local/share/${bloudCfg.dataDir}";
  authentikCfg = config.bloud.apps.authentik;
  authentikEnabled = authentikCfg.enable or false;
in
mkBloudApp {
  name = "actual-budget";
  description = "Actual Budget";
  image = "actualbudget/actual-server:latest";
  port = 5006;
  containerPort = 5006;
  network = "host";
  dataDir = "/data";

  # Load secrets from env file: ACTUAL_OPENID_CLIENT_SECRET
  envFile = "${secretsDir}/actual-budget.env";

  # Depend on Authentik when SSO is enabled
  # The configurator's PreStart waits for the OpenID endpoint
  dependsOn = lib.optionals authentikEnabled [ "apps-authentik-server" ];

  options = {
    openidDiscoveryUrl = {
      default = "http://apps-authentik-proxy/application/o/actual-budget/.well-known/openid-configuration";
      description = "OpenID Connect discovery URL";
    };
    openidClientId = {
      default = "actual-budget-client";
      description = "OpenID Connect client ID";
    };
    # openidClientSecret loaded from actual-budget.env as ACTUAL_OPENID_CLIENT_SECRET
  };

  environment = cfg: {
    ACTUAL_SERVER_URL = "${cfg.externalHost}:${toString cfg.traefikPort}/embed/actual-budget";
  } // lib.optionalAttrs authentikEnabled {
    # Skip Actual Budget's own login - use Authentik only
    ACTUAL_OPENID_ENFORCE = "true";
    # Host-dependent SSO env vars (ACTUAL_OPENID_DISCOVERY_URL, ACTUAL_OPENID_CLIENT_ID,
    # ACTUAL_OPENID_CLIENT_SECRET, ACTUAL_OPENID_SERVER_HOSTNAME) are written to the env file
    # at runtime by the host-agent prestart hook, using detected local IPs.
  };
}
