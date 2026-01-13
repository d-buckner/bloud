# VM-specific NixOS configuration for integration testing via Lima
#
# This module configures NixOS to run in a separate Lima VM for testing.
# It uses different ports from the dev VM to allow parallel execution.
#
# Test ports:
#   - 3001: Go API (instead of 3000)
#   - 5174: Vite (instead of 5173)
#   - 8081: Traefik (instead of 8080)
#
# After first boot, run: sudo nixos-rebuild switch --flake /home/bloud.linux/bloud-v3#vm-test

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

  # Network configuration - TEST VM (different hostname)
  networking = {
    hostName = "bloud-test";
    useDHCP = true;

    # Open firewall for TEST ports
    firewall = {
      enable = true;
      allowedTCPPorts = [
        22    # SSH
        3001  # Host Agent API (test)
        5174  # Web Dev Server (test)
        8081  # Traefik (test)
      ];
    };
  };

  # Bloud user - dedicated service user for the VM
  users.users.bloud = {
    isNormalUser = true;
    description = "Bloud test user";
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

  # Enable Bloud infrastructure
  bloud = {
    enable = true;
    user = "bloud";
    agentPath = "/tmp/host-agent-test";  # Test VM uses different binary path
    # Uses default dataDir "bloud" - VMs are separate so no collision risk
  };

  # Enable Traefik on test ports
  bloud.apps.traefik = {
    enable = true;
    port = 8081;     # Traefik entrypoint (dev: 8080)
    apiPort = 3001;  # Host-agent API (dev: 3000)
    uiPort = 5174;   # Vite dev server (dev: 5173)
  };

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

  # System state version
  system.stateVersion = "24.11";
}
