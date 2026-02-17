{ config, pkgs, lib, ... }:

let
  cfg = config.bloud.host-agent;
  bloudCfg = config.bloud;
  userHome = "/home/${bloudCfg.user}";
  dataDir = "${userHome}/.local/share/${bloudCfg.dataDir}";

  defaultPackage = pkgs.callPackage ../packages/host-agent.nix {};

  pkg = cfg.package;

in
{
  options.bloud.host-agent = {
    enable = lib.mkEnableOption "Bloud host agent service";

    package = lib.mkOption {
      type = lib.types.package;
      default = defaultPackage;
      description = "The bloud host-agent package to use";
    };

    port = lib.mkOption {
      type = lib.types.int;
      default = 3000;
      description = "HTTP port for web UI and API";
    };

    flakeTarget = lib.mkOption {
      type = lib.types.str;
      default = "dev-server";
      description = "Flake target for nixos-rebuild (e.g., 'dev-server', 'iso')";
    };

    sourceDir = lib.mkOption {
      type = lib.types.str;
      default = "${pkg}/share/bloud";
      description = "Path to the bloud source tree (apps, nixos modules, flake)";
    };
  };

  config = lib.mkIf cfg.enable {
    # Create data directories
    system.activationScripts.bloud-host-agent-dirs = lib.stringAfter [ "users" ] ''
      mkdir -p ${dataDir}/{nix,catalog}
      chown -R ${bloudCfg.user}:users ${dataDir}
    '';

    # systemd service (system-wide, NOT user service)
    # Runs as user but system-wide so it can manage system state
    systemd.services.bloud-host-agent = {
      description = "Bloud Host Agent - App Management & Web UI";
      after = [ "network-online.target" "bloud-user-services.service" ];
      wants = [ "network-online.target" ];
      wantedBy = [ "multi-user.target" ];

      environment = {
        BLOUD_PORT = toString cfg.port;
        BLOUD_DATA_DIR = dataDir;
        # DATABASE_URL is built by host-agent from secrets.json at runtime
        BLOUD_APPS_DIR = "${cfg.sourceDir}/apps";
        BLOUD_FLAKE_PATH = cfg.sourceDir;
        BLOUD_NIXOS_PATH = "${cfg.sourceDir}/nixos";
        BLOUD_FLAKE_TARGET = cfg.flakeTarget;
        BLOUD_SSO_BASE_URL = bloudCfg.externalHost;
        BLOUD_SSO_AUTHENTIK_URL = bloudCfg.authentikExternalHost;
      };

      serviceConfig = {
        Type = "simple";
        User = bloudCfg.user;
        Group = "users";

        ExecStartPre = pkgs.writeShellScript "bloud-host-agent-wait-db" ''
          set -e
          echo "Waiting for PostgreSQL and bloud database..."
          for i in $(seq 1 120); do
            if ${pkgs.podman}/bin/podman exec apps-postgres psql -U apps -d bloud -c "SELECT 1" > /dev/null 2>&1; then
              echo "Database ready"
              exit 0
            fi
            sleep 1
          done
          echo "Timeout waiting for database"
          exit 1
        '';

        ExecStart = "${pkg}/bin/host-agent";
        Restart = "always";
        RestartSec = "10s";

        # Working directory for web/build/ lookup
        WorkingDirectory = "${cfg.sourceDir}";

        # Allow service to continue during system shutdown
        KillMode = "mixed";

        # Security (relaxed for development, will harden later)
        NoNewPrivileges = false;

        # Logging
        StandardOutput = "journal";
        StandardError = "journal";
      };
    };

    # Allow user to run nixos-rebuild without password
    security.sudo.extraRules = [
      {
        users = [ bloudCfg.user ];
        commands = [
          {
            command = "${pkgs.nixos-rebuild}/bin/nixos-rebuild";
            options = [ "NOPASSWD" ];
          }
        ];
      }
    ];

    # Add helper commands to systemPackages
    environment.systemPackages = [
      (pkgs.writeShellScriptBin "bloud-host-agent-status" ''
        echo "╔═══════════════════════════════════════════════════╗"
        echo "║           Bloud Host Agent Status                ║"
        echo "╚═══════════════════════════════════════════════════╝"
        echo ""
        systemctl status bloud-host-agent.service --no-pager
        echo ""
        echo "Web UI: http://localhost:${toString cfg.port}"
        echo "API:    http://localhost:${toString cfg.port}/api/health"
        echo ""
        echo "Logs (last 20 lines):"
        journalctl -u bloud-host-agent.service -n 20 --no-pager
      '')

      (pkgs.writeShellScriptBin "bloud-host-agent-logs" ''
        journalctl -u bloud-host-agent.service -f
      '')

      (pkgs.writeShellScriptBin "bloud-host-agent-restart" ''
        sudo systemctl restart bloud-host-agent.service
        sleep 2
        bloud-host-agent-status
      '')
    ];
  };
}
