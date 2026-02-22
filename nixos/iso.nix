# Bloud Installer ISO
#
# Minimal bootable NixOS image that runs the Bloud installer service.
# Boots to a terminal that directs users to http://bloud.local to set up.
# After installation the machine reboots into the installed Bloud system.
#
# Build with: nix build .#packages.x86_64-linux.iso
#
# To build the pre-built artifacts first:
#   cd services/installer
#   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o ../../build/installer ./cmd/installer
#   npm run build --workspace=services/installer/web
#   cp -r services/installer/web/build ../../build/installer-web
#   git add -f build/
#   nix build .#packages.x86_64-linux.iso

{ config, pkgs, lib, modulesPath, ... }:

{
  imports = [
    (modulesPath + "/installer/cd-dvd/iso-image.nix")
    (modulesPath + "/profiles/all-hardware.nix")
    ./modules/installer.nix
  ];

  # ISO image settings
  isoImage = {
    isoName = "bloud-${config.system.nixos.label}.iso";
    volumeID = "BLOUD";
    makeEfiBootable = true;
    makeUsbBootable = true;
  };

  # Boot loader
  boot.loader.timeout = lib.mkForce 5;

  # RAM-based root (stateless live environment)
  fileSystems."/" = lib.mkForce {
    device = "tmpfs";
    fsType = "tmpfs";
    options = [ "size=80%" "mode=755" ];
  };

  # QEMU guest agent for Proxmox/KVM testing
  services.qemuGuest.enable = true;

  # Network
  networking = {
    hostName = "bloud";
    useDHCP = true;

    firewall = {
      enable = true;
      allowedTCPPorts = [
        22    # SSH (for debug access)
        80    # HTTP (iptables redirects to installer port)
        3001  # Installer service
      ];
    };

    # Redirect port 80 → installer (rootless-friendly pattern)
    firewall.extraCommands = ''
      iptables -t nat -A PREROUTING -p tcp --dport 80 -j REDIRECT --to-port 3001
    '';
    firewall.extraStopCommands = ''
      iptables -t nat -D PREROUTING -p tcp --dport 80 -j REDIRECT --to-port 3001 || true
    '';
  };

  # mDNS so browsers can reach http://bloud.local
  services.avahi = {
    enable = true;
    nssmdns4 = true;
    publish = {
      enable = true;
      addresses = true;
    };
  };

  # Installer service
  bloud.installer = {
    enable = true;
  };

  # SSH for emergency debug access (password auth, no keys required on ISO)
  services.openssh = {
    enable = true;
    settings = {
      PermitRootLogin = "yes";
      PasswordAuthentication = true;
      PermitEmptyPasswords = true;
    };
  };

  # Allow PAM to accept empty passwords (required for sshpass -p "" to work)
  security.pam.services.sshd.allowNullPassword = true;

  # Root password for SSH debug access on the installer
  users.users.root.initialHashedPassword = "";

  # Terminal welcome banner shown before the login prompt
  # Directs users to the web UI
  environment.etc."issue".text = ''

    ╔══════════════════════════════════════════════════╗
    ║                     bloud                       ║
    ╠══════════════════════════════════════════════════╣
    ║                                                  ║
    ║  Open a browser and visit:                       ║
    ║                                                  ║
    ║      http://bloud.local                          ║
    ║                                                  ║
    ║  to set up your server.                          ║
    ║                                                  ║
    ╚══════════════════════════════════════════════════╝

  '';

  # Nix settings (needed for nixos-install to work during installation)
  nix.settings = {
    experimental-features = [ "nix-command" "flakes" ];
    trusted-users = [ "root" ];
  };

  # Disk tools available in the installer environment
  environment.systemPackages = with pkgs; [
    parted
    util-linux
    dosfstools
    e2fsprogs
    cryptsetup    # LUKS encryption
    vim
    curl
    jq
  ];

  system.stateVersion = "24.11";
}
