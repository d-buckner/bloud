# NixOS configuration for Proxmox VM template
#
# Build with: nix build .#proxmox-image
# The resulting image will be in result/vzdump-qemu-nixos-*.vma.zst
#
# Deploy to Proxmox:
#   scp result/*.vma.zst root@proxmox:/var/lib/vz/dump/
#   qmrestore /var/lib/vz/dump/vzdump-qemu-nixos-*.vma.zst <vmid> --unique true
#   qm template <vmid>  # Convert to template after verifying it boots
#

{ config, pkgs, lib, modulesPath, ... }:

{
  imports = [
    (modulesPath + "/profiles/qemu-guest.nix")
  ];

  # Proxmox-specific settings via nixos-generators proxmox format
  proxmox = {
    qemuConf = {
      cores = 2;
      memory = 4096;
      name = "nixos-template";
      net0 = "virtio=00:00:00:00:00:00,bridge=vmbr0,firewall=1";
    };
    cloudInit = {
      enable = true;
      defaultStorage = "local-lvm";
    };
  };

  # Boot and filesystem are configured by nixos-generators proxmox format
  # Only add extra kernel modules if needed
  boot.initrd.availableKernelModules = [
    "virtio_pci"
    "virtio_blk"
    "virtio_scsi"
    "virtio_net"
  ];

  # Cloud-init for network configuration from Proxmox
  services.cloud-init = {
    enable = true;
    network.enable = true;
  };

  # QEMU guest agent for Proxmox integration
  services.qemuGuest.enable = true;

  # Network configuration - cloud-init will configure interfaces
  networking = {
    hostName = "nixos";
    useDHCP = lib.mkDefault true;
    firewall.enable = false;  # Disable for initial setup, enable per-VM as needed
  };

  # Bloud user - primary user for services
  users.users.bloud = {
    isNormalUser = true;
    description = "Bloud user";
    extraGroups = [ "wheel" "podman" ];
    initialPassword = "bloud";
    linger = true;  # Enable user services at boot without login
    openssh.authorizedKeys.keys = [
      "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINzG+O7pmz+SfpDZPTlYUs2+7SzjoK/KqwyecLF6YWno daniel@d-buckner.org"
    ];
  };

  # Root user with password for emergency console access
  users.users.root.initialPassword = "nixos";

  # Enable passwordless sudo
  security.sudo.wheelNeedsPassword = false;

  # SSH server
  services.openssh = {
    enable = true;
    settings = {
      PermitRootLogin = "yes";
      PasswordAuthentication = true;  # Disable after first setup if desired
    };
  };

  # Podman for rootless containers
  virtualisation.podman = {
    enable = true;
    dockerCompat = true;
    defaultNetwork.settings.dns_enabled = true;
  };

  # Nix settings
  nix = {
    settings = {
      experimental-features = [ "nix-command" "flakes" ];
      trusted-users = [ "root" "bloud" ];
    };
  };

  # Basic packages
  environment.systemPackages = with pkgs; [
    vim
    git
    curl
    htop
    jq
    tmux
  ];

  system.stateVersion = "24.11";
}
