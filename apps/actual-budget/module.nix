{ config, pkgs, lib, ... }:

let
  mkBloudApp = import ../../nixos/lib/bloud-app.nix { inherit config pkgs lib; };
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
    openidClientSecret = {
      default = "actual-budget-secret-change-in-production";
      description = "OpenID Connect client secret";
    };
  };

  environment = cfg: {
    ACTUAL_SERVER_URL = "${cfg.externalHost}:${toString cfg.traefikPort}/embed/actual-budget";
  } // lib.optionalAttrs authentikEnabled {
    # Discovery URL uses auth subdomain to avoid SW rewriting
    ACTUAL_OPENID_DISCOVERY_URL = "${cfg.authentikExternalHost}:${toString cfg.traefikPort}/application/o/actual-budget/.well-known/openid-configuration";
    ACTUAL_OPENID_CLIENT_ID = cfg.openidClientId;
    ACTUAL_OPENID_CLIENT_SECRET = cfg.openidClientSecret;
    ACTUAL_OPENID_SERVER_HOSTNAME = "${cfg.externalHost}:${toString cfg.traefikPort}/embed/actual-budget";
    # Skip Actual Budget's own login - use Authentik only
    ACTUAL_OPENID_ENFORCE = "true";
  };
}
