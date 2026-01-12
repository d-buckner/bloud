# NixOS configuration for Lima VM disk image
#
# Build with: nix build .#packages.aarch64-linux.lima-image
# The resulting image will be in result/nixos.img
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

  # Also create bloud user for our services
  users.users.bloud = {
    isNormalUser = true;
    description = "Bloud development user";
    extraGroups = [ "wheel" "podman" ];
    initialPassword = "bloud";
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

  # Nix settings
  nix = {
    settings = {
      experimental-features = [ "nix-command" "flakes" ];
      trusted-users = [ "root" "lima" "bloud" ];
    };
  };

  # Basic packages
  environment.systemPackages = with pkgs; [
    vim
    git
    curl
    htop
    jq
  ];

  system.stateVersion = "24.11";
}
