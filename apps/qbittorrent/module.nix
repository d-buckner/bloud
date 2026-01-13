{ config, pkgs, lib, ... }:

let
  mkBloudApp = import ../../nixos/lib/bloud-app.nix { inherit config pkgs lib; };
  mkPodmanService = import ../../nixos/lib/podman-service.nix { inherit pkgs lib; };
  bloudCfg = config.bloud;
  appCfg = config.bloud.apps.qbittorrent;
  configPath = "/home/${bloudCfg.user}/.local/share/${bloudCfg.dataDir}";
in
mkBloudApp {
  name = "qbittorrent";
  description = "Feature-rich BitTorrent client with Flood web UI";

  # Flood serves the UI, qBittorrent runs as backend
  image = "jesec/flood:latest";

  # Flood serves the UI on this port
  port = 8086;
  containerPort = 3000;
  containerName = "flood";
  serviceName = "apps-flood";

  environment = cfg: {
    # Flood config - disable auth (using forward-auth), connect to qBittorrent
    FLOOD_OPTION_auth = "none";
    FLOOD_OPTION_qburl = "http://apps-qbittorrent:8080";
    # qBittorrent has subnet auth bypass enabled, but Flood's schema validation
    # still requires credentials to be provided (they're not actually used)
    FLOOD_OPTION_qbuser = "mock";
    FLOOD_OPTION_qbpass = "mock";
  };

  # Flood stores its config separately from qBittorrent
  volumes = cfg: [
    "${configPath}/flood:/config:z"
  ];

  # Flood depends on qBittorrent being up
  dependsOn = [ "qbittorrent" ];

  # Flood as the main service, qBittorrent as backend
  extraServices = cfg: {
    # Init service to configure qBittorrent for container network auth bypass
    "qbittorrent-init" = {
      description = "Initialize qBittorrent config for Flood";
      before = [ "podman-apps-qbittorrent.service" ];
      wantedBy = [ "bloud-apps.target" ];
      partOf = [ "bloud-apps.target" ];
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = pkgs.writeShellScript "qbittorrent-init" ''
          set -e
          CONFIG_DIR="${configPath}/qbittorrent/qBittorrent"
          CONFIG_FILE="$CONFIG_DIR/qBittorrent.conf"

          mkdir -p "$CONFIG_DIR"

          # Create initial config if it doesn't exist
          if [ ! -f "$CONFIG_FILE" ]; then
            echo "Creating initial qBittorrent config..."
            cat > "$CONFIG_FILE" << 'CONF'
[Preferences]
WebUI\AuthSubnetWhitelistEnabled=true
WebUI\AuthSubnetWhitelist=10.0.0.0/8, 172.16.0.0/12
WebUI\LocalHostAuth=false
CONF
          else
            # Ensure auth bypass is enabled for existing config
            if ! grep -q "AuthSubnetWhitelistEnabled=true" "$CONFIG_FILE"; then
              echo "Enabling subnet auth bypass in existing config..."
              if grep -q "\[Preferences\]" "$CONFIG_FILE"; then
                sed -i '/\[Preferences\]/a WebUI\\AuthSubnetWhitelistEnabled=true\nWebUI\\AuthSubnetWhitelist=10.0.0.0/8, 172.16.0.0/12\nWebUI\\LocalHostAuth=false' "$CONFIG_FILE"
              else
                echo -e "\n[Preferences]\nWebUI\\AuthSubnetWhitelistEnabled=true\nWebUI\\AuthSubnetWhitelist=10.0.0.0/8, 172.16.0.0/12\nWebUI\\LocalHostAuth=false" >> "$CONFIG_FILE"
              fi
            fi
          fi

          echo "qBittorrent config initialized"
        '';
      };
    };

    # qBittorrent backend (no external port - only accessible on container network)
    "podman-apps-qbittorrent" = mkPodmanService {
      name = "apps-qbittorrent";
      image = "linuxserver/qbittorrent:latest";
      environment = {
        PUID = "1000";
        PGID = "1000";
        TZ = "Etc/UTC";
        WEBUI_PORT = "8080";
      };
      volumes = [
        "${configPath}/qbittorrent:/config:z"
        "${configPath}/downloads:/downloads:z"
      ];
      network = "apps-net";
      dependsOn = [ "apps-network" ];
      userns = "keep-id";
      extraAfter = [ "qbittorrent-init.service" ];
      extraRequires = [ "qbittorrent-init.service" ];
    };
  };

  extraConfig = cfg: {
    # Create data directories
    system.activationScripts."bloud-qbittorrent-dirs" = lib.stringAfter [ "users" ] ''
      mkdir -p ${configPath}/qbittorrent
      mkdir -p ${configPath}/flood
      mkdir -p ${configPath}/downloads
      chown -R ${bloudCfg.user}:users ${configPath}/qbittorrent
      chown -R ${bloudCfg.user}:users ${configPath}/flood
      chown -R ${bloudCfg.user}:users ${configPath}/downloads
    '';
  };
}
