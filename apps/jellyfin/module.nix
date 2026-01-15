{ config, pkgs, lib, ... }:

let
  mkBloudApp = import ../../nixos/lib/bloud-app.nix { inherit config pkgs lib; };

  # LDAP Authentication plugin for Jellyfin
  # This enables authentication via Authentik's LDAP outpost
  ldapPlugin = pkgs.fetchurl {
    url = "https://repo.jellyfin.org/files/plugin/ldap-authentication/ldap-authentication_22.0.0.0.zip";
    hash = "sha256-wjhsABvkOcmUYoCgLWJhDynjJdQJToO9MSId4/eqIK4=";
  };
in
mkBloudApp {
  name = "jellyfin";
  description = "Free software media system for streaming movies, TV, and music";

  image = "jellyfin/jellyfin:latest";
  port = 8096;

  environment = cfg: {
    PUID = "1000";
    PGID = "1000";
    TZ = "Etc/UTC";
  };

  volumes = cfg: [
    "${cfg.appDataPath}/config:/config:z"
    "${cfg.appDataPath}/cache:/cache:z"
    "${cfg.configPath}/media/movies:/movies:ro"
    "${cfg.configPath}/media/shows:/shows:ro"
  ];

  dataDir = false;

  # Enable Authentik LDAP outpost when Jellyfin is installed
  # This provides LDAP authentication for TV/mobile clients
  extraConfig = cfg: {
    bloud.apps.authentik.ldap.enable = true;
  };

  # Pre-install LDAP plugin by extracting it to the plugins directory
  extraServices = cfg: {
    jellyfin-ldap-plugin = {
      description = "Install Jellyfin LDAP plugin";
      before = [ "podman-jellyfin.service" ];
      wantedBy = [ "bloud-apps.target" ];
      partOf = [ "bloud-apps.target" ];
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = pkgs.writeShellScript "jellyfin-ldap-plugin-install" ''
          set -e
          PLUGIN_DIR="${cfg.appDataPath}/config/plugins/LDAP-Auth"

          # Create plugin directory if it doesn't exist
          mkdir -p "$PLUGIN_DIR"

          # Check if plugin already installed (by checking for the DLL)
          if [ -f "$PLUGIN_DIR/LDAP-Auth.dll" ]; then
            echo "LDAP plugin already installed"
            exit 0
          fi

          # Extract plugin zip to the plugins directory
          echo "Installing LDAP plugin..."
          ${pkgs.unzip}/bin/unzip -o ${ldapPlugin} -d "$PLUGIN_DIR"
          echo "LDAP plugin installed successfully"
        '';
      };
    };
  };
}
