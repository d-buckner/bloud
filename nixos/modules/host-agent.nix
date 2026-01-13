{ config, pkgs, lib, ... }:

let
  cfg = config.bloud.host-agent;
  postgresCfg = config.bloud.apps.postgres;

  userHome = "/home/${cfg.user}";
  defaultDataDir = "${userHome}/.local/share/bloud";

  # Build database URL from postgres config
  databaseURL = "postgres://${postgresCfg.user}:${postgresCfg.password}@localhost:5432/bloud?sslmode=disable";

  # For initial development, we'll use a manually built binary
  # The binary should be built and placed at /tmp/host-agent
  # Later: Use buildGoModule for proper Nix packaging
  host-agent-dev = pkgs.writeShellScriptBin "host-agent-dev" ''
    # Check if the manually built binary exists
    if [ ! -f /tmp/host-agent ]; then
      echo "ERROR: host-agent binary not found at /tmp/host-agent"
      echo "Please build it first: cd services/host-agent && go build -o /tmp/host-agent ./cmd/host-agent"
      exit 1
    fi

    exec /tmp/host-agent
  '';

in
{
  options.bloud.host-agent = {
    enable = lib.mkEnableOption "Bloud host agent service";

    user = lib.mkOption {
      type = lib.types.str;
      default = "daniel";
      description = "User to run host agent as";
    };

    port = lib.mkOption {
      type = lib.types.int;
      default = 8080;
      description = "HTTP port for web UI and API";
    };

    dataDir = lib.mkOption {
      type = lib.types.str;
      default = defaultDataDir;
      description = "Directory for host agent data (configs, catalog)";
    };
  };

  config = lib.mkIf cfg.enable {
    # Create data directories
    system.activationScripts.bloud-host-agent-dirs = lib.stringAfter [ "users" ] ''
      mkdir -p ${cfg.dataDir}/{nixos/apps,catalog}
      chown -R ${cfg.user}:users ${cfg.dataDir}
    '';

    # Database initialization service
    systemd.services.bloud-host-agent-db-init = {
      description = "Initialize bloud host-agent database";
      after = [ "podman-apps-postgres.service" ];
      requires = [ "podman-apps-postgres.service" ];
      before = [ "bloud-host-agent.service" ];
      wantedBy = [ "multi-user.target" ];

      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        User = cfg.user;
        Group = "users";
        ExecStart = pkgs.writeShellScript "bloud-db-init" ''
          set -e

          # Wait for postgres to be ready
          echo "Waiting for PostgreSQL to be ready..."
          for i in $(seq 1 30); do
            if ${pkgs.podman}/bin/podman exec apps-postgres pg_isready -U ${postgresCfg.user} > /dev/null 2>&1; then
              echo "PostgreSQL is ready"
              break
            fi
            if [ $i -eq 30 ]; then
              echo "Timeout waiting for PostgreSQL"
              exit 1
            fi
            sleep 2
          done

          # Create database if not exists
          if ! ${pkgs.podman}/bin/podman exec apps-postgres psql -U ${postgresCfg.user} -tc "SELECT 1 FROM pg_database WHERE datname = 'bloud'" | grep -q 1; then
            echo "Creating bloud database..."
            ${pkgs.podman}/bin/podman exec apps-postgres psql -U ${postgresCfg.user} -c "CREATE DATABASE bloud"
            ${pkgs.podman}/bin/podman exec apps-postgres psql -U ${postgresCfg.user} -c "GRANT ALL PRIVILEGES ON DATABASE bloud TO ${postgresCfg.user}"
            echo "Database created successfully"
          else
            echo "Database bloud already exists"
          fi
        '';
      };
    };

    # systemd service (system-wide, NOT user service)
    # Runs as user but system-wide so it can manage system state
    systemd.services.bloud-host-agent = {
      description = "Bloud Host Agent - App Management & Web UI";
      after = [ "network-online.target" "bloud-host-agent-db-init.service" ];
      wants = [ "network-online.target" ];
      requires = [ "bloud-host-agent-db-init.service" ];
      wantedBy = [ "multi-user.target" ];

      environment = {
        BLOUD_PORT = toString cfg.port;
        BLOUD_DATA_DIR = cfg.dataDir;
        DATABASE_URL = databaseURL;
      };

      serviceConfig = {
        Type = "simple";
        User = cfg.user;
        Group = "users";
        ExecStart = "${host-agent-dev}/bin/host-agent-dev";
        Restart = "always";
        RestartSec = "10s";

        # Working directory
        WorkingDirectory = userHome;

        # Allow service to continue during system shutdown
        KillMode = "mixed";

        # Security (relaxed for development, will harden later)
        NoNewPrivileges = false;

        # Logging
        StandardOutput = "journal";
        StandardError = "journal";
      };
    };

    # Allow user to run nixos-rebuild without password (for future app installation feature)
    security.sudo.extraRules = [
      {
        users = [ cfg.user ];
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
      # Status checker
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

      # Log viewer
      (pkgs.writeShellScriptBin "bloud-host-agent-logs" ''
        journalctl -u bloud-host-agent.service -f
      '')

      # Restart helper
      (pkgs.writeShellScriptBin "bloud-host-agent-restart" ''
        sudo systemctl restart bloud-host-agent.service
        sleep 2
        bloud-host-agent-status
      '')
    ];
  };
}
