# Native NixOS configuration for Proxmox VM development
#
# Full development environment for running Bloud directly on a Proxmox VM.
# Develop via SSH or VS Code Remote. Host-agent is compiled on the server.
#
# Deployment:
#   1. Clone repo to the VM
#   2. Build host-agent: cd services/host-agent && go build -o /tmp/host-agent ./cmd/host-agent
#   3. Apply config: sudo nixos-rebuild switch --flake .#dev-server
#
# Note: Filesystem config may need adjustment based on your VM's disk layout.
# Check with `lsblk` and update fileSystems accordingly.

{ config, pkgs, lib, modulesPath, ... }:

{
  imports = [
    # QEMU guest profile for Proxmox/KVM integration
    (modulesPath + "/profiles/qemu-guest.nix")
  ]
  # Import generated config from either:
  # 1. Project's generated/ directory (for testing with git)
  # 2. User's data directory (for API-generated config, requires --impure)
  ++ lib.optional (builtins.pathExists ./generated/apps.nix) ./generated/apps.nix
  ++ lib.optional (builtins.pathExists /home/bloud/.local/share/bloud/nix/apps.nix) /home/bloud/.local/share/bloud/nix/apps.nix;

  # QEMU guest agent for Proxmox integration (shutdown, snapshots, etc.)
  services.qemuGuest.enable = true;

  # Boot configuration - BIOS mode (adjust if using EFI)
  # For EFI: set device = "nodev" and enable efiSupport + efiInstallAsRemovable
  boot.loader.grub = {
    enable = true;
    device = "/dev/vda";  # Adjust based on your disk (use lsblk to verify)
  };
  boot.loader.timeout = 3;

  # Kernel modules for virtio
  boot.initrd.availableKernelModules = [
    "virtio_pci"
    "virtio_blk"
    "virtio_scsi"
    "virtio_net"
    "ahci"
    "sd_mod"
  ];

  # Filesystem - adjust based on your VM disk layout
  # Run `lsblk` and `blkid` to find correct device paths
  fileSystems."/" = {
    device = "/dev/vda1";  # Or use by-label: "/dev/disk/by-label/nixos"
    fsType = "ext4";
  };

  # Uncomment if using EFI boot:
  # fileSystems."/boot" = {
  #   device = "/dev/vda2";  # Or by-label: "/dev/disk/by-label/ESP"
  #   fsType = "vfat";
  # };

  # Network configuration
  networking = {
    hostName = "dev-server";
    useDHCP = true;

    # Open firewall for all bloud services
    firewall = {
      enable = true;
      allowedTCPPorts = [
        22    # SSH
        3000  # Host Agent API
        5173  # Vite Dev Server
        8080  # Traefik (main entry point)
        8081  # Traefik additional
        8082  # Traefik additional
        8085  # Miniflux direct
        9001  # Authentik
        9443  # Authentik HTTPS
        5006  # Actual Budget direct
        9999  # Dev server API (if enabled)
      ];
    };
  };

  # Bloud user - dedicated service user
  users.users.bloud = {
    isNormalUser = true;
    description = "Bloud development user";
    extraGroups = [ "wheel" "podman" ];
    # Default password for initial setup (change via passwd)
    initialPassword = "bloud";
    # Enable lingering so user services start at boot
    linger = true;

    # SSH key authentication - add your public key here
    openssh.authorizedKeys.keys = [
      # Add your SSH public key(s) here, e.g.:
      # "ssh-ed25519 AAAA... user@host"
    ];
  };

  # System service to start user services after boot
  # Uses machinectl to properly trigger the user's systemd instance
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

  # SSH server
  services.openssh = {
    enable = true;
    settings = {
      PermitRootLogin = "no";
      PasswordAuthentication = true;  # For initial setup, disable after adding SSH keys
    };
  };

  # Bloud infrastructure
  bloud = {
    enable = true;
    user = "bloud";
    agentPath = "/tmp/host-agent";  # Build with: go build -o /tmp/host-agent ./cmd/host-agent
  };

  # Nix settings - enable flakes
  nix = {
    settings = {
      experimental-features = [ "nix-command" "flakes" ];
      trusted-users = [ "root" "bloud" ];
    };
  };

  # Development packages
  environment.systemPackages = with pkgs; [
    # Dev tools
    go
    air            # Go hot reload
    nodejs_22      # Node.js for Vite/frontend
    tmux           # Session management
    git
    vim
    htop
    jq
    curl
    wget

    # Debugging
    lsof
    netcat
    tcpdump
  ];

  # Git configuration - mark mounted directories as safe
  environment.etc."gitconfig".text = ''
    [safe]
      directory = *
  '';

  # System state version
  system.stateVersion = "24.11";
}
