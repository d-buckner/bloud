# VM-specific NixOS configuration for local development via Lima
#
# This module configures NixOS to run in a Lima VM on macOS.
# After first boot, run: sudo nixos-rebuild switch --flake /home/bloud.linux/bloud#vm-dev
#

{ config, pkgs, lib, modulesPath, ... }:

{
  imports = [
    # Include default QEMU guest configuration
    (modulesPath + "/profiles/qemu-guest.nix")
    # Lima integration
    ./modules/lima-init.nix
  ]
  # Import generated config from either:
  # 1. Project's generated/ directory (for testing with git)
  # 2. User's data directory (for API-generated config, requires --impure)
  ++ lib.optional (builtins.pathExists ./generated/apps.nix) ./generated/apps.nix
  ++ lib.optional (builtins.pathExists /home/bloud/.local/share/bloud/nix/apps.nix) /home/bloud/.local/share/bloud/nix/apps.nix;

  # Boot configuration - use GRUB to match base image
  boot.loader.grub.enable = true;
  boot.loader.grub.efiSupport = true;
  boot.loader.grub.efiInstallAsRemovable = true;
  boot.loader.grub.device = "nodev";
  boot.loader.timeout = 3;

  # Kernel and initrd for virtio
  boot.initrd.availableKernelModules = [
    "virtio_pci"
    "virtio_blk"
    "virtio_scsi"
    "virtio_net"
    "9p"
    "9pnet_virtio"
  ];

  # Filesystems - Lima typically uses virtio disk at /dev/vda
  fileSystems."/" = {
    device = "/dev/disk/by-label/nixos";
    fsType = "ext4";
  };

  fileSystems."/boot" = {
    device = "/dev/disk/by-label/ESP";
    fsType = "vfat";
  };

  # Network configuration
  networking = {
    hostName = "bloud-dev";
    useDHCP = true;

    # Open firewall for all bloud services
    firewall = {
      enable = true;
      allowedTCPPorts = [
        22    # SSH
        3000  # Host Agent API
        5173  # Web Dev Server
        8080  # Traefik dashboard
        8081  # Traefik budget
        8082  # Traefik RSS
        8085  # Miniflux
        9001  # Authentik
        9443  # Authentik HTTPS
        5006  # Actual Budget
      ];
    };
  };

  # Bloud user - dedicated service user for the VM
  users.users.bloud = {
    isNormalUser = true;
    description = "Bloud development user";
    extraGroups = [ "wheel" "podman" ];
    # Default password for development (change via passwd after first boot)
    initialPassword = "bloud";
    # Enable lingering so user services start at boot (not just on login)
    linger = true;

    # Enable SSH key authentication
    openssh.authorizedKeys.keys = [
      # Lima will inject keys here, but you can add your own
      # "ssh-ed25519 AAAA..."
    ];
  };

  # System service to ensure user services start after boot
  # Uses machinectl to properly trigger the user's systemd instance
  # Must wait for network-online.target because user services can't depend on system targets
  systemd.services.bloud-user-services = {
    description = "Start Bloud user services";
    wantedBy = [ "multi-user.target" ];
    after = [ "systemd-user-sessions.service" "user@1000.service" "network-online.target" ];
    wants = [ "network-online.target" ];
    requires = [ "user@1000.service" ];

    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      ExecStart = pkgs.writeShellScript "start-bloud-user-services" ''
        # Reload and start bloud apps target via machinectl
        ${pkgs.systemd}/bin/machinectl shell bloud@ /run/current-system/sw/bin/systemctl --user daemon-reload || true
        ${pkgs.systemd}/bin/machinectl shell bloud@ /run/current-system/sw/bin/systemctl --user start bloud-apps.target || true
      '';
      # Re-run on rebuild to pick up new services
      ExecReload = pkgs.writeShellScript "reload-bloud-user-services" ''
        ${pkgs.systemd}/bin/machinectl shell bloud@ /run/current-system/sw/bin/systemctl --user daemon-reload || true
        ${pkgs.systemd}/bin/machinectl shell bloud@ /run/current-system/sw/bin/systemctl --user restart bloud-apps.target || true
      '';
    };
  };

  # Enable passwordless sudo for bloud user (needed for nixos-rebuild)
  security.sudo.extraRules = [
    {
      users = [ "bloud" ];
      commands = [
        {
          command = "ALL";
          options = [ "NOPASSWD" ];
        }
      ];
    }
  ];

  # SSH server for Lima access
  services.openssh = {
    enable = true;
    settings = {
      PermitRootLogin = "no";
      PasswordAuthentication = true; # For initial setup
    };
  };

  # VS Code Remote SSH support
  services.vscode-server.enable = true;

  # Enable Bloud infrastructure
  bloud = {
    enable = true;
    user = "bloud";
  };

  # Enable dev-server for remote rebuilds
  # Note: dev-server module needs to be created or imported separately
  # bloud.dev-server.enable = true;

  # Nix settings
  nix = {
    settings = {
      experimental-features = [ "nix-command" "flakes" ];
      trusted-users = [ "root" "bloud" ];
    };
  };

  # Basic packages for development
  environment.systemPackages = with pkgs; [
    vim
    git
    curl
    htop
    jq
    # Dev tools for hot reload
    tmux           # Session management
    go             # Go compiler
    air            # Go hot reload
    nodejs_22      # Node.js for Vite
  ];

  # Git configuration - mark mounted directories as safe
  environment.etc."gitconfig".text = ''
    [safe]
      directory = *
  '';

  # System state version
  system.stateVersion = "24.11";
}
