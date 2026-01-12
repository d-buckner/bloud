{ config, pkgs, lib, ... }:

# Dev Server - HTTP API for remote NixOS development
#
# Provides HTTP endpoints for triggering nixos-rebuild from a remote laptop.
# Designed for fast development iteration: rsync code -> curl rebuild -> see output
#
# Usage in /etc/nixos/configuration.nix:
#
#   imports = [ /path/to/bloud-v3/nixos/modules/dev-server.nix ];
#
#   bloud.dev-server = {
#     enable = true;
#     port = 9999;
#     flakePath = "/home/daniel/bloud-test";
#     hostname = "bloud-test";  # Optional: for flake#hostname builds
#   };
#

let
  cfg = config.bloud.dev-server;

  # Build the dev-server Go binary
  devServerBin = pkgs.buildGoModule {
    pname = "bloud-dev-server";
    version = "0.1.0";
    src = ../../services/dev-server;
    vendorHash = null; # No dependencies
  };
in
{
  options.bloud.dev-server = {
    enable = lib.mkEnableOption "Bloud dev server for remote NixOS development";

    port = lib.mkOption {
      type = lib.types.port;
      default = 9999;
      description = "Port for the dev server HTTP API";
    };

    flakePath = lib.mkOption {
      type = lib.types.str;
      default = "/home/daniel/bloud-test";
      description = "Path to the flake directory on this machine";
    };

    hostname = lib.mkOption {
      type = lib.types.str;
      default = "";
      description = "NixOS hostname for flake builds (e.g., 'bloud-test' for .#bloud-test)";
    };

    rebuildCmd = lib.mkOption {
      type = lib.types.enum [ "switch" "test" "boot" "dry-run" ];
      default = "switch";
      description = "nixos-rebuild command to run";
    };

    user = lib.mkOption {
      type = lib.types.str;
      default = "daniel";
      description = "User to run the dev server as";
    };
  };

  config = lib.mkIf cfg.enable {
    # Systemd service for dev server
    systemd.services.bloud-dev-server = {
      description = "Bloud Dev Server - HTTP API for nixos-rebuild";
      wantedBy = [ "multi-user.target" ];
      after = [ "network.target" ];

      serviceConfig = {
        Type = "simple";
        User = cfg.user;
        ExecStart = ''
          ${devServerBin}/bin/dev-server \
            -port ${toString cfg.port} \
            -flake-path ${cfg.flakePath} \
            ${lib.optionalString (cfg.hostname != "") "-hostname ${cfg.hostname}"} \
            -rebuild-cmd ${cfg.rebuildCmd}
        '';
        Restart = "on-failure";
        RestartSec = "5s";
      };
    };

    # Allow the dev server user to run nixos-rebuild without password
    security.sudo.extraRules = [
      {
        users = [ cfg.user ];
        commands = [
          {
            command = "/run/current-system/sw/bin/nixos-rebuild";
            options = [ "NOPASSWD" ];
          }
        ];
      }
    ];

    # Open firewall port (optional - for remote access)
    networking.firewall.allowedTCPPorts = [ cfg.port ];

    # Helper command
    environment.systemPackages = [
      (pkgs.writeShellScriptBin "bloud-dev-status" ''
        echo "Bloud Dev Server"
        echo "================"
        echo ""
        echo "Service status:"
        systemctl status bloud-dev-server --no-pager
        echo ""
        echo "API endpoint: http://localhost:${toString cfg.port}"
        echo ""
        echo "Endpoints:"
        echo "  POST /rebuild  - Trigger rebuild (streams output)"
        echo "  GET  /status   - Get current status"
        echo "  GET  /health   - Health check"
        echo ""
        echo "Quick test:"
        echo "  curl http://localhost:${toString cfg.port}/health"
      '')
    ];
  };
}
