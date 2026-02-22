{ config, pkgs, lib, ... }:

let
  cfg = config.bloud.installer;
  defaultPackage = pkgs.callPackage ../packages/installer.nix {};
  pkg = cfg.package;
in
{
  options.bloud.installer = {
    enable = lib.mkEnableOption "Bloud installer service";

    package = lib.mkOption {
      type = lib.types.package;
      default = defaultPackage;
      description = "The bloud-installer package to use";
    };

    port = lib.mkOption {
      type = lib.types.int;
      default = 3001;
      description = "HTTP port for the installer API and web UI";
    };

    flakePath = lib.mkOption {
      type = lib.types.str;
      default = "${pkg}/share/bloud-installer/bloud";
      description = "Path to the bloud flake used by nixos-install";
    };

    systemPath = lib.mkOption {
      type = lib.types.str;
      default = "";
      description = "Pre-built NixOS system store path. When set, nixos-install uses --system instead of --flake, bypassing flake re-evaluation.";
    };
  };

  config = lib.mkIf cfg.enable {
    systemd.services.bloud-installer = {
      description = "Bloud Installer";
      wantedBy = [ "multi-user.target" ];
      after = [ "network-online.target" ];
      wants = [ "network-online.target" ];

      # NixOS `path` is the correct idiom for adding packages to a service PATH.
      # Setting PATH via `environment` causes Nix evaluation errors with store paths.
      path = with pkgs; [ parted util-linux dosfstools e2fsprogs cryptsetup nix ];

      environment = {
        INSTALLER_PORT = toString cfg.port;
        INSTALLER_FLAKE_PATH = cfg.flakePath;
        INSTALLER_SYSTEM_PATH = cfg.systemPath;
      };

      serviceConfig = {
        Type = "simple";
        # Must run as root to partition disks and run nixos-install
        User = "root";
        ExecStart = "${pkg}/bin/bloud-installer";
        # The installer serves web/build relative to WorkingDirectory
        WorkingDirectory = "${pkg}/share/bloud-installer";
        Restart = "always";
        RestartSec = "5s";
        StandardOutput = "journal";
        StandardError = "journal";
      };
    };
  };
}
