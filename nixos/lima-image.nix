# NixOS configuration for Lima VM disk image
#
# Build with: nix build .#packages.aarch64-linux.lima-image
# The resulting image will be in result/nixos.img
#
# This image includes all dev tools needed for bloud development so that
# ./bloud setup doesn't need to run nixos-rebuild just for basic tooling.
#

{ config, pkgs, lib, modulesPath, ... }:

{
  imports = [
    (modulesPath + "/profiles/qemu-guest.nix")
    ./modules/lima-init.nix
  ];

  # Boot configuration
  boot.loader.grub.enable = true;
  boot.loader.grub.efiSupport = true;
  boot.loader.grub.efiInstallAsRemovable = true;
  boot.loader.grub.device = "nodev";
  boot.loader.timeout = 3;
  boot.growPartition = true;

  # Kernel and initrd for virtio
  boot.initrd.availableKernelModules = [
    "virtio_pci"
    "virtio_blk"
    "virtio_scsi"
    "virtio_net"
    "9p"
    "9pnet_virtio"
  ];

  # Filesystem - will be configured by nixos-generators
  fileSystems."/" = {
    device = "/dev/disk/by-label/nixos";
    fsType = "ext4";
    autoResize = true;
  };

  # Network configuration
  networking = {
    hostName = "bloud-dev";
    useDHCP = true;
    firewall.enable = false;  # Disable for dev
  };

  # Lima user for SSH access (Lima expects this)
  users.users.lima = {
    isNormalUser = true;
    description = "Lima user";
    extraGroups = [ "wheel" ];
    initialPassword = "lima";
  };

  # Bloud user for services - with linger for user services at boot
  users.users.bloud = {
    isNormalUser = true;
    description = "Bloud development user";
    extraGroups = [ "wheel" "podman" ];
    initialPassword = "bloud";
    linger = true;  # Enable user services at boot without login
  };

  # Root user with password for emergency access
  users.users.root.initialPassword = "nixos";

  # Enable passwordless sudo
  security.sudo.wheelNeedsPassword = false;

  # SSH server - essential for Lima access
  services.openssh = {
    enable = true;
    settings = {
      PermitRootLogin = "yes";
      PasswordAuthentication = true;
    };
  };

  # Podman for rootless containers (pre-configured for bloud module)
  virtualisation.podman = {
    enable = true;
    dockerCompat = true;
    defaultNetwork.settings.dns_enabled = true;
  };

  # Nix settings
  nix = {
    settings = {
      experimental-features = [ "nix-command" "flakes" ];
      trusted-users = [ "root" "lima" "bloud" ];
    };
  };

  # Git configuration - mark mounted directories as safe (9p mounts have different ownership)
  environment.etc."gitconfig".text = ''
    [safe]
      directory = *
  '';

  # Basic packages + dev tools for hot reload
  environment.systemPackages = with pkgs; [
    # Basic utilities
    vim
    git
    curl
    htop
    jq
    # Dev tools for hot reload (same as vm-dev.nix)
    tmux           # Session management
    go             # Go compiler
    air            # Go hot reload
    nodejs_22      # Node.js for Vite
  ];

  system.stateVersion = "24.11";
}
