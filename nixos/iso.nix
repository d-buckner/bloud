# Bloud Appliance ISO
#
# Bootable NixOS image that runs Bloud automatically on startup.
# Stateless (RAM-based) — data is lost on reboot. Future work for disk persistence.
#
# Build with: nix build .#packages.x86_64-linux.iso
# Test with:  ./scripts/test-iso.sh

{ config, pkgs, lib, modulesPath, ... }:

{
  imports = [
    (modulesPath + "/installer/cd-dvd/iso-image.nix")
    (modulesPath + "/profiles/all-hardware.nix")
    ./bloud.nix
    ./modules/host-agent.nix
  ];

  # ISO image settings
  isoImage = {
    isoName = "bloud-${config.system.nixos.label}.iso";
    volumeID = "BLOUD";
    makeEfiBootable = true;
    makeUsbBootable = true;
  };

  # Boot loader (override iso-image.nix default of 10)
  boot.loader.timeout = lib.mkForce 5;

  # QEMU guest agent for Proxmox/KVM integration
  services.qemuGuest.enable = true;

  # Network
  networking = {
    hostName = "bloud";
    useDHCP = true;

    firewall = {
      enable = true;
      allowedTCPPorts = [
        22    # SSH
        80    # HTTP (redirected to 8080)
        8080  # Traefik (main entry point)
      ];
    };
  };

  # Redirect port 80 -> 8080 (rootless containers can't bind privileged ports)
  networking.firewall.extraCommands = ''
    iptables -t nat -A PREROUTING -p tcp --dport 80 -j REDIRECT --to-port 8080
  '';
  networking.firewall.extraStopCommands = ''
    iptables -t nat -D PREROUTING -p tcp --dport 80 -j REDIRECT --to-port 8080 || true
  '';

  # Bloud user
  users.users.bloud = {
    isNormalUser = true;
    uid = 1000;
    description = "Bloud service user";
    extraGroups = [ "wheel" "podman" ];
    initialPassword = "bloud";
    linger = true;
    openssh.authorizedKeys.keys = [
      # Add SSH keys here or via first-boot setup
    ];
  };

  # Passwordless sudo for bloud user (needed for nixos-rebuild)
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

  # System service to start user services after boot
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
        ${pkgs.systemd}/bin/machinectl shell bloud@ /run/current-system/sw/bin/systemctl --user daemon-reload || true
        ${pkgs.systemd}/bin/machinectl shell bloud@ /run/current-system/sw/bin/systemctl --user start bloud-apps.target || true
      '';
      ExecReload = pkgs.writeShellScript "reload-bloud-user-services" ''
        ${pkgs.systemd}/bin/machinectl shell bloud@ /run/current-system/sw/bin/systemctl --user daemon-reload || true
        ${pkgs.systemd}/bin/machinectl shell bloud@ /run/current-system/sw/bin/systemctl --user restart bloud-apps.target || true
      '';
    };
  };

  # First-boot secrets initialization
  systemd.services.bloud-init-secrets = {
    description = "Initialize Bloud secrets on first boot";
    after = [ "bloud-user-services.service" ];
    requires = [ "bloud-user-services.service" ];
    before = [ "bloud-host-agent.service" ];
    wantedBy = [ "multi-user.target" ];

    unitConfig = {
      ConditionPathExists = "!/home/bloud/.local/share/bloud/secrets.json";
    };

    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      User = "bloud";
      Group = "users";
      ExecStart = "${config.bloud.host-agent.package}/bin/host-agent init-secrets";
      Environment = "BLOUD_DATA_DIR=/home/bloud/.local/share/bloud";
    };
  };

  # Bloud infrastructure
  bloud = {
    enable = true;
    user = "bloud";
    agentPath = "${config.bloud.host-agent.package}/bin/host-agent";
    externalHost = "http://bloud.local";
    authentikExternalHost = "http://bloud.local";
  };

  # Host agent (Nix-built binary with bundled frontend)
  bloud.host-agent = {
    enable = true;
    flakeTarget = "iso";
  };

  # Route UI traffic to host-agent (serves built SPA) instead of Vite dev server
  bloud.apps.traefik.uiPort = 3000;

  # SSH server
  services.openssh = {
    enable = true;
    settings = {
      PermitRootLogin = "no";
      PasswordAuthentication = true;
    };
  };

  # mDNS for bloud.local resolution
  services.avahi = {
    enable = true;
    nssmdns4 = true;
    publish = {
      enable = true;
      addresses = true;
    };
  };

  # Nix settings
  nix.settings = {
    experimental-features = [ "nix-command" "flakes" ];
    trusted-users = [ "root" "bloud" ];
  };

  # Minimal system packages (no dev tools)
  environment.systemPackages = with pkgs; [
    vim
    curl
    htop
    jq
  ];

  system.stateVersion = "24.11";
}
