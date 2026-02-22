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
      default = "/etc/bloud";
      description = "Path to the bloud flake used by nixos-install";
    };
  };

  config = lib.mkIf cfg.enable {
    systemd.services.bloud-installer = {
      description = "Bloud Installer";
      wantedBy = [ "multi-user.target" ];
      after = [ "network-online.target" ];
      wants = [ "network-online.target" ];

      environment = {
        INSTALLER_PORT = toString cfg.port;
        # systemd strips PATH — explicitly include disk tools and nix
        # nixos-install is at /run/current-system/sw/bin on NixOS
        PATH = lib.concatStringsSep ":" [
          "${pkgs.parted}/bin"
          "${pkgs.util-linux}/bin"      # wipefs, blkid, mount, umount
          "${pkgs.dosfstools}/bin"      # mkfs.vfat
          "${pkgs.e2fsprogs}/bin"       # mkfs.ext4
          "${pkgs.cryptsetup}/bin"      # LUKS
          "${pkgs.nix}/bin"
          "/run/current-system/sw/bin"  # nixos-install, systemctl, etc.
          "/run/wrappers/bin"           # sudo
          "/usr/bin"
          "/bin"
        ];
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
